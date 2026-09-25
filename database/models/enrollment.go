package models

import "time"

// EnrollmentKey 是一次批量自注册（enroll）凭据。
//
// 它只用于「把新机器注册成节点」，不能读取或修改任何已有节点：
// 持有者拿到的是**新生效节点**的 token，无法指定或影响既有 UUID。
//
// 明文密钥只在创建时返回一次，库中仅保存 sha256 摘要与前缀。
type EnrollmentKey struct {
	ID        string `json:"id" gorm:"type:varchar(36);primaryKey"`
	Name      string `json:"name" gorm:"type:varchar(100);not null;default:''"`
	KeyHash   string `json:"-" gorm:"type:varchar(64);uniqueIndex:ux_enrollment_keys_hash;not null"`
	Prefix    string `json:"prefix" gorm:"type:varchar(16);not null;default:''"`
	MaxUses   int    `json:"max_uses" gorm:"not null;default:0"`
	UsedCount int    `json:"used_count" gorm:"not null;default:0"`
	// AllowedCIDRs 是逗号分隔的来源网段白名单，空表示不限制。
	AllowedCIDRs string     `json:"allowed_cidrs" gorm:"type:text;not null;default:''"`
	ExpiresAt    time.Time  `json:"expires_at" gorm:"not null"`
	RevokedAt    *time.Time `json:"revoked_at"`
	CreatedBy    string     `json:"created_by" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// TableName pins the physical name; GORM's default pluralisation would produce
// "enrollment_keys" as well, but stating it keeps the intent explicit.
func (EnrollmentKey) TableName() string { return "enrollment_keys" }

// Active reports whether the key may still be used at the given time.
// A zero ExpiresAt means the key never expires and stays usable until revoked.
func (k EnrollmentKey) Active(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	if !k.ExpiresAt.IsZero() && !k.ExpiresAt.After(now) {
		return false
	}
	if k.MaxUses > 0 && k.UsedCount >= k.MaxUses {
		return false
	}
	return true
}

// EnrolledNode 把「机器指纹」映射到已注册节点，使重复执行同一条指令保持幂等。
type EnrolledNode struct {
	// Fingerprint 是 sha256(enrollmentKeyID + 机器指纹)，避免跨密钥复用同一台机器时串号。
	Fingerprint string `json:"fingerprint" gorm:"type:varchar(64);primaryKey"`
	ClientUUID  string `json:"client_uuid" gorm:"type:varchar(36);index;not null"`
	KeyID       string `json:"key_id" gorm:"type:varchar(36);index;not null"`
	// ReportedAt 记录该节点是否已经真实上报过，用于回收「注册了但从未上线」的孤儿节点。
	ReportedAt *time.Time `json:"reported_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func (EnrolledNode) TableName() string { return "enrolled_nodes" }
