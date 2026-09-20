package packaging

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("missing caller path")
	}
	return filepath.Join(filepath.Dir(file), "..")
}

func TestSetupZigStripsCGODebug(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), ".github", "actions", "setup-zig", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, needle := range []string{
		"CGO_CFLAGS=-Os -g0",
		"CGO_LDFLAGS=-s",
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("setup-zig must export %s so release binaries drop C debug", needle)
		}
	}
}

func TestFrontendArtifactDoesNotShipDuplicateRescueTheme(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), ".github", "actions", "build-frontend", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if strings.Contains(src, "install_theme web/public/rescueTheme") {
		t.Fatal("build-frontend must not copy Lite-Theme into rescueTheme")
	}
	if !strings.Contains(src, "test ! -e web/public/rescueTheme") {
		t.Fatal("build-frontend must assert rescueTheme is absent")
	}
}
