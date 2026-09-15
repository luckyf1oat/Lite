package mcp

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

func TestTerminalWriteSameKeyRetriesAfterNotReady(t *testing.T) {
	db := openMCPTestDB(t)
	oldDB := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = oldDB })

	writes := 0
	oldLookup := mcpTerminalAgentUUID
	oldWrite := mcpWriteTerminal
	mcpTerminalAgentUUID = func(string, string) (string, error) { return "node", nil }
	mcpWriteTerminal = func(string, []byte) error {
		writes++
		if writes == 1 {
			return errors.New("terminal is not ready")
		}
		return nil
	}
	t.Cleanup(func() {
		mcpTerminalAgentUUID = oldLookup
		mcpWriteTerminal = oldWrite
	})

	lease := models.MCPLease{ID: "ls_write_retry", MaxConcurrency: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	args := map[string]any{
		"terminal_id":     "term-1",
		"data":            "aGVsbG8=",
		"encoding":        "base64",
		"idempotency_key": "write-1",
	}
	if _, err := terminalWriteTool(lease, args); err == nil || !strings.Contains(err.Error(), "terminal is not ready") {
		t.Fatalf("first write err=%v, want not ready", err)
	}
	stored := mustLoadOpByKey(t, db, lease.ID, "write-1")
	if stored.State != opFailed || !strings.Contains(stored.Output, "not ready") {
		t.Fatalf("stored first write: state=%s output=%q", stored.State, stored.Output)
	}

	result, err := terminalWriteTool(lease, args)
	if err != nil {
		t.Fatalf("retry write: %v", err)
	}
	payload, _ := result.(gin.H)
	if payload["accepted"] != true {
		t.Fatalf("retry payload=%#v", payload)
	}
	if writes != 2 {
		t.Fatalf("writes=%d, want 2", writes)
	}
	stored = mustLoadOpByKey(t, db, lease.ID, "write-1")
	if stored.State != opSucceeded {
		t.Fatalf("retry stored state=%s", stored.State)
	}
}

func TestTerminalWriteSuccessfulReplayDoesNotResend(t *testing.T) {
	db := openMCPTestDB(t)
	oldDB := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = oldDB })

	writes := 0
	oldLookup := mcpTerminalAgentUUID
	oldWrite := mcpWriteTerminal
	mcpTerminalAgentUUID = func(string, string) (string, error) { return "node", nil }
	mcpWriteTerminal = func(string, []byte) error {
		writes++
		return nil
	}
	t.Cleanup(func() {
		mcpTerminalAgentUUID = oldLookup
		mcpWriteTerminal = oldWrite
	})

	lease := models.MCPLease{ID: "ls_write_ok", MaxConcurrency: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	args := map[string]any{
		"terminal_id":     "term-2",
		"data":            "aGVsbG8=",
		"encoding":        "base64",
		"idempotency_key": "write-ok",
	}
	if _, err := terminalWriteTool(lease, args); err != nil {
		t.Fatal(err)
	}
	if _, err := terminalWriteTool(lease, args); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("successful replay resent write: writes=%d", writes)
	}
}

func TestTerminalOpenFailedReuseDoesNotReturnErrorAsTerminalID(t *testing.T) {
	db := openMCPTestDB(t)
	oldDB := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = oldDB })

	starts := 0
	oldLookup := lookupAuthorizedNode
	oldStart := mcpStartTerminal
	lookupAuthorizedNode = func(models.MCPLease, string) (models.Client, error) {
		return models.Client{UUID: "node"}, nil
	}
	mcpStartTerminal = func(string, string, string, string, time.Time, int, int) (string, error) {
		starts++
		if starts == 1 {
			return "", errors.New("Client is offline")
		}
		return "term-ok", nil
	}
	t.Cleanup(func() {
		lookupAuthorizedNode = oldLookup
		mcpStartTerminal = oldStart
	})

	lease := models.MCPLease{ID: "ls_open_retry", MaxConcurrency: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	args := map[string]any{"agent_uuid": "node", "idempotency_key": "open-1"}
	now := time.Now().UTC()
	if _, err := terminalOpenTool(lease, args, now); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("first open err=%v, want offline", err)
	}
	stored := mustLoadOpByKey(t, db, lease.ID, "open-1")
	if stored.State != opFailed {
		t.Fatalf("stored first open state=%s", stored.State)
	}

	result, err := terminalOpenTool(lease, args, now)
	if err != nil {
		t.Fatalf("retry open: %v", err)
	}
	payload, _ := result.(gin.H)
	if payload["terminal_id"] != "term-ok" {
		t.Fatalf("retry payload=%#v", payload)
	}
	if starts != 2 {
		t.Fatalf("starts=%d, want 2", starts)
	}
}

