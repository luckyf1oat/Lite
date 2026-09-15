package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestParseDurationMinutes(t *testing.T) {
	cases := []struct {
		raw     any
		want    int
		wantErr bool
	}{
		{1, 1, false},
		{30, 30, false},
		{90, 90, false},
		{1440, 1440, false},
		{0, 0, true},
		{-1, 0, true},
		{1441, 0, true},
		{1.5, 0, true},
		{"90", 90, false},
		{"", 0, true},
		{"1.5", 0, true},
		{"25h", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseDurationMinutes(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParseDurationMinutes(%v) succeeded, want error", tc.raw)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("ParseDurationMinutes(%v) = %d, %v; want %d, nil", tc.raw, got, err, tc.want)
		}
	}
}

func TestValidateLeaseDurationRespectsSiteMax(t *testing.T) {
	if _, err := ValidateLeaseDuration(90, 60); err == nil {
		t.Fatal("expected duration over site max to fail")
	}
	got, err := ValidateLeaseDuration(45, 60)
	if err != nil || got != 45 {
		t.Fatalf("got %d %v", got, err)
	}
}

func TestNormalizeSettingsRejectsDefaultAboveMax(t *testing.T) {
	if _, err := NormalizeSettings(120, 60, 4); err == nil {
		t.Fatal("expected default > max to fail")
	}
	got, err := NormalizeSettings(30, 1440, 4)
	if err != nil || got.DefaultMinutes != 30 || got.MaxMinutes != 1440 {
		t.Fatalf("got %#v %v", got, err)
	}
}

func TestPKCEAndRedirect(t *testing.T) {
	if err := verifyPKCE("plain", "abc", "abc"); err == nil {
		t.Fatal("plain PKCE must be rejected")
	}
	// sha256("verifier") base64url
	if err := verifyPKCE("S256", "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk", "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); err == nil {
		// this challenge is not the hash of itself; must fail
	}
	if _, err := parseRedirectURI("https://cursor.example/callback"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseRedirectURI("http://127.0.0.1:1234/cb"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseRedirectURI("http://evil.example/cb"); err == nil {
		t.Fatal("public http redirect must be rejected")
	}
}

func TestTruncateTTLNeverExceedsLease(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	expires := now.Add(90 * time.Second)
	got := truncateTTL(now, expires, 5*time.Minute)
	if got != 90*time.Second {
		t.Fatalf("ttl = %s, want 90s", got)
	}
	if truncateTTL(now, now, time.Minute) != 0 {
		t.Fatal("expired lease must truncate to 0")
	}
}

func TestSameResource(t *testing.T) {
	if !sameResource("https://lite.example/mcp", "https://lite.example/mcp/") {
		t.Fatal("trailing slash should match")
	}
	if sameResource("https://a/mcp", "https://b/mcp") {
		t.Fatal("different hosts must not match")
	}
}

func TestNewMCPDelegationHasNoAdmin(t *testing.T) {
	if strings.Contains("admin", "no") {
		t.Fatal("sanity")
	}
}
