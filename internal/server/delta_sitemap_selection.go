package server

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/fetcher"
)

const (
	DeltaSitemapSelectorRevision = "v1"

	DeltaSitemapSourceAdded              = "sitemap_added"
	DeltaSitemapSourceLastModForward     = "sitemap_lastmod_forward"
	DeltaSitemapSourcePendingUnpublished = "sitemap_pending_unpublished"
	DeltaSitemapSourceCanary             = "sitemap_canary"
)

// DeltaSitemapSelectionURL is a normalized sitemap identity with its raw
// lastmod evidence. URL normalization happens at the planner boundary; this
// selector intentionally has no URL policy or storage dependency.
type DeltaSitemapSelectionURL struct {
	URL     string
	LastMod string
}

// DeltaSitemapSelectionInput contains the three sitemap observation terms and
// persisted bounds used by the pure selection contract.
type DeltaSitemapSelectionInput struct {
	ProjectID                 string
	PublishedSnapshotRevision uint64
	RotationEpoch             time.Time
	Fresh                     []DeltaSitemapSelectionURL
	Raw                       []DeltaSitemapSelectionURL
	Published                 []DeltaSitemapSelectionURL
	ChangedLimit              int
	CanaryCount               int
	MaxCandidates             int
}

// DeltaSitemapSelectedURL is one selected candidate with a stable source.
// EventKind preserves whether a pending-unpublished retry was originally an
// added URL or a forward-lastmod URL.
type DeltaSitemapSelectedURL struct {
	URL       string
	LastMod   string
	Source    string
	EventKind string
}

// DeltaSitemapSelection is the deterministic result of changed-only sitemap
// planning. DeferredEvents always contains every event that was not selected;
// SelectionComplete is false whenever that set is non-empty.
type DeltaSitemapSelection struct {
	Selected          []DeltaSitemapSelectedURL
	DeferredEvents    []DeltaSitemapSelectedURL
	EventTotal        int
	EventSelected     int
	EventDeferred     int
	CanarySelected    int
	SelectionComplete bool
	SourceByURL       map[string]string
}

// SelectDeltaSitemapCandidates compares Fresh against Published (the safety
// term) and uses Raw only for retry provenance. In particular, an event that
// Raw already observed remains selected while Published is still behind.
func SelectDeltaSitemapCandidates(input DeltaSitemapSelectionInput) DeltaSitemapSelection {
	fresh := canonicalSitemapSelectionURLs(input.Fresh)
	raw := sitemapSelectionURLMap(input.Raw)
	published := sitemapSelectionURLMap(input.Published)

	events := make([]DeltaSitemapSelectedURL, 0)
	unchanged := make([]DeltaSitemapSelectionURL, 0)
	for _, candidate := range fresh {
		baseline, existsInPublished := published[candidate.URL]
		kind := ""
		switch {
		case !existsInPublished:
			kind = DeltaSitemapSourceAdded
		case fetcher.SitemapLastModStrictlyForward(candidate.LastMod, baseline.LastMod):
			kind = DeltaSitemapSourceLastModForward
		default:
			unchanged = append(unchanged, candidate)
			continue
		}

		source := kind
		if !eventAheadOfRaw(candidate, raw[candidate.URL], rawHasURL(raw, candidate.URL)) {
			source = DeltaSitemapSourcePendingUnpublished
		}
		events = append(events, DeltaSitemapSelectedURL{
			URL:       candidate.URL,
			LastMod:   candidate.LastMod,
			Source:    source,
			EventKind: kind,
		})
	}

	sort.Slice(events, func(i, j int) bool {
		if events[i].URL != events[j].URL {
			return events[i].URL < events[j].URL
		}
		return events[i].EventKind < events[j].EventKind
	})

	changedLimit := nonNegative(input.ChangedLimit)
	if changedLimit == 0 {
		changedLimit = len(events)
	}
	maxCandidates := nonNegative(input.MaxCandidates)
	eventCapacity := changedLimit
	if maxCandidates < eventCapacity {
		eventCapacity = maxCandidates
	}
	if eventCapacity > len(events) {
		eventCapacity = len(events)
	}

	selected := append([]DeltaSitemapSelectedURL(nil), events[:eventCapacity]...)
	deferred := append([]DeltaSitemapSelectedURL(nil), events[eventCapacity:]...)

	remaining := maxCandidates - len(selected)
	canaryCount := nonNegative(input.CanaryCount)
	if remaining < canaryCount {
		canaryCount = remaining
	}
	if canaryCount > 0 {
		sort.Slice(unchanged, func(i, j int) bool {
			left := sitemapCanaryRank(input.ProjectID, input.PublishedSnapshotRevision, input.RotationEpoch, unchanged[i].URL)
			right := sitemapCanaryRank(input.ProjectID, input.PublishedSnapshotRevision, input.RotationEpoch, unchanged[j].URL)
			if left != right {
				return left < right
			}
			return unchanged[i].URL < unchanged[j].URL
		})
		if canaryCount > len(unchanged) {
			canaryCount = len(unchanged)
		}
		for _, candidate := range unchanged[:canaryCount] {
			selected = append(selected, DeltaSitemapSelectedURL{
				URL:     candidate.URL,
				LastMod: candidate.LastMod,
				Source:  DeltaSitemapSourceCanary,
			})
		}
	}

	sources := make(map[string]string, len(selected)+len(deferred))
	for _, candidate := range selected {
		sources[candidate.URL] = candidate.Source
	}
	for _, candidate := range deferred {
		sources[candidate.URL] = candidate.Source
	}
	return DeltaSitemapSelection{
		Selected:          selected,
		DeferredEvents:    deferred,
		EventTotal:        len(events),
		EventSelected:     eventCapacity,
		EventDeferred:     len(deferred),
		CanarySelected:    canaryCount,
		SelectionComplete: len(deferred) == 0,
		SourceByURL:       sources,
	}
}

