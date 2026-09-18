package mcp

import (
	"errors"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCreateAuthorizationRequestReusesIdenticalPending(t *testing.T) {
	db := openMCPAuthTestDB(t)
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })

	now := time.Now().UTC()
	first, err := createAuthorizationRequest(sampleAuthRequest("cli-a", "https://app.example/cb", "state-1", "challenge-1", now))
	if err != nil {
		t.Fatal(err)
	}
	second, err := createAuthorizationRequest(sampleAuthRequest("cli-a", "https://app.example/cb", "state-1", "challenge-1", now.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("identical authorize created %s, want reuse of %s", second.ID, first.ID)
	}
	var count int64
	if err := db.Model(&models.MCPAuthorizationRequest{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("pending rows = %d, want 1", count)
	}
}

func TestCreateAuthorizationRequestKeepsDifferentPKCE(t *testing.T) {
	db := openMCPAuthTestDB(t)
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })

	now := time.Now().UTC()
	first, err := createAuthorizationRequest(sampleAuthRequest("cli-a", "https://app.example/cb", "state-1", "challenge-1", now))
	if err != nil {
		t.Fatal(err)
	}
	second, err := createAuthorizationRequest(sampleAuthRequest("cli-a", "https://app.example/cb", "state-1", "challenge-2", now))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("different PKCE must not reuse the pending request")
	}
}

func TestRegisterQueuesPendingAndAuthorizeReusesID(t *testing.T) {
	db := openMCPAuthTestDB(t)
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })

	now := time.Now().UTC()
	client := models.MCPClient{ClientID: "cli-reg", ClientName: "Codex"}
	if err := queueRegisteredClientAuthorization(client, "http://127.0.0.1:49981/cb", now); err != nil {
		t.Fatal(err)
	}
	pending, err := pendingAuthorizationRequests(now, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending after register = %d, want 1", len(pending))
	}
	if authorizationRequestReady(pending[0]) {
		t.Fatal("register placeholder must not be ready until authorize")
	}
	attached, err := createAuthorizationRequest(sampleAuthRequest("cli-reg", "http://127.0.0.1:49981/cb", "state-1", "challenge-1", now.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if attached.ID != pending[0].ID {
		t.Fatalf("authorize id = %s, want reuse of %s", attached.ID, pending[0].ID)
	}
	if !authorizationRequestReady(attached) {
		t.Fatal("authorize must attach PKCE onto the register placeholder")
	}
	var count int64
	if err := db.Model(&models.MCPAuthorizationRequest{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want 1 after attach", count)
	}
}

func TestDeniedClientBlocksLaterAuthorize(t *testing.T) {
	db := openMCPAuthTestDB(t)
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })

	now := time.Now().UTC()
	client := models.MCPClient{ClientID: "cli-denied", ClientName: "Codex"}
	if err := queueRegisteredClientAuthorization(client, "http://127.0.0.1:49981/cb", now); err != nil {
		t.Fatal(err)
	}
	pending, err := pendingAuthorizationRequests(now, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if err := db.Model(&pending[0]).Update("status", statusDenied).Error; err != nil {
		t.Fatal(err)
	}
	_, err = createAuthorizationRequest(sampleAuthRequest("cli-denied", "http://127.0.0.1:49981/cb", "state-1", "challenge-1", now.Add(time.Second)))
	if !errors.Is(err, errAuthorizationDenied) {
		t.Fatalf("authorize after deny = %v, want errAuthorizationDenied", err)
	}
	var count int64
	if err := db.Model(&models.MCPAuthorizationRequest{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want 1 denied placeholder and no new pending", count)
	}
	live, err := pendingAuthorizationRequests(now.Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("pending after deny = %d, want 0", len(live))
	}
}

func TestPendingAuthorizationRequestsSkipsExpiredInGo(t *testing.T) {
	db := openMCPAuthTestDB(t)
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })

	now := time.Now().UTC()
	live, err := createAuthorizationRequest(sampleAuthRequest("cli-live", "https://app.example/cb", "state-live", "challenge-live", now))
	if err != nil {
		t.Fatal(err)
	}
	expired := sampleAuthRequest("cli-expired", "https://app.example/cb", "state-x", "challenge-x", now.Add(-time.Hour))
	expired.ExpiresAt = now.Add(-time.Minute)
	if _, err := createAuthorizationRequest(expired); err != nil {
		t.Fatal(err)
	}
	got, err := pendingAuthorizationRequests(now, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != live.ID {
		t.Fatalf("got %+v, want only %s", got, live.ID)
	}
}

func TestUniquePendingAuthorizationRequestsDropsExactDuplicates(t *testing.T) {
	rows := []models.MCPAuthorizationRequest{
		{ID: "ar_new", ClientID: "cli", RedirectURI: "https://cb", State: "s", CodeChallenge: "c", CodeChallengeMethod: pkceS256, Resource: "r", Scope: scopeAgentFull},
		{ID: "ar_old", ClientID: "cli", RedirectURI: "https://cb", State: "s", CodeChallenge: "c", CodeChallengeMethod: pkceS256, Resource: "r", Scope: scopeAgentFull},
		{ID: "ar_other", ClientID: "cli", RedirectURI: "https://cb", State: "s", CodeChallenge: "other", CodeChallengeMethod: pkceS256, Resource: "r", Scope: scopeAgentFull},
	}
	got := uniquePendingAuthorizationRequests(rows)
	if len(got) != 2 || got[0].ID != "ar_new" || got[1].ID != "ar_other" {
		t.Fatalf("got %+v", got)
	}
}

func sampleAuthRequest(clientID, redirect, state, challenge string, now time.Time) models.MCPAuthorizationRequest {
	return models.MCPAuthorizationRequest{
		ClientID:            clientID,
		RedirectURI:         redirect,
		State:               state,
		CodeChallenge:       challenge,
		CodeChallengeMethod: pkceS256,
		Resource:            "http://127.0.0.1:27777/mcp",
		Scope:               scopeAgentFull,
		Status:              statusPending,
		CreatedAt:           now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
}

func openMCPAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:mcp-auth-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.MCPAuthorizationRequest{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}
