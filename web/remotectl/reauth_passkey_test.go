package remotectl

import (
	"errors"
	"testing"
)

func TestReauthorizePasskey(t *testing.T) {
	resetRateLimitsForTest()
	if err := ReauthorizePasskey("", "127.0.0.1", func() error { return nil }); !errors.Is(err, ErrGrantPrincipal) {
		t.Fatalf("empty user = %v", err)
	}
	if err := ReauthorizePasskey("user-a", "127.0.0.1", nil); !errors.Is(err, ErrPasskeyInvalid) {
		t.Fatalf("nil verify = %v", err)
	}
	if err := ReauthorizePasskey("user-a", "127.0.0.1", func() error { return errors.New("nope") }); !errors.Is(err, ErrPasskeyInvalid) {
		t.Fatalf("failed verify = %v", err)
	}
	if err := ReauthorizePasskey("user-a", "127.0.0.1", func() error { return nil }); err != nil {
		t.Fatalf("ok verify = %v", err)
	}
}
