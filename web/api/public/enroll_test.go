package public

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/utils"
)

func TestEnrollmentKeyActiveCoversRevokedExpiredAndExhausted(t *testing.T) {
	now := time.Now().UTC()
	base := models.EnrollmentKey{MaxUses: 3, ExpiresAt: now.Add(time.Hour)}
	if !base.Active(now) {
		t.Fatal("a fresh key with remaining uses must be active")
	}

	expired := base
	expired.ExpiresAt = now.Add(-time.Minute)
	if expired.Active(now) {
		t.Fatal("an expired key must not be active")
	}

	revokedAt := now.Add(-time.Minute)
	revoked := base
	revoked.RevokedAt = &revokedAt
	if revoked.Active(now) {
		t.Fatal("a revoked key must not be active")
	}

	exhausted := base
	exhausted.UsedCount = exhausted.MaxUses
	if exhausted.Active(now) {
		t.Fatal("a key at its usage limit must not be active")
	}

	unlimited := models.EnrollmentKey{MaxUses: 0, ExpiresAt: now.Add(time.Hour), UsedCount: 10000}
	if !unlimited.Active(now) {
		t.Fatal("MaxUses == 0 means unlimited and must stay active")
	}
}

func TestEnrollCIDRAllows(t *testing.T) {
	tests := []struct {
		name      string
		allowlist string
		ip        string
		want      bool
	}{
		{"empty allowlist allows anything", "", "203.0.113.9", true},
		{"cidr match", "10.0.0.0/8,203.0.113.0/24", "203.0.113.9", true},
		{"cidr miss", "10.0.0.0/8", "203.0.113.9", false},
		{"single ip match", "198.51.100.7", "198.51.100.7", true},
		{"single ip miss", "198.51.100.7", "198.51.100.8", false},
		{"ipv6 match", "2001:db8::/32", "2001:db8::1", true},
		{"invalid source ip is rejected", "10.0.0.0/8", "not-an-ip", false},
		{"whitespace tolerated", " 10.0.0.0/8 , 203.0.113.0/24 ", "203.0.113.1", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := enrollCIDRAllows(test.allowlist, test.ip); got != test.want {
				t.Fatalf("enrollCIDRAllows(%q, %q) = %v, want %v", test.allowlist, test.ip, got, test.want)
			}
		})
	}
}

func TestAllowEnrollAttemptEnforcesHourlyLimit(t *testing.T) {
	// The limiter reads its ceiling from settings; with no store bound the
	// getter fails and the built-in default applies, which is far above 3.
	// Drive the limiter directly to keep the test independent of settings.
	resetEnrollRateLimiterForTest()
	const ip = "198.51.100.20"
	const key = "test-key-material"

	limit := enrollMaxPerHour()
	if limit < 3 {
		t.Skipf("default hourly limit (%d) too small for this test", limit)
	}
	for i := 0; i < limit; i++ {
		if !allowEnrollAttempt(ip, key) {
			t.Fatalf("attempt %d was rejected before reaching the limit %d", i+1, limit)
		}
	}
	if allowEnrollAttempt(ip, key) {
		t.Fatalf("attempt %d must be rejected once the hourly limit is reached", limit+1)
	}
	// A different source address keeps its own budget.
	if !allowEnrollAttempt("198.51.100.21", key) {
		t.Fatal("a different address must have an independent budget")
	}
}

func TestMarkEnrolledNodeReportedIsSafeWithoutDatabase(t *testing.T) {
	// Must not panic when the store is unavailable: this runs on the agent
	// report path, which a unit test never initializes.
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("MarkEnrolledNodeReported panicked: %v", recovered)
		}
	}()
	MarkEnrolledNodeReported("", time.Now().UTC())
}

func TestGenerateRandomStringProducesDistinctKeys(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		key := utils.GenerateRandomString(40)
		if len(key) != 40 {
			t.Fatalf("key length = %d, want 40", len(key))
		}
		if _, dup := seen[key]; dup {
			t.Fatal("generated duplicate enrollment key material")
		}
		seen[key] = struct{}{}
	}
}