func TestConcurrentTerminalRetriesSendOnce(t *testing.T) {
	db := openMCPTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	oldDB, oldLookup, oldWrite := database, mcpTerminalAgentUUID, mcpWriteTerminal
	database = func() *gorm.DB { return db }
	mcpTerminalAgentUUID = func(string, string) (string, error) { return "node", nil }
	var attempts atomic.Int32
	mcpWriteTerminal = func(string, []byte) error {
		if attempts.Add(1) == 1 {
			return errors.New("terminal is not ready")
		}
		return nil
	}
	t.Cleanup(func() {
		database, mcpTerminalAgentUUID, mcpWriteTerminal = oldDB, oldLookup, oldWrite
	})
	lease := models.MCPLease{ID: "ls_write_race", MaxConcurrency: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	args := map[string]any{"terminal_id": "term", "data": "echo hello\n", "idempotency_key": "same-key"}
	if _, err := terminalWriteTool(lease, args); err == nil {
		t.Fatal("expected initial not-ready failure")
	}

	var arrivals atomic.Int32
	ready := make(chan struct{})
	if err := db.Callback().Update().Before("gorm:begin_transaction").Register("retry:claim_barrier", func(tx *gorm.DB) {
		values, ok := tx.Statement.Dest.(map[string]any)
		if !ok || values["state"] != opAccepted {
			return
		}
		if arrivals.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			tx.AddError(errors.New("retry claim barrier timed out"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove("retry:claim_barrier")
	})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, writeErr := terminalWriteTool(lease, args)
			results <- writeErr
		}()
	}
	for i := 0; i < 2; i++ {
		if writeErr := <-results; writeErr != nil {
			t.Fatalf("retry returned error: %v", writeErr)
		}
	}
	if got := attempts.Load() - 1; got != 1 {
		t.Fatalf("same key produced %d successful terminal sends, want 1", got)
	}
}

func TestTerminalOpenRetryMustRespectConcurrency(t *testing.T) {
	db := openMCPTestDB(t)
	oldDB, oldLookup, oldStart := database, lookupAuthorizedNode, mcpStartTerminal
	database = func() *gorm.DB { return db }
	lookupAuthorizedNode = func(models.MCPLease, string) (models.Client, error) {
		return models.Client{UUID: "node"}, nil
	}
	starts := 0
	mcpStartTerminal = func(string, string, string, string, time.Time, int, int) (string, error) {
		starts++
		if starts == 1 {
			return "", errors.New("Client is offline")
		}
		return "term-retried", nil
	}
	t.Cleanup(func() {
		database, lookupAuthorizedNode, mcpStartTerminal = oldDB, oldLookup, oldStart
	})
	lease := models.MCPLease{ID: "ls_open_limit", MaxConcurrency: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	args := map[string]any{"agent_uuid": "node", "idempotency_key": "open-retry"}
	if _, err := terminalOpenTool(lease, args, time.Now().UTC()); err == nil {
		t.Fatal("expected initial open failure")
	}
	if _, _, err := createOperation(lease, "node", "exec", "occupy-slot", "digest", lease.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	_, err := terminalOpenTool(lease, args, time.Now().UTC())
	if err != ErrConcurrency || starts != 1 {
		t.Fatalf("retry bypassed occupied slot: error=%v start calls=%d; want concurrency error and 1 start call", err, starts)
	}
	stored := mustLoadOpByKey(t, db, lease.ID, "open-retry")
	if stored.State != opFailed {
		t.Fatalf("blocked retry should keep the failed record, state=%s", stored.State)
	}
}

func TestTerminalOpenRetrySucceedsAfterSlotFreed(t *testing.T) {
	db := openMCPTestDB(t)
	oldDB, oldLookup, oldStart := database, lookupAuthorizedNode, mcpStartTerminal
	database = func() *gorm.DB { return db }
	lookupAuthorizedNode = func(models.MCPLease, string) (models.Client, error) {
		return models.Client{UUID: "node"}, nil
	}
	starts := 0
	mcpStartTerminal = func(string, string, string, string, time.Time, int, int) (string, error) {
		starts++
		if starts == 1 {
			return "", errors.New("Client is offline")
		}
		return "term-after-slot", nil
	}
	t.Cleanup(func() {
		database, lookupAuthorizedNode, mcpStartTerminal = oldDB, oldLookup, oldStart
	})
	lease := models.MCPLease{ID: "ls_open_free", MaxConcurrency: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	args := map[string]any{"agent_uuid": "node", "idempotency_key": "open-free"}
	if _, err := terminalOpenTool(lease, args, time.Now().UTC()); err == nil {
		t.Fatal("expected initial open failure")
	}
	occupy, _, err := createOperation(lease, "node", "exec", "occupy-slot", "digest", lease.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := terminalOpenTool(lease, args, time.Now().UTC()); err != ErrConcurrency {
		t.Fatalf("expected concurrency while slot occupied, err=%v", err)
	}
	failOperation(occupy.ID, "done")
	result, err := terminalOpenTool(lease, args, time.Now().UTC())
	if err != nil {
		t.Fatalf("retry after free: %v", err)
	}
	payload, _ := result.(gin.H)
	if payload["terminal_id"] != "term-after-slot" || starts != 2 {
		t.Fatalf("retry after free payload=%#v starts=%d", payload, starts)
	}
}

func mustLoadOpByKey(t *testing.T, db *gorm.DB, leaseID, key string) models.MCPOperation {
	t.Helper()
	var op models.MCPOperation
	if err := db.Where("lease_id = ? AND idempotency_key = ?", leaseID, key).First(&op).Error; err != nil {
		t.Fatalf("load operation: %v", err)
	}
	return op
}
