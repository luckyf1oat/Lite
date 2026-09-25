// Package clientname generates agent names from a node's egress identity.
//
// A newly registered agent is created with the placeholder name
// "client_xxxxxxxx". The first basic-info report makes the node's public IP
// known, at which point this package resolves the egress country, ASN and ISP
// and renames the node to "CN-1.2.3.4-AS4837-China Unicom".
//
// Two properties matter for a large fleet:
//
//   - Propose never blocks on the network. It reads local state and cache only,
//     so the agent report path is never delayed by an upstream outage.
//   - Lookups run on a small bounded worker pool. Ten thousand nodes appearing
//     at once produce a bounded, paced number of upstream requests instead of
//     ten thousand concurrent ones.
package clientname

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils/exitinfo"
	logger "github.com/nuomiiiii/lite/utils/log"
	"gorm.io/gorm"
)

const (
	// DefaultIntervalSeconds is the report interval applied to freshly created
	// deployment profiles. A WebSSH-only fleet wants the loosest cadence.
	DefaultIntervalSeconds = 600

	// EnabledKey turns automatic node naming on or off.
	EnabledKey = "client_naming_enabled"
	// EchoURLKey overrides the IP-echo endpoint used for egress lookups.
	EchoURLKey = "client_naming_echo_url"
	// TimeoutSecondsKey bounds a single egress lookup.
	TimeoutSecondsKey = "client_naming_timeout_seconds"
	// DefaultIntervalSecondsKey configures the report interval applied to new
	// nodes' deployment profiles.
	DefaultIntervalSecondsKey = "client_default_interval_seconds"

	defaultEnabled        = true
	defaultTimeoutSeconds = 5

	// workerCount bounds concurrent upstream lookups.
	workerCount = 4
	// queueSize bounds how many pending lookups may wait. A full queue drops the
	// request; the next basic-info report retries it.
	queueSize = 4096
	// maxProbeAttempts stops retrying an address whose lookup keeps failing.
	maxProbeAttempts = 3
	// retryCooldown spaces out failed attempts for the same node.
	retryCooldown = 30 * time.Minute
	// indexMaxEntries bounds the in-memory per-node bookkeeping.
	indexMaxEntries = 50000

	// PlaceholderPrefix marks the name assigned at registration time.
	PlaceholderPrefix = "client_"
)

// ErrDisabled is returned when automatic naming is switched off.
var ErrDisabled = errors.New("client naming is disabled")

// Proposal is the locally known naming state for one node.
type Proposal = clients.NameProposal

type nodeState struct {
	ip            string
	attempts      int
	lastAttemptAt time.Time
}

type lookupJob struct {
	uuid string
	ip   string
}

// settings is the cached view of this package's configuration. Reading the
// settings table on every report would be wasteful, and the values change only
// through an administrator action, so a short TTL is enough to pick changes up.
type settings struct {
	enabled      bool
	echoURL      string
	timeout      time.Duration
	defaultEvery float64
}

const settingsTTL = 30 * time.Second

// Namer resolves egress identities and renames nodes.
type Namer struct {
	resolver *exitinfo.Resolver

	mu    sync.Mutex
	index map[string]*nodeState
	jobs  chan lookupJob
	stop  chan struct{}

	settingsMu      sync.Mutex
	settingsAt      time.Time
	settingsValue   settings
	settingsForTest *settings

	wg       sync.WaitGroup
	stopOnce sync.Once
	now      func() time.Time
}

// New builds a Namer from the current settings and starts its workers.
func New() *Namer {
	namer := &Namer{
		index: make(map[string]*nodeState),
		jobs:  make(chan lookupJob, queueSize),
		stop:  make(chan struct{}),
		now:   time.Now,
	}
	current := namer.loadSettings()
	namer.resolver = exitinfo.NewResolver(current.echoURL, current.timeout)
	namer.wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go namer.worker()
	}
	return namer
}

