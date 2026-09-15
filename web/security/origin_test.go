package security

import (
	"net/http/httptest"
	"testing"
)

func TestOriginMatchesRequestThroughPrivateReverseProxy(t *testing.T) {
	request := httptest.NewRequest("GET", "http://lite:27777/api/clients", nil)
	request.Host = "lite:27777"
	request.RemoteAddr = "172.17.0.1:41234"
	request.Header.Set("X-Forwarded-Host", "monitor.example.com")

	if !OriginMatchesRequest("https://monitor.example.com", request) {
		t.Fatal("forwarded public host was not accepted from a private reverse proxy")
	}
}

func TestOriginMatchesRequestSupportsStandardForwardedHost(t *testing.T) {
	request := httptest.NewRequest("GET", "http://127.0.0.1:27777/api/clients", nil)
	request.Host = "127.0.0.1:27777"
	request.RemoteAddr = "127.0.0.1:41234"
	request.Header.Set("Forwarded", `for=192.0.2.10;proto=https;host="monitor.example.com"`)

	if !OriginMatchesRequest("https://monitor.example.com", request) {
		t.Fatal("standard forwarded host was not accepted from a loopback proxy")
	}
}

func TestOriginMatchesRequestRejectsSpoofedForwardedHost(t *testing.T) {
	request := httptest.NewRequest("GET", "http://monitor.internal/api/clients", nil)
	request.Host = "monitor.internal"
	request.RemoteAddr = "203.0.113.10:41234"
	request.Header.Set("X-Forwarded-Host", "evil.example")

	if OriginMatchesRequest("https://evil.example", request) {
		t.Fatal("forwarded host from a public client bypassed origin validation")
	}
}

func TestRemoteOriginAllowedRejectsEmptyNullAndCrossOrigin(t *testing.T) {
	request := httptest.NewRequest("GET", "https://monitor.example/api/admin/mcp/settings", nil)
	request.Host = "monitor.example"
	request.RemoteAddr = "203.0.113.10:443"

	if !RemoteOriginAllowed(request) {
		t.Fatal("same-origin GET without Origin was rejected")
	}
	request.Header.Set("Origin", "null")
	if RemoteOriginAllowed(request) {
		t.Fatal("opaque origin was accepted")
	}
	request.Header.Set("Origin", "https://attacker.example")
	if RemoteOriginAllowed(request) {
		t.Fatal("cross-origin remote request was accepted")
	}
	request.Header.Set("Origin", "https://monitor.example")
	if !RemoteOriginAllowed(request) {
		t.Fatal("same-origin remote request was rejected")
	}
}

func TestRemoteOriginAllowedAllowsEmptyOriginOnSafeMethodsOnly(t *testing.T) {
	get := httptest.NewRequest("GET", "https://monitor.example/api/admin/mcp/settings", nil)
	get.Host = "monitor.example"
	get.RemoteAddr = "203.0.113.10:443"
	if !RemoteOriginAllowed(get) {
		t.Fatal("GET without Origin was rejected")
	}

	head := httptest.NewRequest("HEAD", "https://monitor.example/api/admin/client/remote", nil)
	head.Host = "monitor.example"
	head.RemoteAddr = "203.0.113.10:443"
	if !RemoteOriginAllowed(head) {
		t.Fatal("HEAD without Origin was rejected")
	}

	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		write := httptest.NewRequest(method, "https://monitor.example/api/admin/mcp/settings", nil)
		write.Host = "monitor.example"
		write.RemoteAddr = "203.0.113.10:443"
		if RemoteOriginAllowed(write) {
			t.Fatalf("%s without Origin was accepted", method)
		}
		write.Header.Set("Origin", "https://attacker.example")
		if RemoteOriginAllowed(write) {
			t.Fatalf("cross-origin %s was accepted", method)
		}
		write.Header.Set("Origin", "https://monitor.example")
		if !RemoteOriginAllowed(write) {
			t.Fatalf("same-origin %s was rejected", method)
		}
	}
}

