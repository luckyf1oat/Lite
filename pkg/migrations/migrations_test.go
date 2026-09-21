package migrations

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	appconfig "github.com/nuomiiiii/lite/pkg/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()

	dsn := "file:" + strings.ReplaceAll(name, " ", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlite test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	return db
}

func TestMigrateTrafficResetDayFromTags(t *testing.T) {
	db := openTestDB(t, "migrations_traffic_reset_day")
	if err := db.AutoMigrate(&models.Client{}); err != nil {
		t.Fatalf("migrate client table: %v", err)
	}
	managedDay := 5
	clients := []models.Client{
		{UUID: "legacy", Token: "token-legacy", Tags: "Premium<blue>;<TRD:26>"},
		{UUID: "invalid", Token: "token-invalid", Tags: "<TRD:32>"},
		{UUID: "managed", Token: "token-managed", Tags: "<TRD:26>", TrafficResetDay: &managedDay},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatalf("seed clients: %v", err)
	}

	if err := MigrateTrafficResetDayFromTags(db); err != nil {
		t.Fatalf("migrate traffic reset days: %v", err)
	}

	var legacy, invalid, managed models.Client
	if err := db.First(&legacy, "uuid = ?", "legacy").Error; err != nil {
		t.Fatal(err)
	}
	if legacy.TrafficResetDay == nil || *legacy.TrafficResetDay != 26 {
		t.Fatalf("legacy reset day = %v, want 26", legacy.TrafficResetDay)
	}
	if err := db.First(&invalid, "uuid = ?", "invalid").Error; err != nil {
		t.Fatal(err)
	}
	if invalid.TrafficResetDay != nil {
		t.Fatalf("invalid reset day = %v, want nil", *invalid.TrafficResetDay)
	}
	if err := db.First(&managed, "uuid = ?", "managed").Error; err != nil {
		t.Fatal(err)
	}
	if managed.TrafficResetDay == nil || *managed.TrafficResetDay != managedDay {
		t.Fatalf("managed reset day = %v, want %d", managed.TrafficResetDay, managedDay)
	}
}

func TestHasLegacyConfigTable(t *testing.T) {
	t.Run("config item table", func(t *testing.T) {
		db := openTestDB(t, "migrations_config_item")
		if err := db.AutoMigrate(&appconfig.ConfigItem{}); err != nil {
			t.Fatalf("migrate config item table: %v", err)
		}
		if hasLegacyConfigTable(db) {
			t.Fatal("config item table was detected as legacy config table")
		}
	})

	t.Run("legacy config table", func(t *testing.T) {
		db := openTestDB(t, "migrations_legacy_config")
		if err := db.AutoMigrate(&legacyModelConfig{}); err != nil {
			t.Fatalf("migrate legacy config table: %v", err)
		}
		if !hasLegacyConfigTable(db) {
			t.Fatal("legacy config table was not detected")
		}
	})
}

func TestRunSkipsLegacyConfigMigrationForCurrentConfigItemTable(t *testing.T) {
	db := openTestDB(t, "migrations_config_item_run")
	if err := db.AutoMigrate(&appconfig.ConfigItem{}); err != nil {
		t.Fatalf("migrate config item table: %v", err)
	}
	if err := db.Create(&appconfig.ConfigItem{Key: "o_auth_provider", Value: `"github"`}).Error; err != nil {
		t.Fatalf("seed config item: %v", err)
	}

	if err := Run(Context{DB: db}); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	if db.Migrator().HasColumn(&legacyModelConfig{}, "id") {
		t.Fatal("config item table was changed into the legacy config shape")
	}
	if db.Migrator().HasTable(&models.OidcProvider{}) {
		t.Fatal("legacy OIDC migration ran against the config item table")
	}

	var item appconfig.ConfigItem
	if err := db.First(&item, "key = ?", "o_auth_provider").Error; err != nil {
		t.Fatalf("config item was not preserved: %v", err)
	}
	if item.Value != `"github"` {
		t.Fatalf("unexpected config value: %s", item.Value)
	}
}

