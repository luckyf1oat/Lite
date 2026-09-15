package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/web/api/remote"
	"github.com/nuomiiiii/lite/web/remotectl"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	statusPending = "pending"
	statusActive  = "active"
	statusRevoked = "revoked"
	statusExpired = "expired"
	statusDenied  = "denied"

	kindAccess  = "access"
	kindRefresh = "refresh"
	kindCode    = "code"

	opAccepted        = "accepted"
	opRunning         = "running"
	opSucceeded       = "succeeded"
	opFailed          = "failed"
	opCancelRequested = "cancel_requested"
	opCancelled       = "cancelled"
	opExpired         = "expired"
	opUnknown         = "unknown"

	reasonRestart      = "server_restart"
	reasonRevoked      = "revoked"
	reasonUserSecurity = "user_security"

	adminHistoryRetention = 7 * 24 * time.Hour
	reasonLoginRevoked    = "login_session"
	reasonMCPOff          = "mcp_disabled"
	reasonRemoteOff       = "remote_disabled"
	reasonRefreshReuse    = "refresh_reuse"
)

var database = dbcore.GetDBInstance

func init() {
	accounts.AddUserSecurityListener(RevokeUser)
	remotectl.AddRevokeAllListener(func() {
		_ = revokeActiveLeases("", "", reasonRemoteOff)
	})
	remotectl.AddRevokeLoginListener(func(loginSession string) {
		_ = revokeActiveLeases("", accounts.SessionLookupKey(loginSession), reasonLoginRevoked)
	})
}

func InvalidateActiveLeases() error {
	return revokeActiveLeases("", "", reasonRestart)
}

func RevokeUser(userUUID string) {
	_ = revokeActiveLeases(userUUID, "", reasonUserSecurity)
}

func RevokeAllActive(reason string) error {
	if reason == "" {
		reason = reasonRevoked
	}
	return revokeActiveLeases("", "", reason)
}

func revokeActiveLeases(userUUID, loginHash, reason string) error {
	db := database()
	now := time.Now().UTC()
	query := db.Model(&models.MCPLease{}).Where("status = ? AND revoked_at IS NULL", statusActive)
	if userUUID != "" {
		query = query.Where("owner_user_uuid = ?", userUUID)
	}
	if loginHash != "" {
		query = query.Where("owner_login_session_hash = ?", loginHash)
	}
	var leases []models.MCPLease
	if err := query.Find(&leases).Error; err != nil {
		return err
	}
	if len(leases) == 0 {
		return nil
	}
	ids := make([]string, 0, len(leases))
	families := make([]string, 0, len(leases))
	for _, lease := range leases {
		ids = append(ids, lease.ID)
		families = append(families, lease.TokenFamilyID)
		remote.CloseMCPLeaseSessions(lease.ID)
		cancelLeaseOperations(lease.ID)
	}
	if err := db.Model(&models.MCPLease{}).Where("id IN ?", ids).Updates(map[string]any{
		"status":            statusRevoked,
		"revoked_at":        now,
		"revocation_reason": reason,
	}).Error; err != nil {
		return err
	}
	return db.Model(&models.MCPToken{}).Where("family_id IN ?", families).Updates(map[string]any{
		"used":       true,
		"expires_at": now,
	}).Error
}

func parseTargetUUIDs(raw string) []string {
	var ids []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &ids); err != nil {
		return compactStrings(strings.Split(raw, ","))
	}
	return compactStrings(ids)
}

func encodeTargetUUIDs(ids []string) string {
	return encodeJSONList(ids)
}

func leaseLive(lease models.MCPLease, now time.Time) bool {
	if lease.Status != statusActive || lease.RevokedAt != nil {
		return false
	}
	return lease.ExpiresAt.After(now)
}

func loadLiveLease(id string, now time.Time) (models.MCPLease, error) {
	var lease models.MCPLease
	if err := database().Where("id = ?", id).First(&lease).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.MCPLease{}, ErrLeaseInactive
		}
		return models.MCPLease{}, err
	}
	if !leaseLive(lease, now) {
		if lease.Status == statusActive && !lease.ExpiresAt.After(now) {
			_ = database().Model(&lease).Updates(map[string]any{
				"status":            statusExpired,
				"revocation_reason": statusExpired,
			}).Error
		}
		return models.MCPLease{}, ErrLeaseInactive
	}
	if !accounts.SessionStillValid(lease.OwnerUserUUID, lease.OwnerLoginSessionHash) {
		_ = revokeActiveLeases(lease.OwnerUserUUID, lease.OwnerLoginSessionHash, reasonLoginRevoked)
		return models.MCPLease{}, ErrLeaseInactive
	}
	return lease, nil
}

