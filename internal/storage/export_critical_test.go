package storage

import (
	"testing"
	"time"
)

func TestGSCAnalyticsDailyChunks(t *testing.T) {
	minDate := time.Date(2026, 6, 7, 15, 30, 0, 0, time.FixedZone("test", 3*60*60))
	maxDate := time.Date(2026, 6, 9, 2, 0, 0, 0, time.UTC)

	chunks := gscAnalyticsDailyChunks("project-1", minDate, maxDate)
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, want 3", len(chunks))
	}

	wantStarts := []time.Time{
		time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC),
	}
	for i, want := range wantStarts {
		if chunks[i].ProjectID != "project-1" {
			t.Fatalf("chunks[%d].ProjectID = %q, want project-1", i, chunks[i].ProjectID)
		}
		if !chunks[i].StartDate.Equal(want) {
			t.Fatalf("chunks[%d].StartDate = %s, want %s", i, chunks[i].StartDate, want)
		}
		if !chunks[i].EndDate.Equal(want.AddDate(0, 0, 1)) {
			t.Fatalf("chunks[%d].EndDate = %s, want %s", i, chunks[i].EndDate, want.AddDate(0, 0, 1))
		}
	}
}

func TestGSCAnalyticsDailyChunksRejectsInvalidRange(t *testing.T) {
	minDate := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	maxDate := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)

	if chunks := gscAnalyticsDailyChunks("project-1", minDate, maxDate); chunks != nil {
		t.Fatalf("chunks = %#v, want nil", chunks)
	}
	if chunks := gscAnalyticsDailyChunks("", minDate, minDate); chunks != nil {
		t.Fatalf("chunks = %#v, want nil for empty project", chunks)
	}
}
