package public

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/notificationdefaults"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/nuomiiiii/lite/web/api"
	"gorm.io/gorm"
)

// enroll.go
// 批量自注册端点：一台机器执行同一条指令即可领取自己的节点身份。
//
// 设计要点：
//   - 默认关闭（config.EnrollEnabledKey）。关闭时端点 404，与现状完全一致。
//   - 明文 enroll 密钥只存 sha256 摘要；校验用固定时间比较。
//   - 幂等：同一台机器重复执行返回同一个节点与 token，不会产生重复节点。
//   - 只能新建节点，不接受任何已有节点 UUID，因此无法读取或影响现存节点。

const (
	// enrollMaxBodyBytes bounds the request body.
	enrollMaxBodyBytes = 4 << 10
	// enrollFingerprintMinLen rejects obviously malformed fingerprints.
	enrollFingerprintMinLen = 8
	// enrollRateWindow is the window used by the per-IP limiter.
	enrollRateWindow = time.Hour
	// enrollRateMaxEntries bounds limiter memory.
	enrollRateMaxEntries = 4096
	// enrollDefaultMaxPerHour applies when the setting is absent.
	enrollDefaultMaxPerHour = 60
	// enrollHostnameMaxLen bounds the hostname recorded at enrollment. It is far
	// below clients.name's varchar(100) because the auto-namer later appends
	// "国家代码-IP-ASN-ISP" to this placeholder.
	enrollHostnameMaxLen = 64
)

var (
	errEnrollDisabled   = errors.New("enrollment is disabled")
	errEnrollAuth       = errors.New("enrollment key is invalid or expired")
	errEnrollRateLimit  = errors.New("too many enrollment attempts from this address")
	errEnrollCIDR       = errors.New("this address is not allowed by the enrollment key")
	errEnrollBadRequest = errors.New("invalid enrollment request")
)

type enrollRequest struct {
	Fingerprint string `json:"fingerprint"`
	Hostname    string `json:"hostname"`
}

type enrollResponse struct {
	UUID  string `json:"uuid"`
	Token string `json:"token"`
	Name  string `json:"name"`
}

type enrollRateBucket struct {
	windowStart time.Time
	count       int
}

var (
	enrollRateMu      sync.Mutex
	enrollRateBuckets = make(map[string]enrollRateBucket)
)

// EnrollEnabled reports whether the enrollment endpoint is switched on.
func EnrollEnabled() bool {
	enabled, err := config.GetAs[bool](config.EnrollEnabledKey, false)
	if err != nil {
		return false
	}
	return enabled
}

func enrollMaxPerHour() int {
	value, err := config.GetAs[int](config.EnrollMaxPerHourKey, enrollDefaultMaxPerHour)
	if err != nil || value < 0 {
		return enrollDefaultMaxPerHour
	}
	return value
}

// enrollBearerToken 读取 Authorization: Bearer <token>，并拒绝站点 API Key。
// 站点 API Key 拥有管理员能力，绝不能被当作注册凭据。
func enrollBearerToken(c *gin.Context) string {
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		return ""
	}
	token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	if token == "" {
		return ""
	}
	if api.IsSiteAPIKey(authorization) {
		return ""
	}
	return token
}

// Enroll is the HTTP handler for POST /api/clients/enroll.
func Enroll(c *gin.Context) {
	// Disabled must look exactly like "route does not exist" so the presence of
	// this feature is not advertised on instances that do not use it.
	if !EnrollEnabled() {
		c.Status(http.StatusNotFound)
		return
	}

	plain := enrollBearerToken(c)
	if plain == "" {
		respondEnrollError(c, http.StatusUnauthorized, errEnrollAuth)
		return
	}
	clientIP := c.ClientIP()
	if !allowEnrollAttempt(clientIP, plain) {
		respondEnrollError(c, http.StatusTooManyRequests, errEnrollRateLimit)
		return
	}

	var request enrollRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondEnrollError(c, http.StatusBadRequest, errEnrollBadRequest)
		return
	}
	request.Fingerprint = strings.TrimSpace(request.Fingerprint)
	request.Hostname = strings.TrimSpace(request.Hostname)
	if len(request.Fingerprint) < enrollFingerprintMinLen || len(request.Fingerprint) > 128 {
		respondEnrollError(c, http.StatusBadRequest, errEnrollBadRequest)
		return
	}
	// Truncated well below the column limit: the auto-namer later appends
	// "国家代码-IP-ASN-ISP" to this placeholder, and a long IPv6 address can
	// otherwise overflow varchar(100) and break the panel layout.
	if len(request.Hostname) > enrollHostnameMaxLen {
		request.Hostname = request.Hostname[:enrollHostnameMaxLen]
	}

	now := time.Now().UTC()
	key, err := lookupEnrollmentKey(plain)
	if err != nil {
		respondEnrollError(c, http.StatusUnauthorized, errEnrollAuth)
		return
	}
	if !key.Active(now) {
		respondEnrollError(c, http.StatusForbidden, errEnrollAuth)
		return
	}
	if !enrollCIDRAllows(key.AllowedCIDRs, clientIP) {
		auditlog.Log(clientIP, "enroll", "enrollment rejected by cidr allowlist key="+key.Prefix, "warn")
		respondEnrollError(c, http.StatusForbidden, errEnrollCIDR)
		return
	}

	result, err := enrollNode(key, request.Fingerprint, now)
	switch {
	case errors.Is(err, errEnrollAuth):
		respondEnrollError(c, http.StatusForbidden, errEnrollAuth)
		return
	case err != nil:
		logger.Errorf("enroll", "failed to enroll node for key %s: %v", key.Prefix, err)
		respondEnrollError(c, http.StatusInternalServerError, err)
		return
	}

	auditlog.Log(clientIP, "enroll", "enrolled node "+result.UUID+" via key "+key.Prefix, "info")
	c.JSON(http.StatusOK, gin.H{"status": "success", "data": result})
}

