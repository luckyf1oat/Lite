package cloudflared_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nuomiiiii/lite/utils/cloudflared"
)

func TestDockerfileDoesNotBundleCloudflared(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("missing caller path")
	}
	dockerfile := filepath.Join(filepath.Dir(file), "..", "..", "Dockerfile")
	data, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	src := string(data)
	for _, needle := range []string{"cloudflared", "curl"} {
		if strings.Contains(src, needle) {
			t.Fatalf("Dockerfile must not mention %s", needle)
		}
	}
}

func TestMissingBinaryMessageDoesNotPromiseDockerBundle(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("missing caller path")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "cloudflared.go"))
	if err != nil {
		t.Fatalf("read cloudflared.go: %v", err)
	}
	if strings.Contains(string(src), "Docker image with built-in cloudflared") {
		t.Fatal("cloudflared error must not tell users Docker ships a built-in binary")
	}
}

func TestStatusDoesNotExposeToken(t *testing.T) {
	const token = "test-cloudflare-token"
	t.Setenv("LITE_CLOUDFLARED_TOKEN", token)

	status := cloudflared.Status()
	if !status.EnvTokenPresent {
		t.Fatalf("expected environment token to be detected")
	}

	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("failed to marshal status: %v", err)
	}

	if strings.Contains(string(payload), token) {
		t.Fatalf("expected status payload not to contain the raw token")
	}
}
