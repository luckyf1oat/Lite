package agent

import (
	"testing"
	"time"

	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/web/connection"
)

func TestRecordReportKeepsLatestAndScalesRecentWindow(t *testing.T) {
	// The recent raw window follows the fleet's report interval, so the test
	// pins a short cadence to exercise the short-window path.
	previousInterval := reportIntervalSeconds.Load()
	reportIntervalSeconds.Store(45)
	t.Cleanup(func() { reportIntervalSeconds.Store(previousInterval) })

	mu.Lock()
	previousLatest := latestReport
	previousRecent := recentReports
	latestReport = make(map[string]*v2.Report)
	recentReports = make(map[string][]v2.Report)
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		latestReport = previousLatest
		recentReports = previousRecent
		mu.Unlock()
	})

	now := time.Now().UTC()
	// 45 * 1.5 = 67.5s window: the 2 minute old sample falls outside it while
	// the 30s and 45s old samples are both retained.
	RecordReport(v2.Report{UUID: "node-a", UpdatedAt: now.Add(-2 * time.Minute), CPU: v2.CPUReport{Usage: 10}})
	RecordReport(v2.Report{UUID: "node-a", UpdatedAt: now.Add(-30 * time.Second), CPU: v2.CPUReport{Usage: 20}})
	RecordReport(v2.Report{UUID: "node-a", UpdatedAt: now.Add(-45 * time.Second), CPU: v2.CPUReport{Usage: 15}})

	recent := GetRecentReports("node-a")
	if len(recent) != 2 || recent[0].CPU.Usage != 15 || recent[1].CPU.Usage != 20 {
		t.Fatalf("recent reports = %#v", recent)
	}
	recent[0].CPU.Usage = 99
	if got := GetRecentReports("node-a"); len(got) != 2 || got[0].CPU.Usage != 15 {
		t.Fatalf("recent report cache was mutated through returned slice: %#v", got)
	}

	latest := GetLatestReport()
	if latest["node-a"] == nil || latest["node-a"].CPU.Usage != 20 {
		t.Fatalf("latest report = %#v", latest["node-a"])
	}
	latest["node-a"].CPU.Usage = 99
	if got := GetLatestReport()["node-a"]; got == nil || got.CPU.Usage != 20 {
		t.Fatalf("latest report cache was mutated through returned map: %#v", got)
	}

	DeleteLatestReport("node-a")
	if len(GetRecentReports("node-a")) != 0 || GetLatestReport()["node-a"] != nil {
		t.Fatal("deleting latest report did not clear runtime report state")
	}
}

func TestRecentReportWindowFollowsReportInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval int64
		want     time.Duration
	}{
		{"fast cadence floors at the minimum", 3, recentReportMinWindow},
		{"one minute", 60, 90 * time.Second},
		{"ten minutes", 600, 15 * time.Minute},
		{"very slow cadence caps at the maximum", 3600, recentReportRetention},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := reportIntervalSeconds.Load()
			reportIntervalSeconds.Store(test.interval)
			t.Cleanup(func() { reportIntervalSeconds.Store(previous) })
			if got := RecentReportWindow(); got != test.want {
				t.Fatalf("RecentReportWindow() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDeleteConnectedClientsClearsAllRuntimeState(t *testing.T) {
	mu.Lock()
	previousConnected := connectedClients
	previousPresence := presenceOnly
	previousLatest := latestReport
	previousRecent := recentReports
	connectedClients = make(map[string]*connection.SafeConn)
	presenceOnly = make(map[string]struct {
		id     int64
		expire time.Time
	})
	latestReport = make(map[string]*v2.Report)
	recentReports = make(map[string][]v2.Report)
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		connectedClients = previousConnected
		presenceOnly = previousPresence
		latestReport = previousLatest
		recentReports = previousRecent
		mu.Unlock()
	})
	v2EventMu.Lock()
	previousQueues := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = previousQueues
		v2EventMu.Unlock()
	})

	KeepAlivePresence("node-a", 42, time.Minute)
	RecordReport(v2.Report{UUID: "node-a", UpdatedAt: time.Now().UTC()})
	EnqueueV2Event("node-a", v2.MethodAgentExec, v2.ExecParams{TaskID: "task"})
	DeleteConnectedClients("node-a")

	if IsAgentOnline("node-a") || GetLatestReport()["node-a"] != nil || len(GetRecentReports("node-a")) != 0 {
		t.Fatal("deleted client still has online or report state")
	}
	if events := TakeV2Events("node-a", nil, 16); len(events) != 0 {
		t.Fatalf("deleted client still has queued events: %#v", events)
	}
}

func TestIsPresentIncludesHTTPPresence(t *testing.T) {
	mu.Lock()
	previousConnected := connectedClients
	previousPresence := presenceOnly
	connectedClients = make(map[string]*connection.SafeConn)
	presenceOnly = make(map[string]struct {
		id     int64
		expire time.Time
	})
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		connectedClients = previousConnected
		presenceOnly = previousPresence
		mu.Unlock()
	})

	if IsPresent("node-a") {
		t.Fatal("offline node should not be present")
	}
	KeepAlivePresence("node-a", 7, time.Minute)
	if !IsPresent("node-a") {
		t.Fatal("HTTP presence should count as online for auto-renewal")
	}
	if !IsAgentOnline("node-a") {
		t.Fatal("HTTP presence should count as online")
	}
}

func TestIsAgentOnlineDoesNotStickAfterPresenceExpires(t *testing.T) {
	mu.Lock()
	previousConnected := connectedClients
	previousPresence := presenceOnly
	connectedClients = make(map[string]*connection.SafeConn)
	presenceOnly = make(map[string]struct {
		id     int64
		expire time.Time
	})
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		connectedClients = previousConnected
		presenceOnly = previousPresence
		mu.Unlock()
	})

	KeepAlivePresence("node-a", 7, 20*time.Millisecond)
	if !IsAgentOnline("node-a") {
		t.Fatal("live HTTP presence should count as online")
	}
	time.Sleep(40 * time.Millisecond)
	if IsAgentOnline("node-a") {
		t.Fatal("expired HTTP presence must not keep a node online")
	}
}

func TestGetConnectedClientLooksUpSingleConnection(t *testing.T) {
	mu.Lock()
	previousConnected := connectedClients
	connectedClients = make(map[string]*connection.SafeConn)
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		connectedClients = previousConnected
		mu.Unlock()
	})

	if GetConnectedClient("node-a") != nil {
		t.Fatal("missing client returned a connection")
	}
	conn := &connection.SafeConn{}
	mu.Lock()
	connectedClients["node-a"] = conn
	mu.Unlock()
	if got := GetConnectedClient("node-a"); got != conn {
		t.Fatalf("GetConnectedClient = %v, want the stored connection", got)
	}
	if GetConnectedClient("node-b") != nil {
		t.Fatal("unrelated client returned a connection")
	}
}
