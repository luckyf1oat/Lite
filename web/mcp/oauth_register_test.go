package mcp

import (
	"testing"
	"time"
)

func TestAllowMCPRegisterRateLimit(t *testing.T) {
	ip := "203.0.113.9"
	now := time.Now().UTC()
	registerMu.Lock()
	delete(registerHits, ip)
	registerMu.Unlock()
	for i := 0; i < maxRegisterPerIPMinute; i++ {
		if !allowMCPRegister(ip, now) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if allowMCPRegister(ip, now) {
		t.Fatal("register burst should be rejected")
	}
	if !allowMCPRegister(ip, now.Add(time.Minute+time.Second)) {
		t.Fatal("register should be allowed after the window")
	}
}

func TestRawStringArgKeepsPadding(t *testing.T) {
	args := map[string]any{"data": "  keep  ", "path": " /tmp/a "}
	if rawStringArg(args, "data") != "  keep  " {
		t.Fatalf("raw data = %q", rawStringArg(args, "data"))
	}
	if stringArg(args, "path") != "/tmp/a" {
		t.Fatalf("trimmed path = %q", stringArg(args, "path"))
	}
}