func canonicalSitemapSelectionURLs(values []DeltaSitemapSelectionURL) []DeltaSitemapSelectionURL {
	byURL := make(map[string]DeltaSitemapSelectionURL, len(values))
	for _, value := range values {
		value.URL = strings.TrimSpace(value.URL)
		if value.URL == "" {
			continue
		}
		existing, found := byURL[value.URL]
		if !found || sitemapSelectionValueAfter(value.LastMod, existing.LastMod) {
			byURL[value.URL] = value
		}
	}
	result := make([]DeltaSitemapSelectionURL, 0, len(byURL))
	for _, value := range byURL {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].URL < result[j].URL })
	return result
}

func sitemapSelectionValueAfter(candidate, existing string) bool {
	comparison, comparable := fetcher.CompareSitemapLastMod(candidate, existing)
	if comparable {
		if comparison != 0 {
			return comparison > 0
		}
		return candidate > existing
	}
	_, candidateErr := fetcher.ParseSitemapLastMod(candidate)
	_, existingErr := fetcher.ParseSitemapLastMod(existing)
	if candidateErr == nil || existingErr == nil {
		return candidateErr == nil
	}
	return candidate > existing
}

func sitemapSelectionURLMap(values []DeltaSitemapSelectionURL) map[string]DeltaSitemapSelectionURL {
	result := make(map[string]DeltaSitemapSelectionURL, len(values))
	for _, value := range canonicalSitemapSelectionURLs(values) {
		result[value.URL] = value
	}
	return result
}

func rawHasURL(values map[string]DeltaSitemapSelectionURL, url string) bool {
	_, ok := values[url]
	return ok
}

func eventAheadOfRaw(candidate, raw DeltaSitemapSelectionURL, rawExists bool) bool {
	if !rawExists {
		return true
	}
	return fetcher.SitemapLastModStrictlyForward(candidate.LastMod, raw.LastMod)
}

func sitemapCanaryRank(projectID string, snapshotRevision uint64, epoch time.Time, url string) string {
	identity := strings.Join([]string{
		projectID,
		fmtUint(snapshotRevision),
		epoch.UTC().Format(time.RFC3339Nano),
		url,
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func fmtUint(value uint64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var result [20]byte
	i := len(result)
	for value > 0 {
		i--
		result[i] = digits[value%10]
		value /= 10
	}
	return string(result[i:])
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
