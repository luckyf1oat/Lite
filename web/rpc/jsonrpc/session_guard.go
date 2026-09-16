package jsonrpc

import (
	"context"

	"github.com/nuomiiiii/lite/pkg/rpc"
)

const errAPIKeyAction = "API keys cannot perform this action"
const errHumanSessionRequired = "This action requires an administrator session"

func denyAPIKey(ctx context.Context) *rpc.JsonRpcError {
	meta := rpc.MetaFromContext(ctx)
	if meta == nil || meta.Principal == nil {
		return rpc.MakeError(rpc.PermissionDenied, errHumanSessionRequired, nil)
	}
	if meta.Principal.IsAPIKey || meta.Principal.Type == rpc.PrincipalAPIKey {
		return rpc.MakeError(rpc.PermissionDenied, errAPIKeyAction, nil)
	}
	if meta.Principal.Type != rpc.PrincipalUser || meta.SessionToken == "" {
		return rpc.MakeError(rpc.PermissionDenied, errHumanSessionRequired, nil)
	}
	return nil
}