func TestRunRemovesDeprecatedMetricRetentionConfig(t *testing.T) {
	db := openTestDB(t, "migrations_remove_metric_retention")
	if err := db.AutoMigrate(&appconfig.ConfigItem{}); err != nil {
		t.Fatalf("migrate config item table: %v", err)
	}
	if err := db.Create(&appconfig.ConfigItem{Key: "metric_retention_days", Value: "90"}).Error; err != nil {
		t.Fatalf("seed deprecated config: %v", err)
	}
	if err := Run(Context{DB: db}); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	var count int64
	if err := db.Model(&appconfig.ConfigItem{}).Where("key = ?", "metric_retention_days").Count(&count).Error; err != nil {
		t.Fatalf("count deprecated config: %v", err)
	}
	if count != 0 {
		t.Fatalf("deprecated metric retention config was not removed: %d", count)
	}
}

func TestRunRemovesCompatibilityConfig(t *testing.T) {
	db := openTestDB(t, "migrations_remove_compatibility_config")
	if err := db.AutoMigrate(&appconfig.ConfigItem{}); err != nil {
		t.Fatalf("migrate config item table: %v", err)
	}
	for _, key := range []string{"nezha_compat_enabled", "nezha_compat_listen"} {
		if err := db.Create(&appconfig.ConfigItem{Key: key, Value: "true"}).Error; err != nil {
			t.Fatalf("seed removed config %q: %v", key, err)
		}
	}
	if err := db.Create(&appconfig.ConfigItem{Key: "sitename", Value: `"Komari"`}).Error; err != nil {
		t.Fatalf("seed retained config: %v", err)
	}

	if err := Run(Context{DB: db}); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	var count int64
	if err := db.Model(&appconfig.ConfigItem{}).Where("key IN ?", []string{
		"nezha_compat_enabled",
		"nezha_compat_listen",
	}).Count(&count).Error; err != nil {
		t.Fatalf("count removed config: %v", err)
	}
	if count != 0 {
		t.Fatalf("removed compatibility config remains: %d", count)
	}
	if err := db.First(&appconfig.ConfigItem{}, "key = ?", "sitename").Error; err != nil {
		t.Fatalf("unrelated config was removed: %v", err)
	}
}

func TestRunPreservesVersion120RuntimeShape(t *testing.T) {
	db := openTestDB(t, "migrations_v120_runtime_shape")
	if err := db.AutoMigrate(
		&appconfig.ConfigItem{},
		&models.OidcProvider{},
		&models.MessageSenderProvider{},
		&models.Client{},
		&models.PingTask{},
	); err != nil {
		t.Fatalf("migrate 1.2.0 runtime shape: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&models.Client{UUID: "client-a", Token: "token-a", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	if err := db.Create(&models.PingTask{
		Name:      "already explicit",
		Clients:   models.StringArray{"client-a"},
		DefaultOn: true,
		Type:      "icmp",
		Target:    "example.com",
		Interval:  60,
	}).Error; err != nil {
		t.Fatalf("seed ping task: %v", err)
	}
	if err := db.Create(&appconfig.ConfigItem{Key: appconfig.OAuthProviderKey, Value: `"github"`}).Error; err != nil {
		t.Fatalf("seed config item: %v", err)
	}
	if err := db.Create(&models.OidcProvider{Name: "github", Addition: `{"client_id":"old","client_secret":"secret"}`}).Error; err != nil {
		t.Fatalf("seed oidc provider: %v", err)
	}
	if err := db.Create(&models.MessageSenderProvider{Name: "telegram", Addition: `{"bot_token":"old-token"}`}).Error; err != nil {
		t.Fatalf("seed message sender provider: %v", err)
	}

	if err := Run(Context{DB: db}); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	if db.Migrator().HasColumn(&legacyModelConfig{}, "sitename") {
		t.Fatal("current config item table was treated as legacy wide config")
	}

	var configItem appconfig.ConfigItem
	if err := db.First(&configItem, "key = ?", appconfig.OAuthProviderKey).Error; err != nil {
		t.Fatalf("find config item: %v", err)
	}
	if configItem.Value != `"github"` {
		t.Fatalf("unexpected config item value: %s", configItem.Value)
	}

	var oidc models.OidcProvider
	if err := db.First(&oidc, "name = ?", "github").Error; err != nil {
		t.Fatalf("find oidc provider: %v", err)
	}
	if oidc.Addition != `{"client_id":"old","client_secret":"secret"}` {
		t.Fatalf("oidc provider was unexpectedly changed: %s", oidc.Addition)
	}

	var sender models.MessageSenderProvider
	if err := db.First(&sender, "name = ?", "telegram").Error; err != nil {
		t.Fatalf("find message sender provider: %v", err)
	}
	if sender.Addition != `{"bot_token":"old-token"}` {
		t.Fatalf("message sender provider was unexpectedly changed: %s", sender.Addition)
	}

	var task models.PingTask
	if err := db.First(&task, "name = ?", "already explicit").Error; err != nil {
		t.Fatalf("find ping task: %v", err)
	}
	if len(task.Clients) != 1 || task.Clients[0] != "client-a" {
		t.Fatalf("explicit ping task clients were changed: %v", task.Clients)
	}
}