func leaseContainsNode(lease models.MCPLease, uuid string) bool {
	uuid = strings.TrimSpace(uuid)
	for _, item := range parseTargetUUIDs(lease.TargetUUIDs) {
		if item == uuid {
			return true
		}
	}
	return false
}

func agentSupportsMCP(client models.Client) bool {
	return client.RemoteControlEnabled && client.MCPFull && client.MCPFullVersion >= v2.MCPFullVersion
}

func countActiveLeases(userUUID string) (global int, user int, err error) {
	db := database()
	now := time.Now().UTC()
	var globalCount int64
	if err = db.Model(&models.MCPLease{}).
		Where("status = ? AND expires_at > ? AND revoked_at IS NULL", statusActive, now).
		Count(&globalCount).Error; err != nil {
		return 0, 0, err
	}
	var userCount int64
	if err = db.Model(&models.MCPLease{}).
		Where("status = ? AND expires_at > ? AND revoked_at IS NULL AND owner_user_uuid = ?", statusActive, now, userUUID).
		Count(&userCount).Error; err != nil {
		return 0, 0, err
	}
	return int(globalCount), int(userCount), nil
}

func saveClient(client models.MCPClient) error {
	return database().Clauses(clause.OnConflict{
		UpdateAll: true,
	}).Create(&client).Error
}

func loadClient(id string) (models.MCPClient, error) {
	var client models.MCPClient
	err := database().Where("client_id = ?", id).First(&client).Error
	return client, err
}

func adminHistoryCutoff(now time.Time) time.Time {
	return now.Add(-adminHistoryRetention)
}

// CleanupHistory drops MCP authorizations, operations, tokens, and pending
// requests older than the 7-day admin history window.
func CleanupHistory() error {
	return cleanupHistory(database(), time.Now().UTC())
}

func cleanupHistory(db *gorm.DB, now time.Time) error {
	cutoff := adminHistoryCutoff(now)
	if err := db.Where("created_at < ?", cutoff).Delete(&models.MCPOperation{}).Error; err != nil {
		return err
	}
	if err := db.Where("created_at < ?", cutoff).Delete(&models.MCPAuthorizationRequest{}).Error; err != nil {
		return err
	}
	var leases []models.MCPLease
	if err := db.Where("created_at < ?", cutoff).Find(&leases).Error; err != nil {
		return err
	}
	ids := make([]string, 0, len(leases))
	families := make([]string, 0, len(leases))
	for _, lease := range leases {
		if leaseLive(lease, now) {
			continue
		}
		ids = append(ids, lease.ID)
		families = append(families, lease.TokenFamilyID)
		remote.CloseMCPLeaseSessions(lease.ID)
	}
	if len(ids) > 0 {
		if err := db.Where("lease_id IN ?", ids).Delete(&models.MCPToken{}).Error; err != nil {
			return err
		}
		if err := db.Where("family_id IN ?", families).Delete(&models.MCPToken{}).Error; err != nil {
			return err
		}
		if err := db.Where("id IN ?", ids).Delete(&models.MCPLease{}).Error; err != nil {
			return err
		}
	}
	if err := db.Where("created_at < ?", cutoff).Delete(&models.MCPToken{}).Error; err != nil {
		return err
	}
	return pruneUnusedMCPClients(db, now)
}

func pruneUnusedMCPClients(db *gorm.DB, now time.Time) error {
	if !db.Migrator().HasTable(&models.MCPClient{}) {
		return nil
	}
	stale := now.Add(-24 * time.Hour)
	return db.Exec(`
		DELETE FROM mcp_clients
		WHERE created_at < ?
		AND NOT EXISTS (SELECT 1 FROM mcp_leases WHERE mcp_leases.oauth_client_id = mcp_clients.client_id)
		AND NOT EXISTS (SELECT 1 FROM mcp_tokens WHERE mcp_tokens.client_id = mcp_clients.client_id)
	`, stale).Error
}

func unusedMCPClientCount() int64 {
	var count int64
	_ = database().Raw(`
		SELECT COUNT(*) FROM mcp_clients
		WHERE NOT EXISTS (SELECT 1 FROM mcp_leases WHERE mcp_leases.oauth_client_id = mcp_clients.client_id)
		AND NOT EXISTS (SELECT 1 FROM mcp_tokens WHERE mcp_tokens.client_id = mcp_clients.client_id)
	`).Scan(&count).Error
	return count
}