// loadSettings reads the naming configuration, falling back to documented
// defaults whenever the settings store is unavailable (for example in a unit
// test that has no database).
func (n *Namer) loadSettings() settings {
	fallback := settings{
		enabled:      defaultEnabled,
		echoURL:      exitinfo.DefaultEndpoint,
		timeout:      time.Duration(defaultTimeoutSeconds) * time.Second,
		defaultEvery: DefaultIntervalSeconds,
	}
	if n == nil {
		return fallback
	}
	if n.settingsForTest != nil {
		return *n.settingsForTest
	}
	if !settingsStoreReady() {
		return fallback
	}

	enabled, err := config.GetAs[bool](EnabledKey, defaultEnabled)
	if err != nil {
		enabled = defaultEnabled
	}
	echoURL, err := config.GetAs[string](EchoURLKey, exitinfo.DefaultEndpoint)
	if err != nil || strings.TrimSpace(echoURL) == "" {
		echoURL = exitinfo.DefaultEndpoint
	}
	seconds, err := config.GetAs[int](TimeoutSecondsKey, defaultTimeoutSeconds)
	if err != nil || seconds <= 0 {
		seconds = defaultTimeoutSeconds
	}
	if seconds > 60 {
		seconds = 60
	}
	every, err := config.GetAs[int](DefaultIntervalSecondsKey, DefaultIntervalSeconds)
	if err != nil || every < 1 {
		every = DefaultIntervalSeconds
	}
	if every > 3600 {
		every = 3600
	}
	return settings{
		enabled:      enabled,
		echoURL:      strings.TrimSpace(echoURL),
		timeout:      time.Duration(seconds) * time.Second,
		defaultEvery: float64(every),
	}
}

// currentSettings returns the cached configuration, refreshing it after TTL.
func (n *Namer) currentSettings() settings {
	if n == nil {
		return settings{}
	}
	n.settingsMu.Lock()
	defer n.settingsMu.Unlock()
	if !n.settingsAt.IsZero() && n.now().Sub(n.settingsAt) < settingsTTL {
		return n.settingsValue
	}
	value := n.loadSettings()
	n.settingsValue = value
	n.settingsAt = n.now()
	return value
}

// settingsStoreReady reports whether the settings database has been bound. It
// exists because the config accessors assume an initialized database.
func settingsStoreReady() (ready bool) {
	defer func() {
		if recover() != nil {
			ready = false
		}
	}()
	_, err := config.GetAll()
	return err == nil
}

// Shutdown stops the workers and waits for in-flight lookups to finish.
func (n *Namer) Shutdown() error {
	if n == nil {
		return nil
	}
	n.stopOnce.Do(func() { close(n.stop) })
	done := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("client naming workers did not stop in time")
	}
}

// Reload applies a settings change (endpoint or timeout) and clears the cache.
func (n *Namer) Reload() {
	if n == nil || n.resolver == nil {
		return
	}
	n.settingsMu.Lock()
	n.settingsAt = time.Time{}
	n.settingsMu.Unlock()
	n.resolver.SetEndpoint(n.currentSettings().echoURL)
	n.resolver.Flush()
}

// Propose reports the naming state for a node using local state only.
//
// currentName is the node's stored name and ip is its known egress address.
// A node is only ever renamed while it still carries the registration
// placeholder, so an administrator rename always wins. Once a generated name
// has been written the placeholder is gone and no further probing happens;
// administrative re-naming goes through the explicit backfill call instead.
func (n *Namer) Propose(uuid, currentName, ip string) Proposal {
	ip = strings.TrimSpace(ip)
	if n == nil || uuid == "" || ip == "" || !IsPlaceholderName(currentName) {
		return Proposal{Candidate: ""}
	}

	n.mu.Lock()
	state := n.index[uuid]
	if state == nil {
		state = &nodeState{}
	}
	if state.ip != ip {
		state.ip = ip
		state.attempts = 0
		state.lastAttemptAt = time.Time{}
	}
	attempts := state.attempts
	lastAttempt := state.lastAttemptAt
	if len(n.index) < indexMaxEntries {
		n.index[uuid] = state
	}
	n.mu.Unlock()

	// The resolver cache is authoritative for "already known"; a cached answer
	// lets the caller persist the name without queueing any work.
	if cached := n.resolver.Cached(ip); cached != nil {
		if candidate := exitinfo.Describe(ip, *cached); candidate != "" {
			return Proposal{Candidate: candidate}
		}
	}
	if attempts >= maxProbeAttempts {
		return Proposal{Candidate: ""}
	}
	if !lastAttempt.IsZero() && n.now().Sub(lastAttempt) < retryCooldown {
		return Proposal{Candidate: ""}
	}
	return Proposal{NeedsLookup: true}
}

// Submit queues an asynchronous egress lookup for a node. It never blocks:
// when the queue is full the request is dropped and the next basic-info report
// retries it.
func (n *Namer) Submit(uuid, ip string) {
	if n == nil || uuid == "" || strings.TrimSpace(ip) == "" {
		return
	}
	n.mu.Lock()
	state := n.index[uuid]
	if state == nil {
		state = &nodeState{ip: ip}
	}
	state.attempts++
	state.lastAttemptAt = n.now()
	if len(n.index) < indexMaxEntries {
		n.index[uuid] = state
	}
	n.mu.Unlock()

	select {
	case n.jobs <- lookupJob{uuid: uuid, ip: ip}:
	case <-n.stop:
	default:
		// Queue full: drop. The next basic-info report retries.
	}
}

