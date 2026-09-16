package remote

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/remotectl"
)

func TestAuthorizeRejectsAPIKeyPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:27777/api/admin/client/remote/authorize", strings.NewReader(`{"password":"x"}`))
	context.Request.Host = "127.0.0.1:27777"
	context.Request.RemoteAddr = "127.0.0.1:5273"
	context.Request.Header.Set("Origin", "http://127.0.0.1:27777")
	context.Request.Header.Set("Content-Type", "application/json")
	api.SetPrincipal(context, rpc.NewAPIKeyPrincipal())

	Authorize(context)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateSessionRequiresGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	remotectl.ResetForTest()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:27777/api/admin/client/remote/session", strings.NewReader(`{"uuid":"node-a"}`))
	context.Request.Host = "127.0.0.1:27777"
	context.Request.RemoteAddr = "127.0.0.1:5273"
	context.Request.Header.Set("Origin", "http://127.0.0.1:27777")
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.AddCookie(&http.Cookie{Name: "session_token", Value: "login-a"})
	api.SetPrincipal(context, rpc.NewUserPrincipal("user-a"))

	CreateSession(context)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if message, _ := payload["message"].(string); message != remotectl.ErrGrantRequired.Error() {
		t.Fatalf("message = %v, want grant required", payload["message"])
	}
}

func TestDispatchRemoteRequestAndCreateSessionUseDeliveryGate(t *testing.T) {
	src, err := os.ReadFile("request.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "GuardRemoteDelivery(") {
		t.Fatal("remote session dispatch is not on the same gate as disabling remote management")
	}
	if !strings.Contains(text, "putSessionUnderDeliveryGate(") {
		t.Fatal("remote session creation is not on the same gate as disabling remote management")
	}
	if !strings.Contains(text, "ConsumeAndRotateGrant(") {
		t.Fatal("creating a remote session must consume and rotate the grant")
	}
	if !strings.Contains(text, `"next_grant"`) {
		t.Fatal("creating a remote session must return the rotated grant")
	}
	if !strings.Contains(text, "peekRemoteSessionAdmission(") {
		t.Fatal("creating a remote session must check capacity before consuming the grant")
	}
	if !strings.Contains(text, "respondCreateSessionAdmissionError(") {
		t.Fatal("session admission failures after grant rotation must return the rotated grant")
	}
	if !strings.Contains(text, "rotatedGrantData(") {
		t.Fatal("session creation errors after grant rotation must include next_grant")
	}
}

func TestAuthorizeAndSessionSetNoStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:27777/api/admin/client/remote/authorize", strings.NewReader(`{}`))
	context.Request.Host = "127.0.0.1:27777"
	context.Request.RemoteAddr = "127.0.0.1:5273"
	context.Request.Header.Set("Origin", "http://127.0.0.1:27777")
	api.SetPrincipal(context, rpc.NewAPIKeyPrincipal())
	Authorize(context)
	if got := recorder.Header().Get("Cache-Control"); got != "no-store, private" {
		t.Fatalf("Cache-Control = %q", got)
	}
}
