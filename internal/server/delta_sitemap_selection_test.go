package server

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestDeltaSitemapSelectionPublishedTermControlsPendingEvents(t *testing.T) {
	selection := SelectDeltaSitemapCandidates(DeltaSitemapSelectionInput{
		ProjectID:                 "project-a",
		PublishedSnapshotRevision: 7,
		RotationEpoch:             time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC),
		ChangedLimit:              30,
		CanaryCount:               50,
		MaxCandidates:             80,
		Fresh: []DeltaSitemapSelectionURL{
			{URL: "https://example.test/added", LastMod: "2026-08-26"},
			{URL: "https://example.test/forward", LastMod: "2026-08-26T12:00:00Z"},
			{URL: "https://example.test/equal", LastMod: "2026-08-25"},
			{URL: "https://example.test/backward", LastMod: "2026-08-24"},
			{URL: "https://example.test/invalid", LastMod: "invalid"},
		},
		Raw: []DeltaSitemapSelectionURL{
			{URL: "https://example.test/added", LastMod: "2026-08-26"},
			{URL: "https://example.test/forward", LastMod: "2026-08-26T12:00:00Z"},
		},
		Published: []DeltaSitemapSelectionURL{
			{URL: "https://example.test/forward", LastMod: "2026-08-25T12:00:00Z"},
			{URL: "https://example.test/equal", LastMod: "2026-08-25"},
			{URL: "https://example.test/backward", LastMod: "2026-08-25"},
			{URL: "https://example.test/invalid", LastMod: "2026-08-25"},
		},
	})

	if selection.EventTotal != 2 || selection.EventSelected != 2 || !selection.SelectionComplete {
		t.Fatalf("event counts = %#v, want two selected complete events", selection)
	}
	if selection.SourceByURL["https://example.test/added"] != DeltaSitemapSourcePendingUnpublished ||
		selection.SourceByURL["https://example.test/forward"] != DeltaSitemapSourcePendingUnpublished {
		t.Fatalf("raw-observed events lost pending provenance: %#v", selection.SourceByURL)
	}
	if selection.CanarySelected != 1 || len(selection.Selected) != 3 {
		t.Fatalf("small unchanged cohort should contribute one canary after two events: %#v", selection)
	}
	for _, url := range []string{"https://example.test/equal", "https://example.test/backward", "https://example.test/invalid"} {
		if source := selection.SourceByURL[url]; source != "" && source != DeltaSitemapSourceCanary {
			t.Fatalf("unchanged URL %q became unexpected source %q", url, source)
		}
	}
}

func TestDeltaSitemapSelectionBoundsEventsBeforeCanaries(t *testing.T) {
	for _, eventCount := range []int{0, 1, 30, 31} {
		t.Run(fmt.Sprintf("%d events", eventCount), func(t *testing.T) {
			input := selectionFixture(eventCount, 60)
			selection := SelectDeltaSitemapCandidates(input)
			wantEvents := eventCount
			if wantEvents > 30 {
				wantEvents = 30
			}
			if selection.EventTotal != eventCount || selection.EventSelected != wantEvents || selection.EventDeferred != eventCount-wantEvents {
				t.Fatalf("event counts = %#v, want total=%d selected=%d deferred=%d", selection, eventCount, wantEvents, eventCount-wantEvents)
			}
			if selection.CanarySelected != 6 || len(selection.Selected) != wantEvents+6 {
				t.Fatalf("canary bound = %#v, want %d events then 10%% of 60 unchanged URLs", selection, wantEvents)
			}
			if selection.SelectionComplete != (eventCount <= 30) {
				t.Fatalf("SelectionComplete = %t, want %t", selection.SelectionComplete, eventCount <= 30)
			}
			for _, candidate := range selection.Selected[:wantEvents] {
				if candidate.Source == DeltaSitemapSourceCanary {
					t.Fatalf("canary selected before event: %#v", selection.Selected)
				}
			}
		})
	}
}

func TestDeltaSitemapSelectionCanaryCountsAndRotation(t *testing.T) {
	for _, canaryPool := range []int{1, 9, 10, 49, 50, 100, 158, 500} {
		t.Run(fmt.Sprintf("%d canaries", canaryPool), func(t *testing.T) {
			selection := SelectDeltaSitemapCandidates(selectionFixture(0, canaryPool))
			want := (canaryPool + 9) / 10
			if want > 50 {
				want = 50
			}
			if selection.CanarySelected != want || len(selection.Selected) != want {
				t.Fatalf("canary selection = %#v, want %d", selection, want)
			}
		})
	}

	configuredCap := selectionFixture(0, 2000)
	configuredCap.CanaryCount = 25
	selection := SelectDeltaSitemapCandidates(configuredCap)
	if selection.CanarySelected != 25 {
		t.Fatalf("configured canary cap = %d, want 25", selection.CanarySelected)
	}

	input := selectionFixture(0, 200)
	first := SelectDeltaSitemapCandidates(input)
	second := SelectDeltaSitemapCandidates(input)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same project/revision/epoch selection drifted:\nfirst=%#v\nsecond=%#v", first, second)
	}
	input.RotationEpoch = input.RotationEpoch.AddDate(0, 0, 1)
	rotated := SelectDeltaSitemapCandidates(input)
	if reflect.DeepEqual(first.Selected, rotated.Selected) {
		t.Fatalf("next UTC epoch did not rotate canaries: %#v", first.Selected)
	}
}

