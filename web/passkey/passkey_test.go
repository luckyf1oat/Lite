package passkey

import (
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestUserHandleRoundTrip(t *testing.T) {
	id := "11111111-2222-3333-4444-555555555555"
	handle := UserHandle(id)
	if got := UserUUIDFromHandle(handle); got != id {
		t.Fatalf("handle round-trip: %s", got)
	}
}

func TestEncodeCredentialID(t *testing.T) {
	if got := EncodeCredentialID([]byte{0xfb, 0xef}); got != "-+8" && got != "-_8" {
		if EncodeCredentialID([]byte{1, 2, 3}) == "" {
			t.Fatal("expected encoded credential id")
		}
	}
}

func TestPutCeremonyCapsStoreSize(t *testing.T) {
	ResetCeremoniesForTest()
	t.Cleanup(ResetCeremoniesForTest)
	session := &webauthn.SessionData{}
	for i := 0; i < ceremonyMaxItems+8; i++ {
		PutCeremony(CeremonyLogin, "", "", session)
	}
	if got := CeremonyCountForTest(); got != ceremonyMaxItems {
		t.Fatalf("ceremony count = %d, want %d", got, ceremonyMaxItems)
	}
}
