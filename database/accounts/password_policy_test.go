package accounts

import "testing"

func TestValidateNewPassword(t *testing.T) {
	if err := ValidateNewPassword("short"); err != ErrPasswordTooShort {
		t.Fatalf("short password: %v", err)
	}
	if err := ValidateNewPassword("alllowercase1"); err != ErrPasswordTooWeak {
		t.Fatalf("weak password: %v", err)
	}
	if err := ValidateNewPassword("ALLUPPERCASE1"); err != ErrPasswordTooWeak {
		t.Fatalf("weak password: %v", err)
	}
	if err := ValidateNewPassword("NoDigitsHere"); err != ErrPasswordTooWeak {
		t.Fatalf("weak password: %v", err)
	}
	if err := ValidateNewPassword("ValidPass1"); err != nil {
		t.Fatalf("valid password: %v", err)
	}
}

func TestNormalizeSessionTTLSeconds(t *testing.T) {
	if got := NormalizeSessionTTLSeconds(30); got != DefaultSessionTTLSeconds {
		t.Fatalf("too small: %d", got)
	}
	if got := NormalizeSessionTTLSeconds(MaxSessionTTLSeconds + 1); got != DefaultSessionTTLSeconds {
		t.Fatalf("too large: %d", got)
	}
	if got := NormalizeSessionTTLSeconds(3600); got != 3600 {
		t.Fatalf("valid: %d", got)
	}
}