func TestDeltaSitemapSelectionDeduplicatesAndHonorsGlobalCap(t *testing.T) {
	input := selectionFixture(2, 20)
	input.Fresh = append(input.Fresh,
		DeltaSitemapSelectionURL{URL: " https://example.test/event-000 ", LastMod: "2026-08-25"},
		DeltaSitemapSelectionURL{URL: "https://example.test/canary-000", LastMod: ""},
	)
	input.MaxCandidates = 1
	selection := SelectDeltaSitemapCandidates(input)
	if selection.EventSelected != 1 || selection.EventDeferred != 1 || selection.CanarySelected != 0 || selection.SelectionComplete {
		t.Fatalf("global cap selection = %#v, want one event and incomplete selection", selection)
	}
	seen := map[string]bool{}
	for _, candidate := range append(append([]DeltaSitemapSelectedURL{}, selection.Selected...), selection.DeferredEvents...) {
		if seen[candidate.URL] {
			t.Fatalf("duplicate identity in selection: %#v", selection)
		}
		seen[candidate.URL] = true
	}
	if selection.Selected[0].LastMod != "2026-08-26" {
		t.Fatalf("duplicate identity retained %q, want latest valid lastmod", selection.Selected[0].LastMod)
	}
}

func TestDeltaSitemapSelectionZeroChangedLimitMeansAllEvidenceBackedChanges(t *testing.T) {
	input := selectionFixture(120, 60)
	input.ChangedLimit = 0
	input.MaxCandidates = 5000
	selection := SelectDeltaSitemapCandidates(input)
	if selection.EventSelected != 120 || selection.EventDeferred != 0 || selection.CanarySelected != 6 || !selection.SelectionComplete {
		t.Fatalf("unlimited changed selection = %#v; want 120 events plus 10%% of 60 unchanged URLs", selection)
	}
}

func TestDeltaSitemapSelectionRemainingGlobalCapacityLimitsCanaries(t *testing.T) {
	input := selectionFixture(75, 100)
	input.ChangedLimit = 0
	input.MaxCandidates = 80
	selection := SelectDeltaSitemapCandidates(input)
	if selection.EventSelected != 75 || selection.CanarySelected != 5 || len(selection.Selected) != 80 {
		t.Fatalf("global capacity selection = %#v; want 75 events plus 5 canaries", selection)
	}
}

func TestDeltaSitemapSelectionZeroGlobalMaximumDefersAllEventsAndCanaries(t *testing.T) {
	input := selectionFixture(3, 60)
	input.ChangedLimit = 0
	input.MaxCandidates = 0
	selection := SelectDeltaSitemapCandidates(input)
	if selection.EventSelected != 0 || selection.EventDeferred != 3 || selection.CanarySelected != 0 || selection.SelectionComplete {
		t.Fatalf("zero global maximum selection = %#v; want all events deferred and no canaries", selection)
	}
	if len(selection.Selected) != 0 || len(selection.DeferredEvents) != 3 {
		t.Fatalf("zero global maximum candidates = %#v; want zero selected and three deferred", selection)
	}
}

