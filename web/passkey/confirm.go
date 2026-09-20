package passkey

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

var (
	ErrNoPasskeys        = errors.New("no passkeys on this account")
	ErrConfirmNotFound   = errors.New("passkey confirmation expired")
	ErrAssertionMismatch = errors.New("passkey does not belong to this account")
)

const CeremonyConfirm CeremonyKind = "confirm"

type ConfirmGrant struct {
	ID       string
	UserUUID string
	Expires  time.Time
}

type confirmGrantStore struct {
	mu    sync.Mutex
	items map[string]ConfirmGrant
}

var confirmGrants = confirmGrantStore{items: map[string]ConfirmGrant{}}

func PutConfirmGrant(userUUID string) string {
	id := uuid.NewString()
	confirmGrants.mu.Lock()
	defer confirmGrants.mu.Unlock()
	now := time.Now()
	for key, item := range confirmGrants.items {
		if item.Expires.Before(now) {
			delete(confirmGrants.items, key)
		}
	}
	confirmGrants.items[id] = ConfirmGrant{
		ID:       id,
		UserUUID: userUUID,
		Expires:  now.Add(ceremonyTTL),
	}
	return id
}

func TakeConfirmGrant(id string) (ConfirmGrant, error) {
	confirmGrants.mu.Lock()
	defer confirmGrants.mu.Unlock()
	item, ok := confirmGrants.items[id]
	if !ok || item.Expires.Before(time.Now()) {
		delete(confirmGrants.items, id)
		return ConfirmGrant{}, ErrConfirmNotFound
	}
	delete(confirmGrants.items, id)
	return item, nil
}

func BeginUserAssertion(c *gin.Context, userUUID string) (string, protocol.PublicKeyCredentialRequestOptions, error) {
	wa, err := NewWebAuthn(c)
	if err != nil {
		return "", protocol.PublicKeyCredentialRequestOptions{}, err
	}
	user, err := LoadUser(userUUID)
	if err != nil {
		return "", protocol.PublicKeyCredentialRequestOptions{}, err
	}
	if len(user.WebAuthnCredentials()) == 0 {
		return "", protocol.PublicKeyCredentialRequestOptions{}, ErrNoPasskeys
	}
	assertion, session, err := wa.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", protocol.PublicKeyCredentialRequestOptions{}, err
	}
	return PutCeremony(CeremonyConfirm, userUUID, "", session), assertion.Response, nil
}

func FinishUserAssertion(c *gin.Context, userUUID, ceremonyID string, credential json.RawMessage) error {
	if userUUID == "" || ceremonyID == "" || len(credential) == 0 {
		return ErrConfirmNotFound
	}
	ceremony, err := TakeCeremony(ceremonyID, CeremonyConfirm)
	if err != nil {
		return ErrConfirmNotFound
	}
	if ceremony.UserUUID != userUUID {
		return ErrAssertionMismatch
	}
	wa, err := NewWebAuthn(c)
	if err != nil {
		return err
	}
	user, err := LoadUser(userUUID)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(credential))
	if err != nil {
		return err
	}
	cred, err := wa.ValidateLogin(user, ceremony.Session, parsed)
	if err != nil {
		return err
	}
	return UpdateCredential(userUUID, cred)
}

func resetConfirmGrantsForTest() {
	confirmGrants.mu.Lock()
	confirmGrants.items = map[string]ConfirmGrant{}
	confirmGrants.mu.Unlock()
}
