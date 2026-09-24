package agent

import (
	"sync/atomic"
	"time"

	"github.com/nuomiiiii/lite/pkg/config"
)

// recentReportMinWindow is the floor for the in-memory raw report window. It
// keeps the recent-status panel usable when agents report frequently.
const recentReportMinWindow = 10 * time.Second

// reportIntervalConfigKey mirrors the agent-facing report interval setting.
// web/clientname owns the constant; it is duplicated here so the agent runtime
// does not import a naming package for a scheduling concern.
const reportIntervalConfigKey = "client_default_interval_seconds"

const defaultReportIntervalSeconds = 3.0

// reportIntervalSeconds holds the fleet-wide default report interval in
// seconds. It drives the in-memory recent-report window: a window shorter than
// the reporting cadence would only ever hold a single sample, so it is sized to
// roughly one cadence instead of a fixed minute.
var reportIntervalSeconds atomic.Int64

func init() {
	reportIntervalSeconds.Store(int64(defaultReportIntervalSeconds))
}

// RefreshRecentReportWindow re-reads the configured report interval. It is
// called at startup and whenever the setting changes.
func RefreshRecentReportWindow() {
	seconds, err := config.GetAs[float64](reportIntervalConfigKey, defaultReportIntervalSeconds)
	if err != nil || seconds <= 0 {
		seconds = defaultReportIntervalSeconds
	}
	reportIntervalSeconds.Store(int64(seconds))
}

// RecentReportWindow returns how long raw reports are retained in memory. The
// window is never longer than one reporting cadence plus a small margin, so a
// WebSSH-only fleet reporting once per ten minutes keeps two samples per node
// instead of twenty.
func RecentReportWindow() time.Duration {
	seconds := reportIntervalSeconds.Load()
	if seconds <= 0 {
		seconds = int64(defaultReportIntervalSeconds)
	}
	window := time.Duration(seconds) * time.Second
	window += window / 2
	if window < recentReportMinWindow {
		return recentReportMinWindow
	}
	if window > recentReportRetention {
		return recentReportRetention
	}
	return window
}
