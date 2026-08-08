package interlinking

import (
	"testing"

	"github.com/SEObserver/crawlobserver/internal/storage"
)

func TestWithVirtualLinks(t *testing.T) {
	g := &storage.PageRankGraph{
		N:             3,
		OutLinks:      [][]uint32{{1}, {2}, {}},
		TotalOutLinks: []uint32{1, 1, 0},
		URLToID:       map[string]uint32{"a": 0, "b": 1, "c": 2},
		IDToURL:       []string{"a", "b", "c"},
	}

	gv := WithVirtualLinks(g, []storage.VirtualLink{{SourceURL: "c", TargetURL: "a"}})

	// Original should be unchanged
	if len(g.OutLinks[2]) != 0 {
		t.Error("original graph should not be mutated")
	}
	if g.TotalOutLinks[2] != 0 {
		t.Error("original totalOutLinks should not be mutated")
	}

	// New graph should have the extra edge
	if len(gv.OutLinks[2]) != 1 || gv.OutLinks[2][0] != 0 {
		t.Error("virtual link c→a not added")
	}
	if gv.TotalOutLinks[2] != 1 {
		t.Error("totalOutLinks[2] should be 1")
	}
}

func TestWithVirtualLinksIgnoresInvalid(t *testing.T) {
	g := &storage.PageRankGraph{
		N:             2,
		OutLinks:      [][]uint32{{}, {}},
		TotalOutLinks: []uint32{0, 0},
		URLToID:       map[string]uint32{"a": 0, "b": 1},
		IDToURL:       []string{"a", "b"},
	}

	gv := WithVirtualLinks(g, []storage.VirtualLink{
		{SourceURL: "a", TargetURL: "a"},       // self-link
		{SourceURL: "a", TargetURL: "unknown"}, // unknown target
	})

	if len(gv.OutLinks[0]) != 0 {
		t.Error("invalid virtual links should be ignored")
	}
}

func TestWithVirtualLinkChangesRemovesInternalEdge(t *testing.T) {
	g := &storage.PageRankGraph{
		N:             3,
		OutLinks:      [][]uint32{{1, 2}, {2}, {}},
		TotalOutLinks: []uint32{2, 1, 0},
		URLToID:       map[string]uint32{"a": 0, "b": 1, "c": 2},
		IDToURL:       []string{"a", "b", "c"},
	}

	gv := WithVirtualLinkChanges(g, nil, []storage.VirtualLink{{SourceURL: "a", TargetURL: "b"}})

	if len(g.OutLinks[0]) != 2 || g.TotalOutLinks[0] != 2 {
		t.Error("original graph should not be mutated")
	}
	if len(gv.OutLinks[0]) != 1 || gv.OutLinks[0][0] != 2 {
		t.Fatalf("expected only a->c to remain, got %#v", gv.OutLinks[0])
	}
	if gv.TotalOutLinks[0] != 1 {
		t.Fatalf("expected total outlinks to decrease to 1, got %d", gv.TotalOutLinks[0])
	}
}

func TestWithVirtualLinkChangesResolvesWWWVariants(t *testing.T) {
	g := &storage.PageRankGraph{
		N:             2,
		OutLinks:      [][]uint32{{1}, {}},
		TotalOutLinks: []uint32{1, 0},
		URLToID: map[string]uint32{
			"https://www.example.com/source/": 0,
			"https://www.example.com/target/": 1,
		},
		IDToURL: []string{"https://www.example.com/source/", "https://www.example.com/target/"},
	}

	gv := WithVirtualLinkChanges(g, nil, []storage.VirtualLink{{
		SourceURL: "https://example.com/source/",
		TargetURL: "https://example.com/target/",
	}})

	if len(gv.OutLinks[0]) != 0 {
		t.Fatalf("expected www-equivalent target to be removed, got %#v", gv.OutLinks[0])
	}
	if gv.TotalOutLinks[0] != 0 {
		t.Fatalf("expected total outlinks to decrease to 0, got %d", gv.TotalOutLinks[0])
	}
}

