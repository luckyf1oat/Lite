package accounts

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
)

func TestSessionInactiveUsesLastActivityNotAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour
	idle := models.Session{
		LatestOnline: now.Add(-3 * 24 * time.Hour),
		CreatedAt:    now.Add(-3 * 24 * time.Hour),
		Expires:      now.Add(20 * 24 * time.Hour),
	}
	if !sessionInactive(idle, now, ttl) {
		t.Fatal("session idle for 3 days with a future expiry should be inactive")
	}
	recent := idle
	recent.LatestOnline = now.Add(-time.Hour)
	if sessionInactive(recent, now, ttl) {
		t.Fatal("recently active session should stay valid")
	}
	if !sessionInactive(idle, now, 0) {
		t.Fatal("zero TTL should fall back to the default 24h idle window")
	}

	shown := SessionForDisplay(idle, now, ttl)
	if !shown.LatestOnline.Equal(idle.LatestOnline) {
		t.Fatalf("display latest_online = %v, want %v", shown.LatestOnline, idle.LatestOnline)
	}
	wantExpiry := idle.LatestOnline.Add(ttl)
	if !shown.Expires.Equal(wantExpiry) {
		t.Fatalf("display expires = %v, want last activity + TTL %v", shown.Expires, wantExpiry)
	}

	missingOnline := models.Session{
		CreatedAt: now.Add(-3 * 24 * time.Hour),
		Expires:   now.Add(20 * 24 * time.Hour),
	}
	fallback := SessionForDisplay(missingOnline, now, ttl)
	if !fallback.LatestOnline.Equal(missingOnline.CreatedAt) {
		t.Fatalf("display latest_online fallback = %v, want created_at %v", fallback.LatestOnline, missingOnline.CreatedAt)
	}
	if !fallback.Expires.Equal(missingOnline.CreatedAt.Add(ttl)) {
		t.Fatalf("display expires fallback = %v, want created_at + TTL", fallback.Expires)
	}
}

func TestCapLegacySessionExpiresShortensFutureSessions(t *testing.T) {
	username := "ttl-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "correctpassword")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = DeleteAllSessions()
		_ = DeleteAccountByUsername(username)
	})
	plain, err := CreateSession(user.UUID, int(48*time.Hour/time.Second), "ua", "127.0.0.1", "password")
	require.NoError(t, err)

	var before models.Session
	require.NoError(t, dbcore.GetDBInstance().Where("uuid = ?", user.UUID).First(&before).Error)
	require.True(t, before.Expires.After(time.Now().UTC().Add(2*time.Hour)))

	require.NoError(t, CapLegacySessionExpires(time.Hour))

	var after models.Session
	require.NoError(t, dbcore.GetDBInstance().Where("uuid = ?", user.UUID).First(&after).Error)
	require.True(t, after.Expires.Before(time.Now().UTC().Add(time.Hour+time.Minute)))
	require.True(t, after.Expires.After(time.Now().UTC().Add(50*time.Minute)))
	_ = plain
}

func TestIdleSessionOlderThanTTLCannotSignInOrRefresh(t *testing.T) {
	username := "idle-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "correctpassword")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = DeleteAllSessions()
		_ = DeleteAccountByUsername(username)
	})
	plain, err := CreateSession(user.UUID, int(30*24*time.Hour/time.Second), "ua", "127.0.0.1", "password")
	require.NoError(t, err)

	idleSince := time.Now().UTC().Add(-3 * 24 * time.Hour)
	require.NoError(t, dbcore.GetDBInstance().Model(&models.Session{}).Where("uuid = ?", user.UUID).UpdateColumn("latest_online", idleSince).Error)
	require.NoError(t, dbcore.GetDBInstance().Model(&models.Session{}).Where("uuid = ?", user.UUID).UpdateColumn("created_at", idleSince).Error)

	if SessionStillValid(user.UUID, plain) {
		t.Fatal("idle session older than TTL was still valid")
	}
	if _, _, err := TouchSession(plain, "ua", "127.0.0.1"); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("touch idle session = %v, want ErrSessionExpired", err)
	}
	require.NoError(t, CapLegacySessionExpires(24*time.Hour))
	var after models.Session
	require.NoError(t, dbcore.GetDBInstance().Where("uuid = ?", user.UUID).First(&after).Error)
	require.True(t, !after.Expires.After(time.Now().UTC()), "idle session expiry %v should already have passed", after.Expires)
	if _, err := GetSession(plain); err == nil {
		t.Fatal("idle session older than TTL was accepted")
	}
}

func TestCapLegacySessionExpiresKeepsRecentlyActiveSession(t *testing.T) {
	username := "active-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "correctpassword")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = DeleteAllSessions()
		_ = DeleteAccountByUsername(username)
	})
	plain, err := CreateSession(user.UUID, int(48*time.Hour/time.Second), "ua", "127.0.0.1", "password")
	require.NoError(t, err)
	require.NoError(t, CapLegacySessionExpires(24*time.Hour))
	if _, err := GetSession(plain); err != nil {
		t.Fatalf("recent session rejected after cap: %v", err)
	}
}
