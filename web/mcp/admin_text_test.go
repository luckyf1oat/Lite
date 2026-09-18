package mcp

import "testing"

func TestClipTextStripsANSI(t *testing.T) {
	raw := "\x1b[37;1mNextTrace\x1b[0;22m \x1b[90;1mv1.7.1\x1b[0;22m"
	if got := clipText(raw, AdminOutputPreviewMax); got != "NextTrace v1.7.1" {
		t.Fatalf("clipText = %q", got)
	}
	if got := stripANSI("\x1b[32;1mOK\x1b[0m\r\nnext"); got != "OK\nnext" {
		t.Fatalf("stripANSI = %q", got)
	}
}