func TestWithVirtualLinkChangesRemovesExternalDilutionOnly(t *testing.T) {
	g := &storage.PageRankGraph{
		N:             2,
		OutLinks:      [][]uint32{{1}, {}},
		TotalOutLinks: []uint32{2, 0},
		URLToID:       map[string]uint32{"a": 0, "b": 1},
		IDToURL:       []string{"a", "b"},
	}

	gv := WithVirtualLinkChanges(g, nil, []storage.VirtualLink{{SourceURL: "a", TargetURL: "https://external.example/"}})

	if len(gv.OutLinks[0]) != 1 || gv.OutLinks[0][0] != 1 {
		t.Fatalf("internal edge should remain, got %#v", gv.OutLinks[0])
	}
	if gv.TotalOutLinks[0] != 1 {
		t.Fatalf("expected external removal to reduce dilution only, got %d", gv.TotalOutLinks[0])
	}
}

func TestWithVirtualLinkChangesIgnoresSelfLinkRemoval(t *testing.T) {
	g := &storage.PageRankGraph{
		N:             2,
		OutLinks:      [][]uint32{{1}, {}},
		TotalOutLinks: []uint32{1, 0},
		URLToID:       map[string]uint32{"a": 0, "b": 1},
		IDToURL:       []string{"a", "b"},
	}

	gv := WithVirtualLinkChanges(g, nil, []storage.VirtualLink{{SourceURL: "a", TargetURL: "a"}})

	if len(gv.OutLinks[0]) != 1 || gv.OutLinks[0][0] != 1 {
		t.Fatalf("internal edge should remain, got %#v", gv.OutLinks[0])
	}
	if gv.TotalOutLinks[0] != 1 {
		t.Fatalf("self-link removal should not change total outlinks, got %d", gv.TotalOutLinks[0])
	}
}

func TestSimulationDiff(t *testing.T) {
	g := &storage.PageRankGraph{
		N:             3,
		OutLinks:      [][]uint32{{1}, {2}, {}},
		TotalOutLinks: []uint32{1, 1, 0},
		URLToID:       map[string]uint32{"a": 0, "b": 1, "c": 2},
		IDToURL:       []string{"a", "b", "c"},
	}

	before := storage.ComputePageRankIterations(g.N, g.OutLinks, g.TotalOutLinks)
	gv := WithVirtualLinks(g, []storage.VirtualLink{{SourceURL: "c", TargetURL: "a"}})
	after := storage.ComputePageRankIterations(gv.N, gv.OutLinks, gv.TotalOutLinks)

	totalBefore := before[0] + before[1] + before[2]
	totalAfter := after[0] + after[1] + after[2]

	if totalBefore < 100 || totalAfter < 100 {
		t.Errorf("unexpected totals: before=%.2f, after=%.2f", totalBefore, totalAfter)
	}

	t.Logf("Before: A=%.2f B=%.2f C=%.2f", before[0], before[1], before[2])
	t.Logf("After:  A=%.2f B=%.2f C=%.2f", after[0], after[1], after[2])
}

func TestRemovalSimulationUsesStableNormalization(t *testing.T) {
	g := &storage.PageRankGraph{
		N:             3,
		OutLinks:      [][]uint32{{1}, {2}, {}},
		TotalOutLinks: []uint32{1, 1, 0},
		URLToID:       map[string]uint32{"source": 0, "target": 1, "leaf": 2},
		IDToURL:       []string{"source", "target", "leaf"},
	}

	beforeRaw := storage.ComputePageRankRawIterations(g.N, g.OutLinks, g.TotalOutLinks)
	referenceMax := maxPageRankScore(beforeRaw)
	before := storage.NormalizePageRankScores(beforeRaw, referenceMax)

	gv := WithVirtualLinkChanges(g, nil, []storage.VirtualLink{
		{SourceURL: "source", TargetURL: "target"},
	})
	afterRaw := storage.ComputePageRankRawIterations(gv.N, gv.OutLinks, gv.TotalOutLinks)
	after := storage.NormalizePageRankScores(afterRaw, referenceMax)

	if after[1] >= before[1] {
		t.Fatalf("target page should lose PageRank when its inbound link is removed: before=%.4f after=%.4f", before[1], after[1])
	}
}
