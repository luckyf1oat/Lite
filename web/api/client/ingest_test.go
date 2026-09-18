package client

import (
	"testing"
	"time"
)

func TestPingResultTimeHonorsRecentFinishedAt(t *testing.T) {
	now := time.Date(2026, 9, 18, 11, 4, 0, 0, time.UTC)
	previous := now.Add(-90 * time.Second)

	if got := pingResultTimeAt(time.Time{}, now); !got.Equal(now) {
		t.Fatalf("zero finished_at = %v, want %v", got, now)
	}
	if got := pingResultTimeAt(previous, now); !got.Equal(previous) {
		t.Fatalf("recent finished_at = %v, want %v", got, previous)
	}
	if got := pingResultTimeAt(now.Add(-7*time.Hour), now); !got.Equal(now) {
		t.Fatalf("old finished_at should clamp to now, got %v", got)
	}
	if got := pingResultTimeAt(now.Add(2*time.Minute), now); !got.Equal(now) {
		t.Fatalf("future finished_at should clamp to now, got %v", got)
	}
}
