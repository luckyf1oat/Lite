package selfupdate

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestSelfUpdateIsDisabledByDefault guards the fork's safety posture: the
// in-place updater must not run unless the environment explicitly opts in,
// because its release URL and the version shown in the UI belong to upstream by
// default and would silently replace this fork's binary.
func TestSelfUpdateIsDisabledByDefault(t *testing.T) {
	restore := SetSelfUpdateEnabledForTest(false)
	defer restore()

	if SelfUpdateEnabled() {
		t.Fatal("self update must be disabled by default")
	}
	capability := DetectCapability()
	if capability.Supported {
		t.Fatalf("capability.Supported = true, want false (reason %q)", capability.Reason)
	}
	if capability.Reason != "self_update_disabled_in_this_fork" {
		t.Fatalf("capability.Reason = %q, want the fork opt-out reason", capability.Reason)
	}
}

func TestSelfUpdateOptInRemovesForkGate(t *testing.T) {
	restore := SetSelfUpdateEnabledForTest(true)
	defer restore()

	if !SelfUpdateEnabled() {
		t.Fatal("SelfUpdateEnabled must follow the opt-in flag")
	}
	// The platform may still refuse for other reasons, but it must no longer be
	// blocked by the fork gate.
	if reason := DetectCapability().Reason; reason == "self_update_disabled_in_this_fork" {
		t.Fatalf("reason = %q, want the fork gate to be lifted", reason)
	}
}

func TestEnvEnabledAcceptsCommonAffirmatives(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", " yes ", "on", "enabled"} {
		if !envEnabled(value) {
			t.Fatalf("envEnabled(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off", "disabled", "maybe"} {
		if envEnabled(value) {
			t.Fatalf("envEnabled(%q) = true, want false", value)
		}
	}
}

func TestReleaseBaseURLDefaultsToFork(t *testing.T) {
	// The default is a package-level value, so verify it in a clean subprocess
	// where LITE_UPDATE_BASE_URL cannot leak in from the test environment.
	if os.Getenv("DSH_SELFUPDATE_URL_CHILD") == "1" {
		if releaseBaseURL != defaultReleaseBaseURL {
			t.Fatalf("releaseBaseURL = %q, want %q", releaseBaseURL, defaultReleaseBaseURL)
		}
		if !strings.Contains(defaultReleaseBaseURL, "luckyf1oat/Lite") {
			t.Fatalf("default release base URL %q must point at this fork", defaultReleaseBaseURL)
		}
		if strings.Contains(defaultReleaseBaseURL, "nuomiiiii/Lite") {
			t.Fatalf("default release base URL %q still points upstream", defaultReleaseBaseURL)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestReleaseBaseURLDefaultsToFork")
	cmd.Env = append(filteredEnv("LITE_UPDATE_BASE_URL"), "DSH_SELFUPDATE_URL_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\n%s", err, output)
	}
}

func TestReleaseURLUsesConfiguredBase(t *testing.T) {
	previous := releaseBaseURL
	t.Cleanup(func() { releaseBaseURL = previous })
	releaseBaseURL = "https://example.invalid/releases/download"

	got := releaseURL("1.2.3", "Lite-linux-amd64")
	want := "https://example.invalid/releases/download/1.2.3/Lite-linux-amd64"
	if got != want {
		t.Fatalf("releaseURL = %q, want %q", got, want)
	}
}

func filteredEnv(drop string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, drop+"=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}
