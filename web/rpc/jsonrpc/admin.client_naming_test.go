package jsonrpc

import (
	"testing"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
)

// namingRegistration verifies the admin entry point exists and is admin-only,
// without invoking it (the handler reaches the settings store, which a unit test
// process does not initialize).
func TestNameClientsRegistration(t *testing.T) {
	if got := rpc.RequiredRole("admin:nameClients"); got != rpc.RoleAdmin {
		t.Fatalf("required role = %q, want %q", got, rpc.RoleAdmin)
	}
	if rpc.RequiredRole("admin:nameClients") == rpc.RoleGuest {
		t.Fatal("admin:nameClients must not be reachable by guests")
	}
}

func TestNameClientsRejectsInvalidBody(t *testing.T) {
	req := &rpc.JsonRpcRequest{Version: rpc.RPC_VERSION, Method: "admin:nameClients", ID: 1, Params: "not-an-object"}
	if _, rpcErr := adminNameClients(t.Context(), req); rpcErr == nil {
		t.Fatal("expected an invalid params error")
	}
}

func TestNamingAddressPrefersIPv4(t *testing.T) {
	tests := []struct {
		name string
		ipv4 string
		ipv6 string
		want string
	}{
		{"ipv4 wins", "203.0.113.7", "2001:db8::1", "203.0.113.7"},
		{"ipv6 fallback", "", "2001:db8::1", "2001:db8::1"},
		{"blank input", "   ", "  ", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := models.Client{IPv4: test.ipv4, IPv6: test.ipv6}
			if got := namingAddress(client); got != test.want {
				t.Fatalf("namingAddress = %q, want %q", got, test.want)
			}
		})
	}
}
