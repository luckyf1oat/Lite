package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nuomiiiii/lite/pkg/selfupdate"
)

// runVersionCmd executes the version subcommand with the given flags and
// returns its stdout. Flags are set directly because cobra would not parse argv
// when RunE is invoked straight from a test.
func runVersionCmd(t *testing.T, jsonOutput, capabilities bool) string {
	t.Helper()
	previousJSON := versionJSON
	previousCapabilities := versionCapabilities
	t.Cleanup(func() {
		versionJSON = previousJSON
		versionCapabilities = previousCapabilities
	})
	versionJSON = jsonOutput
	versionCapabilities = capabilities

	var out bytes.Buffer
	VersionCmd.SetOut(&out)
	VersionCmd.SetErr(&out)
	t.Cleanup(func() {
		VersionCmd.SetOut(nil)
		VersionCmd.SetErr(nil)
	})

	if err := VersionCmd.RunE(VersionCmd, nil); err != nil {
		t.Fatalf("version command failed: %v", err)
	}
	return out.String()
}

// TestVersionPlainOutputStaysUpdaterCompatible guards the contract main.go
// depends on: the plain and --json forms are parsed by the self updater, so the
// capability probe must never leak into them.
func TestVersionPlainOutputStaysUpdaterCompatible(t *testing.T) {
	plain := strings.TrimSpace(runVersionCmd(t, false, false))
	if strings.Contains(plain, "self_update") {
		t.Fatalf("plain output leaked capability data: %q", plain)
	}
	if strings.Count(plain, "\n") != 0 {
		t.Fatalf("plain output must be a single line, got %q", plain)
	}

	raw := runVersionCmd(t, true, false)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("json output is not valid JSON: %v (%q)", err, raw)
	}
	if _, exists := decoded["capability"]; exists {
		t.Fatalf("--json without --capabilities must not include a capability block: %q", raw)
	}
	if len(decoded) != 2 {
		t.Fatalf("--json fields = %v, want exactly version and hash", decoded)
	}
}

func TestVersionCapabilitiesReportForkGate(t *testing.T) {
	restore := selfupdate.SetSelfUpdateEnabledForTest(false)
	defer restore()

	plain := runVersionCmd(t, false, true)
	if !strings.Contains(plain, "self_update=false") {
		t.Fatalf("capabilities output = %q, want self_update=false", plain)
	}
	if !strings.Contains(plain, "reason=self_update_disabled_in_this_fork") {
		t.Fatalf("capabilities output = %q, want the fork opt-out reason", plain)
	}

	raw := runVersionCmd(t, true, true)
	var decoded struct {
		Version    string `json:"version"`
		Hash       string `json:"hash"`
		Capability struct {
			Deployment string `json:"deployment"`
			Supported  bool   `json:"supported"`
			Reason     string `json:"reason"`
		} `json:"capability"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("json output is not valid JSON: %v (%q)", err, raw)
	}
	if decoded.Capability.Supported {
		t.Fatalf("capability.Supported = true, want false: %q", raw)
	}
	if decoded.Capability.Reason != "self_update_disabled_in_this_fork" {
		t.Fatalf("capability.Reason = %q", decoded.Capability.Reason)
	}
	if decoded.Version == "" || decoded.Hash == "" {
		t.Fatalf("version/hash must still be present: %q", raw)
	}
}