func TestDeltaSitemapSelectionStableUnpublishedSuppressesOnlyExactProof(t *testing.T) {
	input := DeltaSitemapSelectionInput{
		ProjectID:                 "project-a",
		PublishedSnapshotRevision: 7,
		RotationEpoch:             time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC),
		Fresh: []DeltaSitemapSelectionURL{
			{URL: "https://example.test/stable", LastMod: "2026-08-26T00:00:00Z"},
			{URL: "https://example.test/missing-proof", LastMod: "2026-08-26T00:00:00Z"},
			{URL: "https://example.test/newer-than-proof", LastMod: "2026-08-27T00:00:00Z"},
			{URL: "https://example.test/unchanged", LastMod: "2026-08-25"},
		},
		Published: []DeltaSitemapSelectionURL{
			{URL: "https://example.test/stable", LastMod: "2026-08-25"},
			{URL: "https://example.test/missing-proof", LastMod: "2026-08-25"},
			{URL: "https://example.test/newer-than-proof", LastMod: "2026-08-25"},
			{URL: "https://example.test/unchanged", LastMod: "2026-08-25"},
		},
		Stable: []DeltaSitemapStabilityProof{
			{URL: "https://example.test/stable", LastMod: "2026-08-26T00:00:00Z"},
			{URL: "https://example.test/newer-than-proof", LastMod: "2026-08-26T00:00:00Z"},
		},
		StabilityOlderSessionID: "older",
		StabilityNewerSessionID: "newer",
		StabilityProofDigest:    "proof-digest",
		StabilityLegacyPair:     true,
		ChangedLimit:            30,
		CanaryCount:             50,
		MaxCandidates:           80,
	}

	selection := SelectDeltaSitemapCandidates(input)
	if selection.PublishedDifferenceTotal != 3 || selection.ActionableTotal != 2 || selection.StableAcknowledgedTotal != 1 {
		t.Fatalf("difference classification = %#v", selection)
	}
	if selection.EventTotal != 2 || selection.EventSelected != 2 || selection.EventDeferred != 0 || selection.SelectedTotal != 3 {
		t.Fatalf("actionable event counts = %#v", selection)
	}
	if selection.SourceByURL["https://example.test/stable"] != DeltaSitemapSourceStableUnpublished {
		t.Fatalf("stable source = %#v", selection.SourceByURL)
	}
	if selection.SourceByURL["https://example.test/missing-proof"] != DeltaSitemapSourceLastModForward ||
		selection.SourceByURL["https://example.test/newer-than-proof"] != DeltaSitemapSourceLastModForward {
		t.Fatalf("unproven/newer tuples must remain actionable: %#v", selection.SourceByURL)
	}
	if !selection.PublicationHeld || !selection.SelectionComplete {
		t.Fatalf("stable-only acknowledgement must hold publication without pretending work is deferred: %#v", selection)
	}
	if selection.StabilityOlderSessionID != "older" || selection.StabilityNewerSessionID != "newer" ||
		selection.StabilityProofDigest != "proof-digest" || !selection.StabilityLegacyPair {
		t.Fatalf("stability lineage = %#v", selection)
	}
	for _, candidate := range append(selection.Selected, selection.DeferredEvents...) {
		if candidate.URL == "https://example.test/stable" {
			t.Fatalf("stable evidence was scheduled: %#v", selection)
		}
	}
}

func TestDeltaSitemapSelectionWithoutProofRetainsV1EventBehavior(t *testing.T) {
	input := selectionFixture(1, 0)
	selection := SelectDeltaSitemapCandidates(input)
	if selection.PublishedDifferenceTotal != 1 || selection.ActionableTotal != 1 || selection.StableAcknowledgedTotal != 0 || selection.PublicationHeld {
		t.Fatalf("proof-free selection changed v1 behavior: %#v", selection)
	}
}

func TestDeltaSitemapSelectionPrecisionDriftRemainsActionable(t *testing.T) {
	input := selectionFixture(1, 0)
	input.Fresh[0].LastMod = "2026-08-26T16:53:17Z"
	input.Stable = []DeltaSitemapStabilityProof{{URL: input.Fresh[0].URL, LastMod: "2026-08-26"}}
	selection := SelectDeltaSitemapCandidates(input)
	if selection.ActionableTotal != 1 || selection.StableAcknowledgedTotal != 0 {
		t.Fatalf("mixed-precision lastmod was suppressed: %#v", selection)
	}
}

func selectionFixture(eventCount, canaryCount int) DeltaSitemapSelectionInput {
	fresh := make([]DeltaSitemapSelectionURL, 0, eventCount+canaryCount)
	published := make([]DeltaSitemapSelectionURL, 0, canaryCount)
	for i := 0; i < eventCount; i++ {
		fresh = append(fresh, DeltaSitemapSelectionURL{
			URL:     fmt.Sprintf("https://example.test/event-%03d", i),
			LastMod: "2026-08-26",
		})
	}
	for i := 0; i < canaryCount; i++ {
		url := fmt.Sprintf("https://example.test/canary-%03d", i)
		fresh = append(fresh, DeltaSitemapSelectionURL{URL: url, LastMod: "2026-08-25"})
		published = append(published, DeltaSitemapSelectionURL{URL: url, LastMod: "2026-08-25"})
	}
	return DeltaSitemapSelectionInput{
		ProjectID:                 "project-a",
		PublishedSnapshotRevision: 7,
		RotationEpoch:             time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC),
		Fresh:                     fresh,
		Published:                 published,
		ChangedLimit:              30,
		CanaryCount:               50,
		MaxCandidates:             80,
	}
}
