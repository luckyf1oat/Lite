package install

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/pkg/config"
)

// TestAgentScriptIsEmbedded guards the contract between the served installer
// and the enrollment endpoint: if either side is renamed the script silently
// stops working on child machines, which is hard to notice from the panel.
func TestAgentScriptIsEmbedded(t *testing.T) {
	if strings.TrimSpace(agentScript) == "" {
		t.Fatal("agent.sh must be embedded")
	}
	for _, want := range []string{
		"/api/clients/enroll",
		"Authorization: Bearer",
		"--enroll-key",
		"--enable-remote-control",
		"remote_control_enabled",
		"machine-id",
		"interval",
		"/install/agent.sh",
	} {
		if !strings.Contains(agentScript, want) {
			t.Fatalf("agent.sh is missing %q", want)
		}
	}
	if strings.Contains(agentScript, "\r\n") {
		t.Fatal("agent.sh must use LF line endings; CRLF breaks `sh` on target machines")
	}
}

func TestAgentScriptRequiresBothEndpointAndKey(t *testing.T) {
	if !strings.Contains(agentScript, "both -e/--endpoint and -k/--enroll-key are required") {
		t.Fatal("agent.sh must fail loudly when the endpoint or enrollment key is missing")
	}
}

func TestAgentScriptRouteIsHiddenWhileEnrollmentIsDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterAgentScript(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, AgentScriptPath, nil)
	router.ServeHTTP(recorder, request)

	// Enrollment defaults to disabled and the settings store is absent in this
	// test, so the route must behave exactly like a missing route.
	enabled, _ := config.GetAs[bool](config.EnrollEnabledKey, false)
	if enabled {
		t.Fatalf("expected enrollment to default to disabled, got enabled")
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while enrollment is disabled", recorder.Code)
	}
}