// lookupEnrollmentKey resolves the plaintext key to its stored record.
func lookupEnrollmentKey(plain string) (models.EnrollmentKey, error) {
	sum := sha256.Sum256([]byte(plain))
	digest := hex.EncodeToString(sum[:])
	var key models.EnrollmentKey
	err := dbcore.GetDBInstance().Where("key_hash = ?", digest).First(&key).Error
	if err != nil {
		return models.EnrollmentKey{}, err
	}
	// Constant-time-ish guard: the lookup already matched the digest, but
	// re-verify so a future index change cannot silently weaken this.
	if subtleCompare(key.KeyHash, digest) != 1 {
		return models.EnrollmentKey{}, gorm.ErrRecordNotFound
	}
	return key, nil
}

// enrollNode creates (or reuses) the node bound to this machine fingerprint.
func enrollNode(key models.EnrollmentKey, fingerprint string, now time.Time) (enrollResponse, error) {
	db := dbcore.GetDBInstance()
	// Scope the fingerprint by key so the same machine enrolled through two
	// different keys does not silently share one node record.
	scoped := sha256.Sum256([]byte(key.ID + "\x00" + fingerprint))
	mapped := hex.EncodeToString(scoped[:])

	var existing models.EnrolledNode
	err := db.Where("fingerprint = ?", mapped).First(&existing).Error
	if err == nil {
		var client models.Client
		if err := db.Select("uuid", "token", "name").First(&client, "uuid = ?", existing.ClientUUID).Error; err != nil {
			// The node was deleted out from under the mapping: drop the stale
			// mapping and fall through to a fresh registration.
			if errors.Is(err, gorm.ErrRecordNotFound) {
				_ = db.Where("fingerprint = ?", mapped).Delete(&models.EnrolledNode{}).Error
			} else {
				return enrollResponse{}, err
			}
		} else {
			return enrollResponse{UUID: client.UUID, Token: client.Token, Name: client.Name}, nil
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return enrollResponse{}, err
	}

	clientUUID := uuid.New().String()
	token := utils.GenerateToken()
	placeholder := "client_" + clientUUID[:8]

	err = db.Transaction(func(tx *gorm.DB) error {
		// Re-check the quota inside the transaction so two concurrent requests
		// cannot both consume the last slot.
		var fresh models.EnrollmentKey
		if err := tx.First(&fresh, "id = ?", key.ID).Error; err != nil {
			return err
		}
		if !fresh.Active(now) {
			return errEnrollAuth
		}
		client := models.Client{
			UUID:             clientUUID,
			Token:            token,
			Name:             placeholder,
			TrafficLimitType: "sum",
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := tx.Create(&client).Error; err != nil {
			return err
		}
		mapping := models.EnrolledNode{
			Fingerprint: mapped,
			ClientUUID:  clientUUID,
			KeyID:       key.ID,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := tx.Create(&mapping).Error; err != nil {
			return err
		}
		return tx.Model(&models.EnrollmentKey{}).Where("id = ?", key.ID).
			Update("used_count", gorm.Expr("used_count + 1")).Error
	})
	if err != nil {
		return enrollResponse{}, err
	}

	// Mirror the normal "add node" side effects. A failure here does not fail
	// enrollment: the node is already usable.
	if err := tasks.AddDefaultOnClientUUID(clientUUID); err != nil {
		logger.Warnf("enroll", "failed to apply default ping tasks to enrolled node %s: %v", clientUUID, err)
	}
	if err := notificationdefaults.ApplyDefaultsToNewClient(clientUUID); err != nil {
		logger.Warnf("enroll", "failed to apply notification defaults to enrolled node %s: %v", clientUUID, err)
	}
	return enrollResponse{UUID: clientUUID, Token: token, Name: placeholder}, nil
}

// MarkEnrolledNodeReported records that a registered node has actually reported,
// which exempts it from orphan cleanup. Unknown UUIDs are ignored.
func MarkEnrolledNodeReported(clientUUID string, now time.Time) {
	if clientUUID == "" {
		return
	}
	err := dbcore.GetDBInstance().Model(&models.EnrolledNode{}).
		Where("client_uuid = ? AND reported_at IS NULL", clientUUID).
		Update("reported_at", now).Error
	if err != nil {
		logger.Warnf("enroll", "failed to mark enrolled node %s as reported: %v", clientUUID, err)
	}
}

// CleanupEnrolledNodes removes nodes that registered but never reported within
// ttl, releasing their enrollment quota. It returns how many nodes were removed.
func CleanupEnrolledNodes(ttl time.Duration) (int, error) {
	if ttl <= 0 {
		return 0, nil
	}
	db := dbcore.GetDBInstance()
	cutoff := time.Now().UTC().Add(-ttl)

	var stale []models.EnrolledNode
	if err := db.Where("reported_at IS NULL AND created_at < ?", cutoff).Find(&stale).Error; err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}

	removed := 0
	for _, mapping := range stale {
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("uuid = ?", mapping.ClientUUID).Delete(&models.Client{}).Error; err != nil {
				return err
			}
			if err := tx.Where("fingerprint = ?", mapping.Fingerprint).Delete(&models.EnrolledNode{}).Error; err != nil {
				return err
			}
			return tx.Model(&models.EnrollmentKey{}).Where("id = ?", mapping.KeyID).
				Update("used_count", gorm.Expr("CASE WHEN used_count > 0 THEN used_count - 1 ELSE 0 END")).Error
		})
		if err != nil {
			logger.Warnf("enroll", "failed to clean up enrolled node %s: %v", mapping.ClientUUID, err)
			continue
		}
		removed++
	}
	return removed, nil
}

