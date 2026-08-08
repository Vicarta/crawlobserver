package storage

import (
	"strings"
	"testing"
)

func TestCoreWebVitalRatingThresholds(t *testing.T) {
	tests := []struct {
		name     string
		value    float64
		goodMax  float64
		needsMax float64
		want     string
	}{
		{name: "good boundary", value: 2500, goodMax: 2500, needsMax: 4000, want: "good"},
		{name: "needs improvement", value: 2500.1, goodMax: 2500, needsMax: 4000, want: "needs_improvement"},
		{name: "needs boundary", value: 4000, goodMax: 2500, needsMax: 4000, want: "needs_improvement"},
		{name: "poor", value: 4000.1, goodMax: 2500, needsMax: 4000, want: "poor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := coreWebVitalRating(tt.value, tt.goodMax, tt.needsMax); got != tt.want {
				t.Fatalf("coreWebVitalRating() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCWVSortFallsBackToWorstFirst(t *testing.T) {
	expr, order := cwvSort("invalid", "invalid")
	if expr == "" || order != "DESC" {
		t.Fatalf("cwvSort fallback = (%q, %q), want non-empty expression and DESC", expr, order)
	}

	if got := normalizeCWVRating("needs_improvement"); got != "needs_improvement" {
		t.Fatalf("normalizeCWVRating() = %q", got)
	}
	if got := normalizeCWVRating("invalid"); got != "" {
		t.Fatalf("normalizeCWVRating(invalid) = %q, want empty", got)
	}
}

func TestCWVValidMeasurementRejectsZeroTimings(t *testing.T) {
	for _, fragment := range []string{"cwv_lcp_ms > 0", "cwv_ttfb_ms > 0"} {
		if !strings.Contains(cwvValidMeasurementFilter, fragment) {
			t.Fatalf("valid CWV measurement filter is missing %q", fragment)
		}
	}
}
