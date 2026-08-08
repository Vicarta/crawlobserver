package interlinking

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

// WithVirtualLinks returns a copy of the graph with additional edges injected.
func WithVirtualLinks(g *storage.PageRankGraph, links []storage.VirtualLink) *storage.PageRankGraph {
	return WithVirtualLinkChanges(g, links, nil)
}

// WithVirtualLinkChanges returns a copy of the graph with additions/removals applied.
func WithVirtualLinkChanges(g *storage.PageRankGraph, addLinks, removeLinks []storage.VirtualLink) *storage.PageRankGraph {
	newOutLinks := make([][]uint32, g.N)
	newTotalOutLinks := make([]uint32, g.N)
	for i := uint32(0); i < g.N; i++ {
		if len(g.OutLinks[i]) > 0 {
			newOutLinks[i] = make([]uint32, len(g.OutLinks[i]))
			copy(newOutLinks[i], g.OutLinks[i])
		}
		newTotalOutLinks[i] = g.TotalOutLinks[i]
	}

	for _, vl := range removeLinks {
		if vl.SourceURL == vl.TargetURL {
			continue
		}
		srcID, srcOK := resolveGraphURLID(g, vl.SourceURL)
		if !srcOK {
			continue
		}
		if newTotalOutLinks[srcID] > 0 {
			newTotalOutLinks[srcID]--
		}
		tgtID, tgtOK := resolveGraphURLID(g, vl.TargetURL)
		if !tgtOK || srcID == tgtID {
			continue
		}
		newOutLinks[srcID] = removeLinkTarget(newOutLinks[srcID], tgtID)
	}

	for _, vl := range addLinks {
		srcID, srcOK := resolveGraphURLID(g, vl.SourceURL)
		tgtID, tgtOK := resolveGraphURLID(g, vl.TargetURL)
		if !srcOK || !tgtOK || srcID == tgtID {
			continue
		}
		newOutLinks[srcID] = append(newOutLinks[srcID], tgtID)
		newTotalOutLinks[srcID]++
	}

	return &storage.PageRankGraph{
		N:             g.N,
		OutLinks:      newOutLinks,
		TotalOutLinks: newTotalOutLinks,
		URLToID:       g.URLToID,
		IDToURL:       g.IDToURL,
	}
}

func resolveGraphURLID(g *storage.PageRankGraph, rawURL string) (uint32, bool) {
	for _, candidate := range urlWWWVariants(rawURL) {
		if id, ok := g.URLToID[candidate]; ok {
			return id, true
		}
	}
	return 0, false
}

func urlWWWVariants(rawURL string) []string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil
	}
	seen := map[string]struct{}{trimmed: {}}
	result := []string{trimmed}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return result
	}
	host := u.Host
	if strings.HasPrefix(strings.ToLower(host), "www.") {
		u.Host = host[4:]
	} else {
		u.Host = "www." + host
	}
	alt := u.String()
	if _, ok := seen[alt]; !ok {
		result = append(result, alt)
	}
	return result
}

func removeLinkTarget(out []uint32, target uint32) []uint32 {
	if len(out) == 0 {
		return out
	}
	next := out[:0]
	for _, existing := range out {
		if existing != target {
			next = append(next, existing)
		}
	}
	return next
}

// SimulationStore is the subset of storage needed for PageRank simulation.
type SimulationStore interface {
	LoadPageRankGraph(ctx context.Context, sessionID string) (*storage.PageRankGraph, error)
	InsertSimulation(ctx context.Context, sessionID string, simID string, virtualLinks []storage.VirtualLink, results []storage.SimulationResultRow, meta storage.SimulationMeta) error
}

// SimulateResult holds the outcome of a PageRank simulation.
type SimulateResult struct {
	SimulationID  string
	PagesImproved uint32
	PagesDeclined uint32
	AvgDiff       float64
	MaxDiff       float64
	Results       []storage.SimulationResultRow
}

// SimulatePageRank computes PageRank before/after adding virtual links.
func SimulatePageRank(ctx context.Context, store SimulationStore, sessionID, simID string, links []storage.VirtualLink) (*SimulateResult, error) {
	return SimulatePageRankChanges(ctx, store, sessionID, simID, links, nil)
}

// SimulatePageRankChanges computes PageRank before/after adding and removing links.
func SimulatePageRankChanges(ctx context.Context, store SimulationStore, sessionID, simID string, addLinks, removeLinks []storage.VirtualLink) (*SimulateResult, error) {
	start := time.Now()

	applog.Info("interlinking", "Loading PageRank graph for simulation...")
	graph, err := store.LoadPageRankGraph(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading graph: %w", err)
	}
	applog.Infof("interlinking", "Graph loaded: %d nodes", graph.N)

	if graph.N == 0 {
		return nil, fmt.Errorf("empty graph")
	}

	// Compute before and after on raw scores, then normalize both with the
	// same baseline max so simulation diffs compare like-for-like.
	beforeRaw := storage.ComputePageRankRawIterations(graph.N, graph.OutLinks, graph.TotalOutLinks)
	referenceMax := maxPageRankScore(beforeRaw)
	before := storage.NormalizePageRankScores(beforeRaw, referenceMax)

	// Apply virtual changes and compute after
	graphWith := WithVirtualLinkChanges(graph, addLinks, removeLinks)
	afterRaw := storage.ComputePageRankRawIterations(graphWith.N, graphWith.OutLinks, graphWith.TotalOutLinks)
	after := storage.NormalizePageRankScores(afterRaw, referenceMax)

	// Compute diffs
	var (
		improved, declined uint32
		totalDiff          float64
		maxDiff            float64
	)
	results := make([]storage.SimulationResultRow, graph.N)
	for i := uint32(0); i < graph.N; i++ {
		diff := after[i] - before[i]
		results[i] = storage.SimulationResultRow{
			URL:            graph.IDToURL[i],
			PageRankBefore: before[i],
			PageRankAfter:  after[i],
			PageRankDiff:   diff,
		}
		if diff > 0.001 {
			improved++
		} else if diff < -0.001 {
			declined++
		}
		totalDiff += diff
		absDiff := math.Abs(diff)
		if absDiff > maxDiff {
			maxDiff = absDiff
		}
	}

	avgDiff := 0.0
	if graph.N > 0 {
		avgDiff = totalDiff / float64(graph.N)
	}

	meta := storage.SimulationMeta{
		ID:                simID,
		CrawlSessionID:    sessionID,
		VirtualLinksCount: uint32(len(addLinks) + len(removeLinks)),
		PagesImproved:     improved,
		PagesDeclined:     declined,
		AvgDiff:           avgDiff,
		MaxDiff:           maxDiff,
		ComputedAt:        time.Now(),
	}

	if err := store.InsertSimulation(ctx, sessionID, simID, append(addLinks, removeLinks...), results, meta); err != nil {
		return nil, fmt.Errorf("storing simulation: %w", err)
	}

	applog.Infof("interlinking", "Simulation complete: %d improved, %d declined, avg diff %.4f in %s",
		improved, declined, avgDiff, time.Since(start))

	return &SimulateResult{
		SimulationID:  simID,
		PagesImproved: improved,
		PagesDeclined: declined,
		AvgDiff:       avgDiff,
		MaxDiff:       maxDiff,
		Results:       results,
	}, nil
}

func maxPageRankScore(scores []float64) float64 {
	var maxScore float64
	for _, score := range scores {
		if score > maxScore {
			maxScore = score
		}
	}
	return maxScore
}