// Forget drops per-node bookkeeping, for example after a node is deleted.
func (n *Namer) Forget(uuid string) {
	if n == nil || uuid == "" {
		return
	}
	n.mu.Lock()
	delete(n.index, uuid)
	n.mu.Unlock()
}

func (n *Namer) worker() {
	defer n.wg.Done()
	for {
		select {
		case <-n.stop:
			return
		case job := <-n.jobs:
			n.resolveAndRename(job)
		}
	}
}

func (n *Namer) resolveAndRename(job lookupJob) {
	ctx, cancel := context.WithTimeout(context.Background(), n.currentSettings().timeout)
	defer cancel()

	info, err := n.resolver.Resolve(ctx, job.ip)
	if err != nil {
		logger.Debugf("clientname", "egress lookup failed for %s (%s): %v", job.uuid, job.ip, err)
		return
	}
	name := exitinfo.Describe(job.ip, info)
	if name == "" {
		return
	}
	// Defence in depth: Describe already bounds the length, but a future change
	// there must not be able to overflow clients.name (varchar(100)).
	if len(name) > exitinfo.MaxNameLength {
		name = name[:exitinfo.MaxNameLength]
	}
	if err := ApplyGeneratedName(job.uuid, name); err != nil {
		logger.Warnf("clientname", "failed to rename node %s: %v", job.uuid, err)
		return
	}
	logger.Infof("clientname", "named node %s as %q", job.uuid, name)
}

// ApplyGeneratedName writes a generated name, but only while the node still
// carries its registration placeholder. This keeps an administrator rename
// authoritative even when a lookup completes after the rename.
func ApplyGeneratedName(uuid, name string) error {
	return applyGeneratedName(dbcore.GetDBInstance(), uuid, name)
}

func applyGeneratedName(db *gorm.DB, uuid, name string) error {
	if db == nil || uuid == "" || strings.TrimSpace(name) == "" {
		return nil
	}
	var current models.Client
	if err := db.Select("uuid", "name").First(&current, "uuid = ?", uuid).Error; err != nil {
		return err
	}
	if !IsPlaceholderName(current.Name) {
		return nil
	}
	return db.Model(&models.Client{}).Where("uuid = ?", uuid).Updates(map[string]any{
		"name":                name,
		"name_auto_generated": true,
		"updated_at":          time.Now().UTC(),
	}).Error
}

// IsPlaceholderName reports whether name is still the registration-time
// placeholder ("client_" plus the first UUID segment) or empty.
func IsPlaceholderName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	return strings.HasPrefix(name, PlaceholderPrefix)
}

// Enabled reports whether automatic naming is switched on.
func Enabled() bool {
	enabled, err := config.GetAs[bool](EnabledKey, defaultEnabled)
	if err != nil {
		return defaultEnabled
	}
	return enabled
}

// DefaultInterval returns the configured default report interval in seconds
// for newly created deployment profiles.
func DefaultInterval() float64 {
	return (&Namer{}).loadSettings().defaultEvery
}

// Current exposes the process-wide namer, or nil when it is not initialized.
func Current() *Namer {
	return activeNamer
}

var activeNamer *Namer

// SetActive installs the process-wide namer used by administrative backfill.
// SetNodeNamer must be called with the same instance so the report path and the
// backfill path share one cache.
func SetActive(namer *Namer) {
	activeNamer = namer
}

// NameForIP resolves an address and renders a node name using the process-wide
// namer. It performs a network call.
func NameForIP(ctx context.Context, ip string) (string, error) {
	if activeNamer == nil {
		return "", fmt.Errorf("client naming is not initialized")
	}
	return activeNamer.NameForIP(ctx, ip)
}

// NameForIP resolves an address and renders the node name. It performs a
// network call and is meant for administrative backfill, not the report path.
func (n *Namer) NameForIP(ctx context.Context, ip string) (string, error) {
	if n == nil {
		return "", fmt.Errorf("client naming is not initialized")
	}
	info, err := n.resolver.Resolve(ctx, ip)
	if err != nil {
		return "", err
	}
	name := exitinfo.Describe(ip, info)
	if name == "" {
		return "", fmt.Errorf("egress lookup produced no name for %s", ip)
	}
	return name, nil
}

// ResetForTest clears bookkeeping and the resolver cache.
func (n *Namer) ResetForTest() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.index = make(map[string]*nodeState)
	n.mu.Unlock()
	n.resolver.Flush()
}
