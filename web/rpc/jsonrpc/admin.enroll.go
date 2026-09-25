package jsonrpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/utils"
)

// admin.enroll.go
// 批量自注册（enroll）密钥的签发、查看与撤销。
//
// 明文密钥只在创建时返回一次；库中仅保存 sha256 摘要与前缀。
// 这些方法都属于高权限操作：拿到密钥即可注册新节点，因此标记为敏感操作。

const (
	enrollKeyDefaultHours = 168  // 7 天
	enrollKeyDefaultUses  = 200  // 最多注册 200 台
	enrollKeyMaxHours     = 8760 // 1 年
	enrollKeyMaxUses      = 10000
)

func init() {
	RegisterWithGroupAndMeta("createEnrollmentKey", rpc.RoleAdmin, adminCreateEnrollmentKey, &rpc.MethodMeta{
		Name:    "admin:createEnrollmentKey",
		Summary: "Issue a bulk enrollment key for one-command agent deployment",
	})
	RegisterWithGroupAndMeta("listEnrollmentKeys", rpc.RoleAdmin, adminListEnrollmentKeys, &rpc.MethodMeta{
		Name:    "admin:listEnrollmentKeys",
		Summary: "List issued enrollment keys (prefixes only)",
	})
	RegisterWithGroupAndMeta("revokeEnrollmentKey", rpc.RoleAdmin, adminRevokeEnrollmentKey, &rpc.MethodMeta{
		Name:    "admin:revokeEnrollmentKey",
		Summary: "Revoke an enrollment key so it can no longer register nodes",
	})
	RegisterWithGroupAndMeta("listEnrolledNodes", rpc.RoleAdmin, adminListEnrolledNodes, &rpc.MethodMeta{
		Name:    "admin:listEnrolledNodes",
		Summary: "List nodes that were registered through enrollment keys",
	})
	// Issuing a key grants the ability to register nodes, so treat it like the
	// other token-revealing operations.
	rpc.MarkSensitive("admin:createEnrollmentKey")
}

type enrollmentKeyView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Prefix       string `json:"prefix"`
	MaxUses      int    `json:"max_uses"`
	UsedCount    int    `json:"used_count"`
	AllowedCIDRs string `json:"allowed_cidrs"`
	// ExpiresAt 为 null 表示永不失效（与 NeverExpires 冗余，便于前端直接判断）。
	ExpiresAt    *time.Time `json:"expires_at"`
	NeverExpires bool       `json:"never_expires"`
	RevokedAt    *time.Time `json:"revoked_at"`
	CreatedAt    time.Time  `json:"created_at"`
	Active       bool       `json:"active"`
	// Command 是可直接分发的部署指令；仅在创建时返回，因为其中含明文密钥。
	Command string `json:"command,omitempty"`
}

func viewEnrollmentKey(key models.EnrollmentKey, now time.Time) enrollmentKeyView {
	view := enrollmentKeyView{
		ID:           key.ID,
		Name:         key.Name,
		Prefix:       key.Prefix,
		MaxUses:      key.MaxUses,
		UsedCount:    key.UsedCount,
		AllowedCIDRs: key.AllowedCIDRs,
		NeverExpires: key.ExpiresAt.IsZero(),
		RevokedAt:    key.RevokedAt,
		CreatedAt:    key.CreatedAt,
		Active:       key.Active(now),
	}
	// Never emit the zero time: the panel and any API client should see an
	// explicit null rather than 0001-01-01.
	if !key.ExpiresAt.IsZero() {
		expires := key.ExpiresAt
		view.ExpiresAt = &expires
	}
	return view
}

