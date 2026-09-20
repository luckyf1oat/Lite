package api

import (
	"bytes"
	"testing"
)

func TestPasskeyAssertionFromBody(t *testing.T) {
	ceremonyID, credential := passkeyAssertionFromBody([]byte(`{"ceremony_id":"c1","credential":{"id":"cred"}}`))
	if ceremonyID != "c1" {
		t.Fatalf("ceremony id = %q", ceremonyID)
	}
	if !bytes.Contains(credential, []byte(`"id":"cred"`)) {
		t.Fatalf("credential = %s", credential)
	}

	ceremonyID, credential = passkeyAssertionFromBody([]byte(`{"otp":"123456"}`))
	if ceremonyID != "" || len(credential) != 0 {
		t.Fatalf("otp body should not look like a passkey: %q %s", ceremonyID, credential)
	}

	ceremonyID, credential = passkeyAssertionFromBody([]byte(`{"ceremony_id":"c1","credential":null}`))
	if ceremonyID != "c1" || credential != nil {
		t.Fatalf("null credential: %q %s", ceremonyID, credential)
	}
}
