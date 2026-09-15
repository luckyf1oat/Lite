package mcp

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
)

func TestGroupHasLiveOperationIgnoresExpiredSiblings(t *testing.T) {
	now := time.Now().UTC()
	expired := models.MCPOperation{Deadline: now.Add(-time.Minute)}
	live := models.MCPOperation{Deadline: now.Add(time.Minute)}
	if groupHasLiveOperation([]models.MCPOperation{expired}, now) {
		t.Fatal("expired-only group should not renew")
	}
	if !groupHasLiveOperation([]models.MCPOperation{expired, live}, now) {
		t.Fatal("a live sibling should still renew the lease")
	}
}