func TestRemoteOriginAllowedRejectsCrossOriginGet(t *testing.T) {
	request := httptest.NewRequest("GET", "https://monitor.example/api/admin/mcp/leases", nil)
	request.Host = "monitor.example"
	request.RemoteAddr = "203.0.113.10:443"
	request.Header.Set("Origin", "https://attacker.example")
	if RemoteOriginAllowed(request) {
		t.Fatal("cross-origin GET was accepted")
	}
}

func TestRemoteOriginAllowedAllowsLoopbackViteAndRejectsSpoofedVite(t *testing.T) {
	loopback := httptest.NewRequest("GET", "http://127.0.0.1:27777/api/admin/client/remote", nil)
	loopback.Host = "127.0.0.1:27777"
	loopback.RemoteAddr = "127.0.0.1:41234"
	loopback.Header.Set("Origin", "http://127.0.0.1:5273")
	if !RemoteOriginAllowed(loopback) {
		t.Fatal("loopback Vite origin was rejected")
	}

	public := httptest.NewRequest("GET", "http://127.0.0.1:27777/api/admin/client/remote", nil)
	public.Host = "127.0.0.1:27777"
	public.RemoteAddr = "203.0.113.10:41234"
	public.Header.Set("Origin", "http://127.0.0.1:5273")
	if RemoteOriginAllowed(public) {
		t.Fatal("non-loopback client used a spoofed Vite origin")
	}
}

func TestRemoteOriginAllowedKeepsForwardedHostRulesOnWrites(t *testing.T) {
	private := httptest.NewRequest("POST", "http://lite:27777/api/admin/mcp/settings", nil)
	private.Host = "lite:27777"
	private.RemoteAddr = "172.17.0.1:41234"
	private.Header.Set("X-Forwarded-Host", "monitor.example.com")
	if RemoteOriginAllowed(private) {
		t.Fatal("POST without Origin was accepted behind a private reverse proxy")
	}
	private.Header.Set("Origin", "https://monitor.example.com")
	if !RemoteOriginAllowed(private) {
		t.Fatal("POST matching X-Forwarded-Host from a private reverse proxy was rejected")
	}

	forwarded := httptest.NewRequest("POST", "http://127.0.0.1:27777/api/admin/mcp/settings", nil)
	forwarded.Host = "127.0.0.1:27777"
	forwarded.RemoteAddr = "127.0.0.1:41234"
	forwarded.Header.Set("Forwarded", `for=192.0.2.10;proto=https;host="monitor.example.com"`)
	forwarded.Header.Set("Origin", "https://monitor.example.com")
	if !RemoteOriginAllowed(forwarded) {
		t.Fatal("POST matching standard Forwarded host from a loopback proxy was rejected")
	}

	public := httptest.NewRequest("POST", "http://monitor.internal/api/admin/mcp/settings", nil)
	public.Host = "monitor.internal"
	public.RemoteAddr = "203.0.113.10:41234"
	public.Header.Set("X-Forwarded-Host", "evil.example")
	public.Header.Set("Origin", "https://evil.example")
	if RemoteOriginAllowed(public) {
		t.Fatal("POST used a spoofed public X-Forwarded-Host")
	}

	get := httptest.NewRequest("GET", "http://lite:27777/api/admin/mcp/settings", nil)
	get.Host = "lite:27777"
	get.RemoteAddr = "172.17.0.1:41234"
	get.Header.Set("X-Forwarded-Host", "monitor.example.com")
	if !RemoteOriginAllowed(get) {
		t.Fatal("GET without Origin behind a private reverse proxy was rejected")
	}
}

func TestOriginMatchesRequestKeepsDirectSameHostSupport(t *testing.T) {
	request := httptest.NewRequest("GET", "https://monitor.example.com/api/clients", nil)
	request.Host = "monitor.example.com"
	request.RemoteAddr = "203.0.113.10:41234"

	if !OriginMatchesRequest("https://monitor.example.com", request) {
		t.Fatal("direct same-host origin was rejected")
	}
}
