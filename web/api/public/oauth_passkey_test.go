package public

import "testing"

func TestPasskeySSOConfirmError(t *testing.T) {
	if got := passkeySSOConfirmError("", "user-a", "github_1", "github_1"); got != "passkey_confirm_failed" {
		t.Fatalf("empty session = %s", got)
	}
	if got := passkeySSOConfirmError("user-a", "user-b", "github_1", "github_1"); got != "passkey_confirm_failed" {
		t.Fatalf("uuid mismatch = %s", got)
	}
	if got := passkeySSOConfirmError("user-a", "user-a", "", "github_1"); got != "passkey_confirm_mismatch" {
		t.Fatalf("unbound = %s", got)
	}
	if got := passkeySSOConfirmError("user-a", "user-a", "github_1", "github_2"); got != "passkey_confirm_mismatch" {
		t.Fatalf("provider id mismatch = %s", got)
	}
	if got := passkeySSOConfirmError("user-a", "user-a", "github_1", "github_1"); got != "" {
		t.Fatalf("match = %s", got)
	}
}