func adminCreateEnrollmentKey(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Name         string `json:"name"`
		MaxUses      int    `json:"max_uses"`
		ExpiresHours int    `json:"expires_in_hours"`
		AllowedCIDRs string `json:"allowed_cidrs"`
		Endpoint     string `json:"endpoint"`
		Interval     int    `json:"interval"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid request body: "+err.Error(), nil)
	}

	// expires_in_hours semantics:
	//   - omitted / negative -> use the default (7 days)
	//   - 0                 -> never expires
	//   - positive          -> that many hours, capped at one year
	hours := params.ExpiresHours
	neverExpires := hours == 0
	if hours < 0 {
		hours = enrollKeyDefaultHours
	}
	if !neverExpires && hours > enrollKeyMaxHours {
		return nil, rpc.MakeError(rpc.InvalidParams, "expires_in_hours must be at most 8760 (1 year); use 0 for no expiry", nil)
	}
	uses := params.MaxUses
	if uses <= 0 {
		uses = enrollKeyDefaultUses
	}
	if uses > enrollKeyMaxUses {
		return nil, rpc.MakeError(rpc.InvalidParams, "max_uses must be at most 10000", nil)
	}

	allowed, err := normalizeEnrollCIDRs(params.AllowedCIDRs)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}

	plain := utils.GenerateRandomString(40)
	sum := sha256.Sum256([]byte(plain))
	now := time.Now().UTC()
	actor, ip := auditActor(ctx)
	key := models.EnrollmentKey{
		ID:           uuid.New().String(),
		Name:         strings.TrimSpace(params.Name),
		KeyHash:      hex.EncodeToString(sum[:]),
		Prefix:       plain[:8],
		MaxUses:      uses,
		AllowedCIDRs: allowed,
		CreatedBy:    actor,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if !neverExpires {
		key.ExpiresAt = now.Add(time.Duration(hours) * time.Hour)
	}
	if err := dbcore.GetDBInstance().Create(&key).Error; err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to issue enrollment key: "+err.Error(), nil)
	}

	view := viewEnrollmentKey(key, now)
	view.Command = buildEnrollCommand(params.Endpoint, plain, params.Interval)

	auditlog.Log(ip, actor, "issue enrollment key "+key.Prefix+" ("+key.Name+")", "warn")
	return view, nil
}

func adminListEnrollmentKeys(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var keys []models.EnrollmentKey
	if err := dbcore.GetDBInstance().Order("created_at DESC").Find(&keys).Error; err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to list enrollment keys: "+err.Error(), nil)
	}
	now := time.Now().UTC()
	views := make([]enrollmentKeyView, 0, len(keys))
	for _, key := range keys {
		views = append(views, viewEnrollmentKey(key, now))
	}
	return views, nil
}

func adminRevokeEnrollmentKey(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		ID string `json:"id"`
	}
	if err := req.BindParams(&params); err != nil || strings.TrimSpace(params.ID) == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "id is required", nil)
	}
	now := time.Now().UTC()
	result := dbcore.GetDBInstance().Model(&models.EnrollmentKey{}).
		Where("id = ? AND revoked_at IS NULL", params.ID).
		Updates(map[string]any{"revoked_at": now, "updated_at": now})
	if result.Error != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to revoke enrollment key: "+result.Error.Error(), nil)
	}
	if result.RowsAffected == 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "enrollment key not found or already revoked", nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "revoke enrollment key "+params.ID, "warn")
	return map[string]any{"id": params.ID, "revoked": true}, nil
}

func adminListEnrolledNodes(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	type row struct {
		ClientUUID string     `json:"client_uuid"`
		Name       string     `json:"name"`
		KeyPrefix  string     `json:"key_prefix"`
		ReportedAt *time.Time `json:"reported_at"`
		CreatedAt  time.Time  `json:"created_at"`
	}
	var rows []row
	err := dbcore.GetDBInstance().Raw(`
		SELECT e.client_uuid AS client_uuid,
		       COALESCE(c.name, '') AS name,
		       COALESCE(k.prefix, '') AS key_prefix,
		       e.reported_at AS reported_at,
		       e.created_at AS created_at
		FROM enrolled_nodes e
		LEFT JOIN clients c ON c.uuid = e.client_uuid
		LEFT JOIN enrollment_keys k ON k.id = e.key_id
		ORDER BY e.created_at DESC
		LIMIT 500
	`).Scan(&rows).Error
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to list enrolled nodes: "+err.Error(), nil)
	}
	return rows, nil
}

// normalizeEnrollCIDRs validates and canonicalises a comma separated allowlist.
func normalizeEnrollCIDRs(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parts := make([]string, 0, 4)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			parts = append(parts, network.String())
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			parts = append(parts, ip.String())
			continue
		}
		return "", errors.New("allowed_cidrs contains an invalid CIDR or IP: " + entry)
	}
	return strings.Join(parts, ","), nil
}

// buildEnrollCommand renders the one-liner an operator distributes.
func buildEnrollCommand(endpoint, plainKey string, interval int) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = "https://<your-panel-domain>"
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if interval < 1 || interval > 3600 {
		interval = 600
	}
	return "curl -fsSL " + endpoint + "/install/agent.sh | sudo bash -s -- " +
		"-e " + endpoint + " -k " + plainKey + " -i " + strconv.Itoa(interval)
}
