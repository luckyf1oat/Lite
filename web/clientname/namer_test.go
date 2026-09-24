package clientname

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/utils/exitinfo"
)

const namingSampleResponse = `{
  "ip": "203.0.113.7",
  "location": {"country": "CN", "countryName": "China"},
  "network": {"asn": "AS4837", "org": "China Unicom"}
}`

// newTestNamer builds a Namer whose resolver points at a local echo stub. The
// worker pool mirrors production so the asynchronous path is exercised for real.
func newTestNamer(t *testing.T, handler http.HandlerFunc) (*Namer, *int64) {
	t.Helper()
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	namer := &Namer{
		index: make(map[string]*nodeState),
		jobs:  make(chan lookupJob, queueSize),
		stop:  make(chan struct{}),
		now:   time.Now,
	}
	namer.resolver = exitinfo.NewResolver(server.URL, 2*time.Second)
	namer.wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go namer.worker()
	}
	t.Cleanup(func() { _ = namer.Shutdown() })
	return namer, &hits
}

func TestProposeIgnoresAdministratorNames(t *testing.T) {
	namer, hits := newTestNamer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(namingSampleResponse))
	})

	proposal := namer.Propose("node-a", "Tokyo edge", "203.0.113.7")
	if proposal.NeedsLookup || proposal.Candidate != "" {
		t.Fatalf("proposal = %#v, want no action for a renamed node", proposal)
	}
	if *hits != 0 {
		t.Fatalf("upstream hits = %d, want 0", *hits)
	}
}

func TestProposeRequestsLookupForPlaceholder(t *testing.T) {
	namer, _ := newTestNamer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(namingSampleResponse))
	})

	proposal := namer.Propose("node-a", "client_1234abcd", "203.0.113.7")
	if !proposal.NeedsLookup {
		t.Fatalf("proposal = %#v, want NeedsLookup", proposal)
	}
	if proposal.Candidate != "" {
		t.Fatalf("candidate = %q, want empty before the lookup", proposal.Candidate)
	}
}

func TestProposeReturnsCandidateOnceCached(t *testing.T) {
	namer, _ := newTestNamer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(namingSampleResponse))
	})

	if _, err := namer.NameForIP(context.Background(), "203.0.113.7"); err != nil {
		t.Fatalf("warm the cache: %v", err)
	}

	proposal := namer.Propose("node-a", "client_1234abcd", "203.0.113.7")
	if proposal.NeedsLookup {
		t.Fatalf("proposal = %#v, want no further lookup", proposal)
	}
	if proposal.Candidate != "CN-203.0.113.7-AS4837-China Unicom" {
		t.Fatalf("candidate = %q", proposal.Candidate)
	}
}

func TestProposeHonoursAttemptLimit(t *testing.T) {
	namer, _ := newTestNamer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	clock := time.Now()
	namer.now = func() time.Time { return clock }

	for i := 1; i <= maxProbeAttempts; i++ {
		if proposal := namer.Propose("node-a", "client_x", "203.0.113.7"); !proposal.NeedsLookup {
			t.Fatalf("attempt %d: proposal = %#v, want NeedsLookup", i, proposal)
		}
		namer.Submit("node-a", "203.0.113.7")
		clock = clock.Add(retryCooldown + time.Minute)
	}
	if proposal := namer.Propose("node-a", "client_x", "203.0.113.7"); proposal.NeedsLookup {
		t.Fatalf("proposal = %#v, want the attempt limit to stop lookups", proposal)
	}
}

func TestProposeDefersRetryDuringCooldown(t *testing.T) {
	namer, _ := newTestNamer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	clock := time.Now()
	namer.now = func() time.Time { return clock }

	namer.Submit("node-b", "198.51.100.4")
	if proposal := namer.Propose("node-b", "client_y", "198.51.100.4"); proposal.NeedsLookup {
		t.Fatalf("proposal = %#v, want the cooldown to defer the retry", proposal)
	}

	clock = clock.Add(retryCooldown + time.Second)
	if proposal := namer.Propose("node-b", "client_y", "198.51.100.4"); !proposal.NeedsLookup {
		t.Fatalf("proposal = %#v, want a retry once the cooldown elapses", proposal)
	}
}

func TestSubmitIsNonBlockingWhenQueueIsFull(t *testing.T) {
	namer := &Namer{
		index: make(map[string]*nodeState),
		jobs:  make(chan lookupJob, 1),
		stop:  make(chan struct{}),
		now:   time.Now,
	}
	namer.resolver = exitinfo.NewResolver("http://127.0.0.1:1/", time.Second)
	namer.jobs <- lookupJob{uuid: "occupies", ip: "203.0.113.1"}

	done := make(chan struct{})
	go func() {
		namer.Submit("node-a", "203.0.113.7")
		namer.Submit("node-b", "203.0.113.8")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Submit blocked on a full queue")
	}
}

func TestIsPlaceholderName(t *testing.T) {
	tests := map[string]bool{
		"":                true,
		"client_":         true,
		"client_1234abcd": true,
		"CN-1.2.3.4":      false,
		"Tokyo edge":      false,
	}
	for name, want := range tests {
		if got := IsPlaceholderName(name); got != want {
			t.Fatalf("IsPlaceholderName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDefaultIntervalIsBounded(t *testing.T) {
	// Without a database the getter falls back to the built-in default, which
	// must be the documented 600 second cadence.
	if got := DefaultInterval(); got != DefaultIntervalSeconds {
		t.Fatalf("DefaultInterval = %v, want %v", got, DefaultIntervalSeconds)
	}
}
