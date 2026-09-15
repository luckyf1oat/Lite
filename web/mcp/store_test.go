package mcp

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCleanupHistoryKeepsSevenDays(t *testing.T) {
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
	old := now.Add(-8 * 24 * time.Hour)
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
