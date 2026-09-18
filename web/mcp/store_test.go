package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCleanupHistoryKeepsThreeDays(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:mcp-history-cleanup?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.MCPLease{}, &models.MCPOperation{}, &models.MCPToken{}, &models.MCPAuthorizationRequest{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	old := now.Add(-4 * 24 * time.Hour)
	recent := now.Add(-2 * 24 * time.Hour)

	leases := []models.MCPLease{
		{
			ID:            "ls_old_expired",
			OwnerUserUUID: "user",
			OAuthClientID: "cli",
			TokenFamilyID: "fam_old",
			TargetUUIDs:   `["node"]`,
			Status:        statusExpired,
			CreatedAt:     old,
			ExpiresAt:     old.Add(time.Hour),
		},
		{
			ID:            "ls_recent",
			OwnerUserUUID: "user",
			OAuthClientID: "cli",
			TokenFamilyID: "fam_recent",
			TargetUUIDs:   `["node"]`,
			Status:        statusExpired,
			CreatedAt:     recent,
			ExpiresAt:     recent.Add(time.Hour),
		},
		{
			ID:            "ls_old_live",
			OwnerUserUUID: "user",
			OAuthClientID: "cli",
			TokenFamilyID: "fam_live",
			TargetUUIDs:   `["node"]`,
			Status:        statusActive,
			CreatedAt:     old,
			ExpiresAt:     now.Add(time.Hour),
		},
	}
	for i := range leases {
		if err := db.Create(&leases[i]).Error; err != nil {
			t.Fatalf("create lease %s: %v", leases[i].ID, err)
		}
	}
	ops := []models.MCPOperation{
		{ID: "op_old", LeaseID: "ls_old_expired", AgentUUID: "node", ToolName: "exec", State: opSucceeded, CreatedAt: old, Deadline: old},
		{ID: "op_recent", LeaseID: "ls_recent", AgentUUID: "node", ToolName: "exec", State: opSucceeded, CreatedAt: recent, Deadline: recent},
	}
	for i := range ops {
		if err := db.Create(&ops[i]).Error; err != nil {
			t.Fatalf("create operation %s: %v", ops[i].ID, err)
		}
	}

	if err := cleanupHistory(db, now); err != nil {
		t.Fatalf("cleanupHistory: %v", err)
	}

	var leaseCount int64
	if err := db.Model(&models.MCPLease{}).Count(&leaseCount).Error; err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if leaseCount != 2 {
		t.Fatalf("leases after cleanup = %d, want 2 (recent + still-live)", leaseCount)
	}
	var opCount int64
	if err := db.Model(&models.MCPOperation{}).Count(&opCount).Error; err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if opCount != 1 {
		t.Fatalf("operations after cleanup = %d, want 1", opCount)
	}
	var kept models.MCPOperation
	if err := db.Where("id = ?", "op_recent").First(&kept).Error; err != nil {
		t.Fatalf("recent operation missing: %v", err)
	}
}

func TestCompactStaleOperationOutputsKeepsLiveReads(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:mcp-output-compact?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.MCPLease{}, &models.MCPOperation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	freshFinish := now.Add(-time.Minute)
	staleFinish := now.Add(-operationOutputRetention - time.Minute)
	large := strings.Repeat("a", AdminOutputPreviewMax+200)
	leases := []models.MCPLease{
		{ID: "ls_live", OwnerUserUUID: "user", OAuthClientID: "cli", TokenFamilyID: "fam_live", TargetUUIDs: `["node"]`, Status: statusActive, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)},
		{ID: "ls_expired", OwnerUserUUID: "user", OAuthClientID: "cli", TokenFamilyID: "fam_old", TargetUUIDs: `["node"]`, Status: statusExpired, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)},
	}
	for i := range leases {
		if err := db.Create(&leases[i]).Error; err != nil {
			t.Fatalf("create lease %s: %v", leases[i].ID, err)
		}
	}
	ops := []models.MCPOperation{
		{ID: "op_fresh", LeaseID: "ls_live", AgentUUID: "node", ToolName: "exec", State: opSucceeded, Output: large, CreatedAt: freshFinish, FinishedAt: &freshFinish, Deadline: freshFinish},
		{ID: "op_stale", LeaseID: "ls_live", AgentUUID: "node", ToolName: "exec", State: opSucceeded, Output: large, CreatedAt: staleFinish, FinishedAt: &staleFinish, Deadline: staleFinish},
		{ID: "op_expired", LeaseID: "ls_expired", AgentUUID: "node", ToolName: "exec", State: opSucceeded, Output: large, CreatedAt: freshFinish, FinishedAt: &freshFinish, Deadline: freshFinish},
	}
	for i := range ops {
		if err := db.Create(&ops[i]).Error; err != nil {
			t.Fatalf("create operation %s: %v", ops[i].ID, err)
		}
	}
	if err := compactStaleOperationOutputs(db, now); err != nil {
		t.Fatalf("compactStaleOperationOutputs: %v", err)
	}

	var fresh, stale, expired models.MCPOperation
	if err := db.Where("id = ?", "op_fresh").First(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Output != large {
		t.Fatalf("fresh live output length = %d, want %d", len(fresh.Output), len(large))
	}
	if err := db.Where("id = ?", "op_stale").First(&stale).Error; err != nil {
		t.Fatal(err)
	}
	if len([]rune(stale.Output)) != AdminOutputPreviewMax {
		t.Fatalf("stale output runes = %d, want %d", len([]rune(stale.Output)), AdminOutputPreviewMax)
	}
	if !stale.Truncated {
		t.Fatal("stale output should be marked truncated")
	}
	if err := db.Where("id = ?", "op_expired").First(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if len([]rune(expired.Output)) != AdminOutputPreviewMax {
		t.Fatalf("expired-lease output runes = %d, want %d", len([]rune(expired.Output)), AdminOutputPreviewMax)
	}
}

func TestRecordDeniedAuthorizationAppearsInHistory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:mcp-deny-history?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.MCPLease{}, &models.MCPOperation{}, &models.MCPAuthorizationRequest{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	req := models.MCPAuthorizationRequest{
		ID:                  "ar_deny",
		ClientID:            "cli_deny",
		RedirectURI:         "http://127.0.0.1:9/callback",
		State:               "st",
		CodeChallenge:       "challenge",
		CodeChallengeMethod: "S256",
		Status:              statusPending,
		CreatedAt:           now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("create request: %v", err)
	}
	lease, err := recordDeniedAuthorization(db, req, "user-1", "sess", now)
	if err != nil {
		t.Fatalf("recordDeniedAuthorization: %v", err)
	}
	if lease.Status != statusDenied {
		t.Fatalf("lease status = %q, want denied", lease.Status)
	}
	var stored models.MCPAuthorizationRequest
	if err := db.Where("id = ?", req.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload request: %v", err)
	}
	if stored.Status != statusDenied {
		t.Fatalf("request status = %q, want denied", stored.Status)
	}
	var opCount int64
	if err := db.Model(&models.MCPOperation{}).Where("lease_id = ?", lease.ID).Count(&opCount).Error; err != nil {
		t.Fatal(err)
	}
	if opCount != 0 {
		t.Fatalf("denied authorization wrote %d operations, want 0", opCount)
	}
}

func TestPurgeInactiveHistoryKeepsActiveLeases(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:mcp-history-purge?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.MCPLease{}, &models.MCPOperation{}, &models.MCPToken{}, &models.MCPAuthorizationRequest{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	leases := []models.MCPLease{
		{ID: "ls_denied", OwnerUserUUID: "user", OAuthClientID: "cli", TokenFamilyID: "fam_d", TargetUUIDs: "[]", Status: statusDenied, CreatedAt: now, ExpiresAt: now},
		{ID: "ls_expired", OwnerUserUUID: "user", OAuthClientID: "cli", TokenFamilyID: "fam_e", TargetUUIDs: "[]", Status: statusExpired, CreatedAt: now, ExpiresAt: now.Add(-time.Hour)},
		{ID: "ls_revoked", OwnerUserUUID: "user", OAuthClientID: "cli", TokenFamilyID: "fam_r", TargetUUIDs: "[]", Status: statusRevoked, CreatedAt: now, ExpiresAt: now},
		{ID: "ls_active", OwnerUserUUID: "user", OAuthClientID: "cli", TokenFamilyID: "fam_a", TargetUUIDs: `["node"]`, Status: statusActive, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	for i := range leases {
		if err := db.Create(&leases[i]).Error; err != nil {
			t.Fatalf("create lease %s: %v", leases[i].ID, err)
		}
	}
	if err := db.Create(&models.MCPOperation{ID: "op_denied", LeaseID: "ls_denied", AgentUUID: "", ToolName: "grant", State: statusDenied, CreatedAt: now, Deadline: now}).Error; err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if err := db.Create(&models.MCPOperation{ID: "op_active", LeaseID: "ls_active", AgentUUID: "node", ToolName: "exec", State: opSucceeded, CreatedAt: now, Deadline: now}).Error; err != nil {
		t.Fatalf("create live operation: %v", err)
	}
	deleted, err := purgeInactiveHistory(db, now)
	if err != nil {
		t.Fatalf("purgeInactiveHistory: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted = %d, want 3", deleted)
	}
	var leaseCount, opCount int64
	if err := db.Model(&models.MCPLease{}).Count(&leaseCount).Error; err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if leaseCount != 1 {
		t.Fatalf("leases after purge = %d, want 1", leaseCount)
	}
	if err := db.Model(&models.MCPOperation{}).Count(&opCount).Error; err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if opCount != 1 {
		t.Fatalf("operations after purge = %d, want 1", opCount)
	}
}

func TestAgentSupportsMCPRequiresFullCapability(t *testing.T) {
	if agentSupportsMCP(models.Client{RemoteControlEnabled: true}) {
		t.Fatal("old remote-control agents must not count as full MCP")
	}
	if agentSupportsMCP(models.Client{MCPFull: true, MCPFullVersion: 1}) {
		t.Fatal("mcp_full without remote control must not count")
	}
	if !agentSupportsMCP(models.Client{RemoteControlEnabled: true, MCPFull: true, MCPFullVersion: 1}) {
		t.Fatal("current full MCP agents should be accepted")
	}
}
