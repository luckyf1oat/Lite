package jsonrpc

import (
	"context"
	"strings"

	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/web/clientname"
)

// admin.client_naming.go
// 节点自动命名：新节点首次上报基础信息后，用出口探测结果把占位名称
// （client_xxxxxxxx）改写为「国家代码-IP-ASN-ISP」。命名在后台异步完成，
// 这里只提供人工补齐/重算入口，方便批量部署后一次性处理历史节点。

func init() {
	reg("nameClients", adminNameClients, "Resolve egress identity and rename nodes that still use the placeholder name")
}

func adminNameClients(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUIDs []string `json:"uuids"`
		All   bool     `json:"all"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid request body: "+err.Error(), nil)
	}
	if !clientname.Enabled() {
		return nil, rpc.MakeError(rpc.InvalidRequest, "automatic client naming is disabled", nil)
	}

	targets, err := namingTargets(params.UUIDs, params.All)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to list clients: "+err.Error(), nil)
	}

	named := make([]map[string]string, 0, len(targets))
	skipped := 0
	for _, target := range targets {
		ip := namingAddress(target)
		if ip == "" {
			skipped++
			continue
		}
		name, resolveErr := clientname.NameForIP(ctx, ip)
		if resolveErr != nil {
			skipped++
			continue
		}
		if applyErr := clientname.ApplyGeneratedName(target.UUID, name); applyErr != nil {
			skipped++
			continue
		}
		named = append(named, map[string]string{"uuid": target.UUID, "name": name})
	}

	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "name clients: "+strings.Join(params.UUIDs, ","), "info")

	return map[string]any{
		"requested": len(targets),
		"named":     named,
		"skipped":   skipped,
	}, nil
}

// namingTargets selects the nodes eligible for naming. Only nodes still using
// the registration placeholder are considered, so an administrator rename is
// never overwritten.
func namingTargets(uuids []string, all bool) ([]models.Client, error) {
	db := dbcore.GetDBInstance()
	query := db.Select("uuid", "name", "ipv4", "ipv6", "name_auto_generated")
	if !all {
		if len(uuids) == 0 {
			return nil, nil
		}
		query = query.Where("uuid IN ?", uuids)
	}
	var found []models.Client
	if err := query.Find(&found).Error; err != nil {
		return nil, err
	}
	out := make([]models.Client, 0, len(found))
	for _, client := range found {
		if !clientname.IsPlaceholderName(client.Name) {
			continue
		}
		out = append(out, client)
	}
	return out, nil
}

func namingAddress(client models.Client) string {
	if ip := strings.TrimSpace(client.IPv4); ip != "" {
		return ip
	}
	return strings.TrimSpace(client.IPv6)
}
