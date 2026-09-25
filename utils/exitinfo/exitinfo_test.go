package exitinfo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sampleResponse mirrors the shape returned by the production echo service.
const sampleResponse = `{
  "version": "1.2.0",
  "ok": true,
  "ip": "203.0.113.7",
  "location": {"country": "CN", "countryName": "China", "city": "Qingdao"},
  "network": {"asn": "AS4837", "org": "China Unicom", "colo": "LAX"}
}`

func newTestResolver(t *testing.T, handler http.HandlerFunc) (*Resolver, *int64) {
	t.Helper()
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return NewResolver(server.URL, 2*time.Second), &hits
}

func TestResolveParsesEgressIdentity(t *testing.T) {
	resolver, hits := newTestResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleResponse))
	})

	info, err := resolver.Resolve(context.Background(), "203.0.113.7")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if info.CountryCode != "CN" {
		t.Fatalf("country code = %q, want CN", info.CountryCode)
	}
	if info.ASN != "4837" {
		t.Fatalf("asn = %q, want 4837", info.ASN)
	}
	if info.ISP != "China Unicom" {
		t.Fatalf("isp = %q, want China Unicom", info.ISP)
	}
	if got := Describe("203.0.113.7", info); got != "CN-203.0.113.7-AS4837-China Unicom" {
		t.Fatalf("describe = %q", got)
	}
	if *hits != 1 {
		t.Fatalf("upstream hits = %d, want 1", *hits)
	}
}

func TestResolveCachesSuccessAndFailure(t *testing.T) {
	resolver, hits := newTestResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleResponse))
	})

	for i := 0; i < 3; i++ {
		if _, err := resolver.Resolve(context.Background(), "203.0.113.7"); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if *hits != 1 {
		t.Fatalf("upstream hits = %d, want 1 (cached)", *hits)
	}
	if cached := resolver.Cached("203.0.113.7"); cached == nil || cached.ASN != "4837" {
		t.Fatalf("Cached returned %#v, want the resolved identity", cached)
	}

	failures, failHits := newTestResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	for i := 0; i < 3; i++ {
		if _, err := failures.Resolve(context.Background(), "198.51.100.9"); err == nil {
			t.Fatal("expected an error from a failing upstream")
		}
	}
	if *failHits != 1 {
		t.Fatalf("failing upstream hits = %d, want 1 (negative cache)", *failHits)
	}
	if cached := failures.Cached("198.51.100.9"); cached != nil {
		t.Fatalf("Cached returned %#v for a negative entry, want nil", cached)
	}
}

func TestResolveRejectsInvalidIPWithoutRequest(t *testing.T) {
	resolver, hits := newTestResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream must not be called for an invalid address")
	})
	if _, err := resolver.Resolve(context.Background(), "not-an-ip"); err == nil {
		t.Fatal("expected an error for an invalid address")
	}
	if *hits != 0 {
		t.Fatalf("upstream hits = %d, want 0", *hits)
	}
}

func TestResolveFallsBackToFlatFields(t *testing.T) {
	resolver, _ := newTestResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ip":"203.0.113.8","country_code":"us","isp":"Example ISP","asn":"7018"}`))
	})
	info, err := resolver.Resolve(context.Background(), "203.0.113.8")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := Describe("203.0.113.8", info); got != "US-203.0.113.8-AS7018-Example ISP" {
		t.Fatalf("describe = %q", got)
	}
}

func TestResolveErrorsWhenResponseHasNoIdentity(t *testing.T) {
	resolver, _ := newTestResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"ip":"203.0.113.9"}`))
	})
	if _, err := resolver.Resolve(context.Background(), "203.0.113.9"); err == nil {
		t.Fatal("expected an error when the response carries no usable identity")
	}
}

func TestDescribeOmitsMissingParts(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		info Info
		want string
	}{
		{"country only", "203.0.113.1", Info{CountryCode: "jp"}, "JP-203.0.113.1"},
		{"asn without isp", "203.0.113.2", Info{CountryCode: "de", ASN: "AS3320"}, "DE-203.0.113.2-AS3320"},
		{"isp whitespace collapsed", "203.0.113.3", Info{CountryCode: "us", ISP: "  Example   Host  "}, "US-203.0.113.3-Example Host"},
		{"empty", "203.0.113.4", Info{}, "203.0.113.4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Describe(test.ip, test.info); got != test.want {
				t.Fatalf("describe = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDescribeBoundsLength(t *testing.T) {
	// A full IPv6 address plus every other part must not overflow the node name.
	info := Info{CountryCode: "US", ASN: "4837", ISP: "China Unicom Backbone International"}
	name := Describe("2607:9d00:2000:0105::a088:9e36", info)
	if len(name) > MaxNameLength {
		t.Fatalf("name length = %d, want <= %d (%q)", len(name), MaxNameLength, name)
	}
	if !strings.Contains(name, "US") || !strings.Contains(name, "AS4837") {
		t.Fatalf("a degraded name must still identify the node: %q", name)
	}

	// IPv4 names keep every part because they fit comfortably.
	if v4 := Describe("203.0.113.7", Info{CountryCode: "CN", ASN: "AS4837", ISP: "China Unicom"}); v4 != "CN-203.0.113.7-AS4837-China Unicom" {
		t.Fatalf("ipv4 name = %q", v4)
	}

	// Pathological input still returns something within bounds.
	huge := Describe("2001:db8:1:2:3:4:5:6", Info{
		CountryCode: "DE",
		ASN:         "3320",
		ISP:         strings.Repeat("VeryLongOperatorName ", 8),
	})
	if len(huge) > MaxNameLength {
		t.Fatalf("pathological name length = %d, want <= %d", len(huge), MaxNameLength)
	}
}

func TestShortenAddress(t *testing.T) {
	tests := map[string]string{
		"203.0.113.7":                    "203.0.113.7",
		"2607:9d00:2000:0105::a088:9e36": "2607:9d00:2000:0105…a088:9e36",
		"2001:db8::1":                    "2001:db8::1",
		"":                               "",
	}
	for input, want := range tests {
		if got := shortenAddress(input); got != want {
			t.Fatalf("shortenAddress(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeHelpers(t *testing.T) {
	if got := normalizeASN(" as1234 "); got != "1234" {
		t.Fatalf("normalizeASN = %q", got)
	}
	if got := normalizeASN("4837"); got != "4837" {
		t.Fatalf("normalizeASN = %q", got)
	}
	if got := normalizeASN(""); got != "" {
		t.Fatalf("normalizeASN = %q", got)
	}
	if got := normalizeCountryCode(" cn "); got != "CN" {
		t.Fatalf("normalizeCountryCode = %q", got)
	}
	if got := normalizeCountryCode("1"); got != "" {
		t.Fatalf("normalizeCountryCode = %q", got)
	}
	if got := normalizeCountryCode("12"); got != "" {
		t.Fatalf("normalizeCountryCode = %q, want empty for digits", got)
	}
}
