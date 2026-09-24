package install

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/pkg/config"
)

// AgentScriptPath is the public path that serves the bulk enrollment installer.
const AgentScriptPath = "/install/agent.sh"

// agentScript is the installer handed to child machines. It is served by the
// panel itself so a node only needs to reach the panel, not GitHub.
//
//go:embed agent.sh
var agentScript string

// AgentScript serves the enrollment installer.
//
// The endpoint is always reachable so operators can read and self-host the
// script, but it refuses to hand out a script when enrollment is disabled: the
// script would be useless, and serving it would advertise an unused feature.
func AgentScript(c *gin.Context) {
	enabled, _ := config.GetAs[bool](config.EnrollEnabledKey, false)
	if !enabled {
		c.Status(http.StatusNotFound)
		return
	}
	// The installer evolves together with the server. A stale copy served from
	// an edge cache would silently deploy an old script, so forbid caching.
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Data(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(agentScript))
}

// RegisterAgentScript binds the enrollment installer path.
func RegisterAgentScript(r *gin.Engine) {
	r.GET(AgentScriptPath, AgentScript)
}