func TestRunMigratesLegacyConfigTableToConfigItems(t *testing.T) {
	db := openTestDB(t, "migrations_legacy_config_to_items")
	if err := db.AutoMigrate(&legacyModelConfig{}); err != nil {
		t.Fatalf("migrate legacy config table: %v", err)
	}
	legacy := legacyModelConfig{
		Sitename:                   "Old Lite",
		Description:                "legacy description",
		Theme:                      "classic",
		GeoIpEnabled:               true,
		GeoIpProvider:              "ip-api",
		OAuthProvider:              "github",
		NotificationMethod:         "none",
		TrafficLimitPercentage:     66.5,
		ExpireNotificationLeadDays: 3,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy config: %v", err)
	}

	if err := Run(Context{DB: db}); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	if db.Migrator().HasColumn(&legacyModelConfig{}, "sitename") {
		t.Fatal("legacy config columns were not removed")
	}

	var sitename appconfig.ConfigItem
	if err := db.First(&sitename, "key = ?", appconfig.SitenameKey).Error; err != nil {
		t.Fatalf("find migrated sitename: %v", err)
	}
	if sitename.Value != `"Old Lite"` {
		t.Fatalf("unexpected sitename value: %s", sitename.Value)
	}

	var corsOriginCheck appconfig.ConfigItem
	if err := db.First(&corsOriginCheck, "key = ?", appconfig.CorsOriginCheckEnabledKey).Error; err == nil {
		t.Fatalf("unexpected migrated cors_origin_check_enabled value: %s", corsOriginCheck.Value)
	}
}

