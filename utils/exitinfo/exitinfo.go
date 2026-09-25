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

// MaxNameLength bounds a generated node name. It is deliberately below the
// clients.name column width (varchar(100)) and the panel's display width: a
// full IPv6 address plus country, ASN and ISP can reach ~80 characters on its
// own, which overflows the column and breaks the node list layout.
const MaxNameLength = 60

// Describe renders the standard node name: country code, IP, ASN and ISP.
// Missing pieces are omitted so a partial answer still produces a stable name.
// IPv6 addresses are abridged and long operator names dropped as needed so the
// result always fits MaxNameLength.
func Describe(ip string, info Info) string {
	address := firstNonEmpty(info.IP, ip)
	code := normalizeCountryCode(info.CountryCode)
	asn := asnToken(info.ASN)
	isp := sanitizeSegment(info.ISP)

	// Progressive fallbacks, longest first: the first variant that fits wins, so
	// an IPv4 node keeps every part while a long IPv6 name degrades gracefully.
	candidates := [][]string{
		{code, address, asn, isp},
		{code, address, asn},
		{code, shortenAddress(address), asn, isp},
		{code, shortenAddress(address), asn},
		{shortenAddress(address)},
	}
	for _, parts := range candidates {
		if name := joinParts(parts); name != "" && len(name) <= MaxNameLength {
			return name
		}
	}
	// Pathological input: hard-truncate the shortest meaningful form.
	name := joinParts([]string{code, shortenAddress(address)})
	if name == "" {
		name = address
	}
	if len(name) > MaxNameLength {
		name = name[:MaxNameLength]
	}
	return name
}

func joinParts(parts []string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "-")
}

func asnToken(raw string) string {
	asn := normalizeASN(raw)
	if asn == "" {
		return ""
	}
	return "AS" + asn
}

// shortenAddress abridges an IPv6 address to "head…tail" so the node name stays
// readable. IPv4 addresses are returned unchanged.
func shortenAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == "" || !strings.Contains(address, ":") {
		return address
	}
	const keepHead, keepTail = 4, 2
	groups := strings.Split(address, ":")
	if len(groups) <= keepHead+keepTail {
		return address
	}
	return strings.Join(groups[:keepHead], ":") + "…" + strings.Join(groups[len(groups)-keepTail:], ":")
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
