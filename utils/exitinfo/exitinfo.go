// Package exitinfo resolves a node's egress identity (country code, ASN and
// ISP) from an IP-echo service. It is used to auto-name new agents such as
// "CN-1.2.3.4-AS4837-China Unicom".
//
// The package is intentionally self-contained: it performs one HTTP GET per
// uncached IP, parses a small set of well-known fields, and preserves the raw
// country code so callers can choose their own formatting.
package exitinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// DefaultEndpoint is the IP-echo service used when none is configured. It
// answers with `?format=json` and reports location.country, network.asn and
// network.org.
const DefaultEndpoint = "https://ipecho.5671234.xyz/?format=json"

const (
	defaultTimeout = 5 * time.Second
	// maxBodyBytes bounds the response we are willing to decode.
	maxBodyBytes = 64 << 10
	// cacheTTL is how long one resolved IP stays cached. A node's egress
	// identity rarely changes, and a positive cache keeps a fleet appearing at
	// once from hammering the upstream service.
	cacheTTL = 6 * time.Hour
	// failureTTL is the negative cache window. It is long enough to protect the
	// upstream during an outage and short enough that a transient failure does
	// not permanently block naming.
	failureTTL      = 10 * time.Minute
	maxCacheEntries = 20000
)

// Info is the subset of an IP-echo response this package depends on.
type Info struct {
	// IP echoes the queried address as reported by the service.
	IP string
	// CountryCode is the upper-case ISO 3166-1 alpha-2 code (for example "CN").
	CountryCode string
	// CountryName is the human readable country name when the service supplies it.
	CountryName string
	// ASN is the bare autonomous system number without the "AS" prefix, or "".
	ASN string
	// ISP is the network operator / organisation name, or "".
	ISP string
}

// echoResponse mirrors the fields of the JSON echo payload. All fields are
// optional so a service that omits parts of the document still decodes.
type echoResponse struct {
	IP       string `json:"ip"`
	Location struct {
		Country     string `json:"country"`
		CountryName string `json:"countryName"`
	} `json:"location"`
	Network struct {
		ASN string `json:"asn"`
		Org string `json:"org"`
	} `json:"network"`
	// Flat fallbacks for simpler echo services.
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	ASN         string `json:"asn"`
	Org         string `json:"org"`
	ISP         string `json:"isp"`
}

type cacheEntry struct {
	info    Info
	ok      bool
	expires time.Time
}

// Resolver fetches and caches egress identity information.
type Resolver struct {
	mu       sync.Mutex
	cache    map[string]cacheEntry
	client   *http.Client
	endpoint string
	now      func() time.Time
}

// NewResolver builds a resolver with a bounded HTTP client.
func NewResolver(endpoint string, timeout time.Duration) *Resolver {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpoint
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Resolver{
		cache:    make(map[string]cacheEntry),
		endpoint: endpoint,
		now:      time.Now,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  false,
			},
		},
	}
}

// SetEndpoint swaps the upstream URL (used when settings change at runtime).
func (r *Resolver) SetEndpoint(endpoint string) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	r.mu.Lock()
	r.endpoint = endpoint
	r.mu.Unlock()
}

func (r *Resolver) currentEndpoint() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.endpoint
}

// Resolve returns the egress identity for ip, using the cache when possible.
// A lookup failure is reported as an error and cached briefly so a fleet of
// nodes cannot repeatedly hammer a failing upstream.
func (r *Resolver) Resolve(ctx context.Context, ip string) (Info, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return Info{}, fmt.Errorf("exitinfo: empty ip")
	}
	if _, err := netip.ParseAddr(ip); err != nil {
		return Info{}, fmt.Errorf("exitinfo: invalid ip %q: %w", ip, err)
	}

	if info, ok, hit := r.lookup(ip); hit {
		if !ok {
			return Info{}, fmt.Errorf("exitinfo: negative cache for %s", ip)
		}
		return info, nil
	}

	info, err := r.fetch(ctx, ip)
	r.store(ip, info, err == nil)
	if err != nil {
		return Info{}, err
	}
	return info, nil
}