func TestRunExpandsLegacyPingAllClientsTasks(t *testing.T) {
	db := openTestDB(t, "migrations_ping_all_clients")
	if err := db.AutoMigrate(&models.Client{}); err != nil {
		t.Fatalf("migrate clients: %v", err)
	}
	if err := db.Exec(`
		CREATE TABLE ping_tasks (
			id integer primary key autoincrement,
			weight integer not null default 0,
			name varchar(255) not null,
			all_clients boolean not null default false,
			type varchar(12) not null default 'icmp',
			target varchar(255) not null,
			interval integer not null default 60
		)
	`).Error; err != nil {
		t.Fatalf("create legacy ping_tasks: %v", err)
	}
	now := time.Now().UTC()
	clients := []models.Client{
		{UUID: "client-a", Token: "token-a", CreatedAt: now, UpdatedAt: now},
		{UUID: "client-b", Token: "token-b", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatalf("seed clients: %v", err)
	}
	if err := db.Exec("INSERT INTO ping_tasks (name, all_clients, type, target, interval) VALUES (?, ?, ?, ?, ?)", "legacy task", true, "icmp", "example.com", 60).Error; err != nil {
		t.Fatalf("seed legacy ping task: %v", err)
	}

	if err := Run(Context{DB: db}); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	var task models.PingTask
	if err := db.First(&task).Error; err != nil {
		t.Fatalf("find migrated ping task: %v", err)
	}
	if len(task.Clients) != 2 {
		t.Fatalf("expected two migrated clients, got %v", task.Clients)
	}
	got := map[string]bool{}
	for _, uuid := range task.Clients {
		got[uuid] = true
	}
	if !got["client-a"] || !got["client-b"] {
		t.Fatalf("unexpected migrated clients: %v", task.Clients)
	}

	raw, err := json.Marshal(task.Clients)
	if err != nil {
		t.Fatalf("marshal migrated clients: %v", err)
	}
	if string(raw) != `["client-a","client-b"]` {
		t.Fatalf("unexpected clients json: %s", raw)
	}
}

func TestMigrateLegacyCustomTrafficCycleKeysRemapsCurrentCycle(t *testing.T) {
	db := openTestDB(t, "migrations_custom_cycle_key")
	if err := db.AutoMigrate(&models.Client{}, &models.TrafficCalibrationAdjustment{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	day := 15
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	clients := []models.Client{
		{UUID: "custom-current", Token: "token-custom", TrafficResetDay: &day, TrafficResetTime: "12:38:12", TrafficResetTimezone: "UTC", TrafficResetAllowance: 50, TrafficResetCycle: "2026-09-15"},
		{UUID: "custom-stale", Token: "token-stale", TrafficResetDay: &day, TrafficResetTime: "12:38:12", TrafficResetTimezone: "UTC", TrafficResetAllowance: 50, TrafficResetCycle: "2026-08-15"},
		{UUID: "beijing-default", Token: "token-beijing", TrafficResetDay: &day, TrafficResetAllowance: 50, TrafficResetCycle: "2026-09-15"},
		{UUID: "precise-old", Token: "token-precise", TrafficResetDay: &day, TrafficResetTime: "12:38:12", TrafficResetTimezone: "UTC", TrafficResetAllowance: 50, TrafficResetCycle: "2026-09-15T00:00:00Z"},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatalf("seed clients: %v", err)
	}
	if err := db.Create(&models.TrafficCalibrationAdjustment{
		CalibrationID: "cal-1", Client: "custom-current", Cycle: "2026-09-15", Day: "2026-09-16", UpDelta: 1,
	}).Error; err != nil {
		t.Fatalf("seed calibration: %v", err)
	}

	if err := MigrateLegacyCustomTrafficCycleKeys(db, now); err != nil {
		t.Fatalf("migrate custom cycle keys: %v", err)
	}

	var remapped models.Client
	if err := db.First(&remapped, "uuid = ?", "custom-current").Error; err != nil {
		t.Fatalf("load remapped client: %v", err)
	}
	if remapped.TrafficResetCycle != "2026-09-15T12:38:12Z" || remapped.TrafficResetAllowance != 50 {
		t.Fatalf("current custom cycle = %q allowance %d", remapped.TrafficResetCycle, remapped.TrafficResetAllowance)
	}
	var adjustment models.TrafficCalibrationAdjustment
	if err := db.First(&adjustment, "client = ?", "custom-current").Error; err != nil {
		t.Fatalf("load remapped calibration: %v", err)
	}
	if adjustment.Cycle != "2026-09-15T12:38:12Z" {
		t.Fatalf("calibration cycle = %q", adjustment.Cycle)
	}

	var stale models.Client
	if err := db.First(&stale, "uuid = ?", "custom-stale").Error; err != nil {
		t.Fatalf("load stale client: %v", err)
	}
	if stale.TrafficResetCycle != "2026-08-15" {
		t.Fatalf("stale custom cycle should wait for expire, got %q", stale.TrafficResetCycle)
	}

	var beijing models.Client
	if err := db.First(&beijing, "uuid = ?", "beijing-default").Error; err != nil {
		t.Fatalf("load beijing client: %v", err)
	}
	if beijing.TrafficResetCycle != "2026-09-15" {
		t.Fatalf("default Beijing cycle = %q", beijing.TrafficResetCycle)
	}

	var precise models.Client
	if err := db.First(&precise, "uuid = ?", "precise-old").Error; err != nil {
		t.Fatalf("load precise client: %v", err)
	}
	if precise.TrafficResetCycle != "2026-09-15T00:00:00Z" {
		t.Fatalf("previous precise key should stay until expire, got %q", precise.TrafficResetCycle)
	}
}

func TestMigrateLegacyCustomTrafficCycleKeysRemapsCalibrationWithoutAllowance(t *testing.T) {
	db := openTestDB(t, "migrations_custom_cycle_key_calibration_only")
	if err := db.AutoMigrate(&models.Client{}, &models.TrafficCalibrationAdjustment{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	day := 15
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&models.Client{
		UUID: "cal-only", Token: "token-cal-only", TrafficResetDay: &day,
		TrafficResetTime: "12:38:12", TrafficResetTimezone: "UTC",
	}).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	if err := db.Create(&models.TrafficCalibrationAdjustment{
		CalibrationID: "cal-only", Client: "cal-only", Cycle: "2026-09-15", Day: "2026-09-16", UpDelta: 3,
	}).Error; err != nil {
		t.Fatalf("seed calibration: %v", err)
	}

	if err := MigrateLegacyCustomTrafficCycleKeys(db, now); err != nil {
		t.Fatalf("migrate custom cycle keys: %v", err)
	}

	var client models.Client
	if err := db.First(&client, "uuid = ?", "cal-only").Error; err != nil {
		t.Fatalf("load client: %v", err)
	}
	if client.TrafficResetCycle != "" || client.TrafficResetAllowance != 0 {
		t.Fatalf("calibration-only client cycle = %q allowance %d", client.TrafficResetCycle, client.TrafficResetAllowance)
	}
	var adjustment models.TrafficCalibrationAdjustment
	if err := db.First(&adjustment, "client = ?", "cal-only").Error; err != nil {
		t.Fatalf("load calibration: %v", err)
	}
	if adjustment.Cycle != "2026-09-15T12:38:12Z" {
		t.Fatalf("calibration cycle = %q", adjustment.Cycle)
	}
}

func TestMigrateLegacyCustomTrafficCycleKeysRollsBackWhenCalibrationUpdateFails(t *testing.T) {
	db := openTestDB(t, "migrations_custom_cycle_key_rollback")
	if err := db.AutoMigrate(&models.Client{}, &models.TrafficCalibrationAdjustment{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	day := 15
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&models.Client{
		UUID: "rollback", Token: "token-rollback", TrafficResetDay: &day,
		TrafficResetTime: "12:38:12", TrafficResetTimezone: "UTC",
		TrafficResetAllowance: 50, TrafficResetCycle: "2026-09-15",
	}).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	if err := db.Create(&models.TrafficCalibrationAdjustment{
		CalibrationID: "cal-rollback", Client: "rollback", Cycle: "2026-09-15", Day: "2026-09-16", UpDelta: 1,
	}).Error; err != nil {
		t.Fatalf("seed calibration: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_cal_update BEFORE UPDATE ON traffic_calibration_adjustments
BEGIN
	SELECT RAISE(ABORT, 'forced calibration remap failure');
END;`).Error; err != nil {
		t.Fatalf("create failing trigger: %v", err)
	}

	if err := MigrateLegacyCustomTrafficCycleKeys(db, now); err == nil {
		t.Fatal("expected calibration remap failure")
	}

	var client models.Client
	if err := db.First(&client, "uuid = ?", "rollback").Error; err != nil {
		t.Fatalf("load client: %v", err)
	}
	if client.TrafficResetCycle != "2026-09-15" || client.TrafficResetAllowance != 50 {
		t.Fatalf("rolled back client cycle = %q allowance %d", client.TrafficResetCycle, client.TrafficResetAllowance)
	}
	var adjustment models.TrafficCalibrationAdjustment
	if err := db.First(&adjustment, "client = ?", "rollback").Error; err != nil {
		t.Fatalf("load calibration: %v", err)
	}
	if adjustment.Cycle != "2026-09-15" {
		t.Fatalf("rolled back calibration cycle = %q", adjustment.Cycle)
	}
}

type lite223Client struct {
	UUID                  string `gorm:"type:varchar(36);primaryKey"`
	Token                 string `gorm:"type:varchar(255);unique;not null"`
	Tags                  string `gorm:"type:text"`
	TrafficResetDay       *int
	TrafficResetAllowance int64  `gorm:"not null;default:0"`
	TrafficResetCycle     string `gorm:"type:varchar(10);not null;default:''"`
}

func (lite223Client) TableName() string { return "clients" }

type lite223Calibration struct {
	ID            uint64 `gorm:"primaryKey"`
	CalibrationID string `gorm:"type:varchar(32)"`
	Client        string
	Cycle         string `gorm:"type:varchar(10)"`
	Day           string `gorm:"type:varchar(10)"`
	UpDelta       int64
	DownDelta     int64
	TargetUp      int64
	TargetDown    int64
}

func (lite223Calibration) TableName() string { return "traffic_calibration_adjustments" }

func TestUpgradeFromLite223KeepsBeijingCycleAndCalibration(t *testing.T) {
	db := openTestDB(t, "migrations_lite_223_cycle")
	if err := db.AutoMigrate(&lite223Client{}, &lite223Calibration{}); err != nil {
		t.Fatalf("create 2.2.3 tables: %v", err)
	}
	day := 15
	if err := db.Create(&lite223Client{
		UUID: "node-223", Token: "token-223", TrafficResetDay: &day,
		TrafficResetAllowance: 50, TrafficResetCycle: "2026-09-15",
	}).Error; err != nil {
		t.Fatalf("seed 2.2.3 client: %v", err)
	}
	if err := db.Create(&lite223Calibration{
		CalibrationID: "cal-223", Client: "node-223", Cycle: "2026-09-15", Day: "2026-09-16", UpDelta: 1,
	}).Error; err != nil {
		t.Fatalf("seed 2.2.3 calibration: %v", err)
	}

	if err := db.AutoMigrate(&models.Client{}, &models.TrafficCalibrationAdjustment{}); err != nil {
		t.Fatalf("upgrade tables: %v", err)
	}
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	if err := MigrateLegacyCustomTrafficCycleKeys(db, now); err != nil {
		t.Fatalf("migrate after 2.2.3 upgrade: %v", err)
	}

	var client models.Client
	if err := db.First(&client, "uuid = ?", "node-223").Error; err != nil {
		t.Fatalf("load upgraded client: %v", err)
	}
	if client.TrafficResetCycle != "2026-09-15" || client.TrafficResetAllowance != 50 {
		t.Fatalf("2.2.3 cycle = %q allowance %d", client.TrafficResetCycle, client.TrafficResetAllowance)
	}
	if client.TrafficResetTime != "00:00:00" && client.TrafficResetTime != "" {
		t.Fatalf("2.2.3 reset time = %q, want default midnight", client.TrafficResetTime)
	}

	var adjustment models.TrafficCalibrationAdjustment
	if err := db.First(&adjustment, "client = ?", "node-223").Error; err != nil {
		t.Fatalf("load upgraded calibration: %v", err)
	}
	if adjustment.Cycle != "2026-09-15" {
		t.Fatalf("2.2.3 calibration cycle = %q", adjustment.Cycle)
	}
}

type upstreamClient struct {
	UUID  string `gorm:"type:varchar(36);primaryKey"`
	Token string `gorm:"type:varchar(255);unique;not null"`
	Tags  string `gorm:"type:text"`
}

func (upstreamClient) TableName() string { return "clients" }

func TestUpgradeFromUpstreamAdoptsTagResetDayWithoutRemappingCycle(t *testing.T) {
	db := openTestDB(t, "migrations_upstream_cycle")
	if err := db.AutoMigrate(&upstreamClient{}); err != nil {
		t.Fatalf("create upstream client table: %v", err)
	}
	if err := db.Create(&upstreamClient{
		UUID: "upstream-node", Token: "token-upstream", Tags: "Premium<blue>;<TRD:26>",
	}).Error; err != nil {
		t.Fatalf("seed upstream client: %v", err)
	}
	if err := db.AutoMigrate(&models.Client{}); err != nil {
		t.Fatalf("upgrade client table: %v", err)
	}
	if err := MigrateTrafficResetDayFromTags(db); err != nil {
		t.Fatalf("adopt upstream reset day: %v", err)
	}
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	if err := MigrateLegacyCustomTrafficCycleKeys(db, now); err != nil {
		t.Fatalf("migrate after upstream upgrade: %v", err)
	}

	var client models.Client
	if err := db.First(&client, "uuid = ?", "upstream-node").Error; err != nil {
		t.Fatalf("load upgraded upstream client: %v", err)
	}
	if client.TrafficResetDay == nil || *client.TrafficResetDay != 26 {
		t.Fatalf("upstream reset day = %v, want 26", client.TrafficResetDay)
	}
	if client.TrafficResetCycle != "" {
		t.Fatalf("upstream cycle should stay empty, got %q", client.TrafficResetCycle)
	}
	if client.TrafficResetAllowance != 0 {
		t.Fatalf("upstream allowance = %d, want 0", client.TrafficResetAllowance)
	}
}
