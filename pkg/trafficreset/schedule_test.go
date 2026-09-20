package trafficreset

import (
	"testing"
	"time"
)

func TestExistingBeijingMidnightUnchanged(t *testing.T) {
	day := 26
	s := FromFields(&day, "", "")
	now := time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)
	if got := s.CycleKey(now); got != "2026-07-26" {
		t.Fatalf("CycleKey = %q, want 2026-07-26", got)
	}
	day = 1
	s = FromFields(&day, "", "")
	if got := s.CycleKey(now); got != "2026-08-01" {
		t.Fatalf("CycleKey day 1 = %q, want 2026-08-01", got)
	}
}

func TestResetWaitsUntilConfiguredClockInTimezone(t *testing.T) {
	day := 15
	s := FromFields(&day, "12:38:12", "UTC")
	before := time.Date(2026, 9, 15, 12, 38, 11, 0, time.UTC)
	if got := s.CycleKey(before); got != "2026-08-15" {
		t.Fatalf("before clock CycleKey = %q, want 2026-08-15", got)
	}
	at := time.Date(2026, 9, 15, 12, 38, 12, 0, time.UTC)
	if got := s.CycleKey(at); got != "2026-09-15" {
		t.Fatalf("at clock CycleKey = %q, want 2026-09-15", got)
	}
	next := s.Next(at)
	wantNext := time.Date(2026, 10, 15, 12, 38, 12, 0, time.UTC)
	if !next.Equal(wantNext) {
		t.Fatalf("Next = %v, want %v", next, wantNext)
	}
	if got := s.FormatNext(at); got != "2026-10-15T12:38:12Z" {
		t.Fatalf("FormatNext = %q", got)
	}
}

func TestShanghaiOffsetOnUTCClock(t *testing.T) {
	day := 1
	s := FromFields(&day, "00:00:00", "Asia/Shanghai")
	// 2026-08-01 00:00 Shanghai is 2026-07-31 16:00 UTC.
	before := time.Date(2026, 7, 31, 15, 59, 0, 0, time.UTC)
	if got := s.CycleKey(before); got != "2026-07-01" {
		t.Fatalf("before Shanghai midnight CycleKey = %q, want 2026-07-01", got)
	}
	after := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	if got := s.CycleKey(after); got != "2026-08-01" {
		t.Fatalf("at Shanghai midnight CycleKey = %q, want 2026-08-01", got)
	}
	if got := s.FormatNext(after); got != "2026-09-01T00:00:00+08:00" {
		t.Fatalf("FormatNext = %q", got)
	}
}

func TestNextFromJanuary31ClampsFebruary(t *testing.T) {
	day := 31
	s := FromFields(&day, "", "")
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, Location(DefaultTimezone))
	got := s.Next(start)
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, Location(DefaultTimezone))
	if !got.Equal(want) {
		t.Fatalf("Next(Jan 31) = %v, want %v", got, want)
	}
}

func TestNormalizeClockAndTimezone(t *testing.T) {
	clock, err := NormalizeClock("7:8")
	if err != nil || clock != "07:08:00" {
		t.Fatalf("NormalizeClock = %q %v", clock, err)
	}
	tz, err := NormalizeTimezone("UTC")
	if err != nil || tz != "UTC" {
		t.Fatalf("NormalizeTimezone = %q %v", tz, err)
	}
	if _, err := NormalizeTimezone("Not/AZone"); err == nil {
		t.Fatal("expected unknown timezone error")
	}
}