// Cached returns the cached identity for ip without performing any network
// call. It returns nil when the address is unknown, expired or negatively
// cached. Callers use it to decide whether a lookup is worth queuing.
func (r *Resolver) Cached(ip string) *Info {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil
	}
	info, ok, hit := r.lookup(ip)
	if !hit || !ok {
		return nil
	}
	copied := info
	return &copied
}

func (r *Resolver) lookup(ip string) (Info, bool, bool) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, found := r.cache[ip]
	if !found {
		return Info{}, false, false
	}
	if !entry.expires.After(now) {
		delete(r.cache, ip)
		return Info{}, false, false
	}
	return entry.info, entry.ok, true
}

func (r *Resolver) store(ip string, info Info, ok bool) {
	ttl := failureTTL
	if ok {
		ttl = cacheTTL
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) >= maxCacheEntries {
		r.evictLocked(now)
	}
	r.cache[ip] = cacheEntry{info: info, ok: ok, expires: now.Add(ttl)}
}

func (r *Resolver) evictLocked(now time.Time) {
	for key, entry := range r.cache {
		if !entry.expires.After(now) {
			delete(r.cache, key)
		}
	}
	// Still full after dropping expired entries: clear the oldest half by
	// dropping the map wholesale. Naming is idempotent, so a cold cache only
	// costs one extra upstream request per node.
	if len(r.cache) >= maxCacheEntries {
		r.cache = make(map[string]cacheEntry, maxCacheEntries/2)
	}
}

// Flush drops every cached entry. Used by settings hot-reload and tests.
func (r *Resolver) Flush() {
	r.mu.Lock()
	r.cache = make(map[string]cacheEntry)
	r.mu.Unlock()
}

func (r *Resolver) fetch(ctx context.Context, ip string) (Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.currentEndpoint(), nil)
	if err != nil {
		return Info{}, fmt.Errorf("exitinfo: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Lite/exitinfo")

	resp, err := r.client.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("exitinfo: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("exitinfo: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return Info{}, fmt.Errorf("exitinfo: read body: %w", err)
	}

	var decoded echoResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Info{}, fmt.Errorf("exitinfo: decode response: %w", err)
	}

	info := Info{
		IP:          firstNonEmpty(decoded.IP, ip),
		CountryCode: normalizeCountryCode(firstNonEmpty(decoded.Location.Country, decoded.CountryCode, decoded.Country)),
		CountryName: strings.TrimSpace(decoded.Location.CountryName),
		ASN:         normalizeASN(firstNonEmpty(decoded.Network.ASN, decoded.ASN)),
		ISP:         strings.TrimSpace(firstNonEmpty(decoded.Network.Org, decoded.ISP, decoded.Org)),
	}
	if info.CountryCode == "" && info.ASN == "" && info.ISP == "" {
		return Info{}, fmt.Errorf("exitinfo: response carried no usable identity for %s", ip)
	}
	return info, nil
}

// Describe renders the standard node name: country code, IP, ASN and ISP.
// Missing pieces are omitted so a partial answer still produces a stable name.
func Describe(ip string, info Info) string {
	parts := make([]string, 0, 4)
	if code := normalizeCountryCode(info.CountryCode); code != "" {
		parts = append(parts, code)
	}
	if address := firstNonEmpty(info.IP, ip); address != "" {
		parts = append(parts, address)
	}
	if asn := normalizeASN(info.ASN); asn != "" {
		parts = append(parts, "AS"+asn)
	}
	if isp := sanitizeSegment(info.ISP); isp != "" {
		parts = append(parts, isp)
	}
	return strings.Join(parts, "-")
}

// sanitizeSegment trims a name fragment and collapses whitespace so the
// generated name stays a single readable token.
func sanitizeSegment(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

// normalizeASN strips a leading "AS" and any whitespace, keeping only digits.
func normalizeASN(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "AS")
	var digits strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' {
			digits.WriteRune(char)
			continue
		}
		if digits.Len() > 0 {
			break
		}
	}
	return digits.String()
}

// normalizeCountryCode keeps a leading two letter ISO code and upper-cases it.
func normalizeCountryCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) < 2 {
		return ""
	}
	candidate := value[:2]
	for _, char := range candidate {
		if char < 'A' || char > 'Z' {
			return ""
		}
	}
	return candidate
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
