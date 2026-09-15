package mcp

import (
	"testing"

	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

func TestMCPResultWeakRecognizesDeliveryTimeout(t *testing.T) {
	if !mcpResultWeak(v2.TaskResultParams{Result: v2.DeliveryTimeoutTaskResult}) {
		t.Fatal("delivery timeout should be a weak MCP result")
	}
	if mcpResultWeak(v2.TaskResultParams{Result: "ok", ExitCode: 0}) {
		t.Fatal("successful output should not be weak")
	}
	if !mcpOutputWeak(v2.DeliveryTimeoutTaskResult) {
		t.Fatal("stored delivery timeout should be weak")
	}
	if mcpOutputWeak("hostname\n") {
		t.Fatal("real command output should not be weak")
	}
}
