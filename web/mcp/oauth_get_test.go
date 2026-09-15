package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMCPGetUnauthenticatedReturnsBearerChallenge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := isMCPEnabled
	isMCPEnabled = func() bool { return true }
	t.Cleanup(func() { isMCPEnabled = previous })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:27777/mcp", nil)
	handleMCPGet(ctx)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	www := recorder.Header().Get("WWW-Authenticate")
	want := `resource_metadata="http://127.0.0.1:27777/.well-known/oauth-protected-resource/mcp"`
	if !strings.Contains(www, want) {
		t.Fatalf("WWW-Authenticate = %q", www)
	}
}

func TestMCPRouteMissingWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := isMCPEnabled
	isMCPEnabled = func() bool { return false }
	t.Cleanup(func() { isMCPEnabled = previous })

	engine := gin.New()
	registerPublicRoutes(engine)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/mcp"},
		{http.MethodPost, "/mcp"},
		{http.MethodOptions, "/mcp"},
		{http.MethodGet, "/mcp/"},
		{http.MethodGet, "/mcp/unknown"},
		{http.MethodGet, "/.well-known/oauth-protected-resource"},
		{http.MethodGet, "/.well-known/oauth-protected-resource/mcp"},
		{http.MethodGet, "/.well-known/oauth-authorization-server"},
		{http.MethodGet, "/oauth"},
		{http.MethodGet, "/oauth/"},
		{http.MethodGet, "/oauth/unknown"},
		{http.MethodPost, "/oauth/register"},
		{http.MethodGet, "/oauth/register"},
		{http.MethodOptions, "/oauth/register"},
		{http.MethodGet, "/oauth/authorize"},
		{http.MethodOptions, "/oauth/authorize"},
		{http.MethodPost, "/oauth/token"},
		{http.MethodGet, "/oauth/token"},
		{http.MethodPost, "/oauth/revoke"},
		{http.MethodGet, "/oauth/revoke"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, "http://127.0.0.1:27777"+tc.path, nil)
		engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, body = %s", tc.method, tc.path, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if strings.Contains(body, "invalid_token") ||
			strings.Contains(body, "access_denied") ||
			strings.Contains(body, "authorization_endpoint") ||
			strings.Contains(body, "Lite MCP") {
			t.Fatalf("%s %s should not advertise MCP when disabled: %s", tc.method, tc.path, body)
		}
		assertNotPublicSite(t, tc.method, tc.path, recorder)
	}
}

func TestMCPAndOAuthDoNotFallThroughToPublicSite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := isMCPEnabled
	isMCPEnabled = func() bool { return true }
	t.Cleanup(func() { isMCPEnabled = previous })

	engine := gin.New()
	engine.RedirectTrailingSlash = false
	registerPublicRoutes(engine)
	engine.NoRoute(func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<!doctype html><html><title>public theme</title></html>"))
	})

	for _, path := range []string{"/oauth", "/oauth/", "/oauth/unknown", "/mcp/", "/mcp/unknown"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:27777"+path, nil)
		engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, body = %s", path, recorder.Code, recorder.Body.String())
		}
		assertNotPublicSite(t, http.MethodGet, path, recorder)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:27777/mcp", nil)
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("GET /mcp status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	assertNotPublicSite(t, http.MethodGet, "/mcp", recorder)
}

func assertNotPublicSite(t *testing.T, method, path string, recorder *httptest.ResponseRecorder) {
	t.Helper()
	body := recorder.Body.String()
	ct := recorder.Header().Get("Content-Type")
	if strings.Contains(strings.ToLower(ct), "text/html") ||
		strings.Contains(strings.ToLower(body), "<html") ||
		strings.Contains(body, "public theme") {
		t.Fatalf("%s %s must not serve the public site: status=%d type=%q body=%s", method, path, recorder.Code, ct, body)
	}
}
