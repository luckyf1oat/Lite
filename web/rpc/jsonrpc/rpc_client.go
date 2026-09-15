package jsonrpc

import (
	"encoding/json"

	"github.com/nuomiiiii/lite/database/models"
)

// rpcClient is the public RPC node payload. MCP capability flags stay on
// admin:listClients / admin:getClient only.
type rpcClient struct {
	models.Client
}

func (c rpcClient) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(c.Client)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	delete(object, "mcp_full")
	delete(object, "mcp_full_version")
	return json.Marshal(object)
}

func rpcClientsWithoutMCP(nodes []models.Client) []rpcClient {
	out := make([]rpcClient, len(nodes))
	for i, node := range nodes {
		out[i] = rpcClient{Client: node}
	}
	return out
}