// allowEnrollAttempt applies a per-address hourly limit in front of key checks.
func allowEnrollAttempt(ip, plain string) bool {
	limit := enrollMaxPerHour()
	if limit <= 0 {
		return true
	}
	sum := sha256.Sum256([]byte(plain))
	key := ip + "|" + hex.EncodeToString(sum[:8])
	now := time.Now()

	enrollRateMu.Lock()
	defer enrollRateMu.Unlock()
	if len(enrollRateBuckets) > enrollRateMaxEntries {
		for existing, bucket := range enrollRateBuckets {
			if now.Sub(bucket.windowStart) >= enrollRateWindow {
				delete(enrollRateBuckets, existing)
			}
		}
		if len(enrollRateBuckets) > enrollRateMaxEntries {
			enrollRateBuckets = make(map[string]enrollRateBucket)
		}
	}
	bucket := enrollRateBuckets[key]
	if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) >= enrollRateWindow {
		enrollRateBuckets[key] = enrollRateBucket{windowStart: now, count: 1}
		return true
	}
	if bucket.count >= limit {
		return false
	}
	bucket.count++
	enrollRateBuckets[key] = bucket
	return true
}

// resetEnrollRateLimiterForTest clears the limiter state.
func resetEnrollRateLimiterForTest() {
	enrollRateMu.Lock()
	enrollRateBuckets = make(map[string]enrollRateBucket)
	enrollRateMu.Unlock()
}

// enrollCIDRAllows reports whether ip falls inside any configured CIDR.
// An empty allowlist means "any address".
func enrollCIDRAllows(allowlist, ip string) bool {
	allowlist = strings.TrimSpace(allowlist)
	if allowlist == "" {
		return true
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, entry := range strings.Split(allowlist, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			if network.Contains(parsed) {
				return true
			}
			continue
		}
		if single := net.ParseIP(entry); single != nil && single.Equal(parsed) {
			return true
		}
	}
	return false
}

func responder(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"status": "error", "error": message})
}

func respondEnrollError(c *gin.Context, status int, err error) {
	responder(c, status, err.Error())
}

// subtleCompare is a tiny constant-time string equality check kept local so the
// enrollment path does not depend on crypto/subtle's argument-length behaviour.
func subtleCompare(a, b string) int {
	if len(a) != len(b) {
		return 0
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	if diff == 0 {
		return 1
	}
	return 0
}
