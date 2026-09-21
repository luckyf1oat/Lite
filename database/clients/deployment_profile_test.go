package clients

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var errInjectedResetClock = errors.New("injected reset clock failure")

func TestDeploymentProfileRuntimeConfigExcludesInstallationOnlyFields(t *testing.T) {
	profile := DeploymentProfile{
		Platform:                "linux",
		EnableRemoteControl:     true,
		DisableAutoUpdate:       true,
		IgnoreUnsafeCert:        true,
		GetIPAddrFromNIC:        true,
		EnableGHProxy:           true,
		GHProxy:                 "https://example.com",
		EnableCustomDir:         true,
		Dir:                     "/opt/custom-agent",
		EnableCustomServiceName: true,
		ServiceName:             "custom-agent",
		EnableInterval:          true,
		Interval:                15,
	}
	if err := normalizeDeploymentProfile(&profile); err != nil {
		t.Fatalf("normalizeDeploymentProfile() error = %v", err)
	}
	encoded, err := json.Marshal(profile.RuntimeConfig())
	if err != nil {
		t.Fatalf("marshal runtime config: %v", err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{
		"enable_remote_control",
		"enable_mcp",
		"disable_web_ssh",
		"disable_auto_update",
		"ignore_unsafe_cert",
		"get_ip_addr_from_nic",
		"ghproxy",
		"dir",
		"service_name",
	} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("runtime config contains installation-only field %q: %s", forbidden, payload)
		}
	}
	if !strings.Contains(payload, `"interval":15`) {
		t.Fatalf("runtime config is missing interval: %s", payload)
	}
}

func TestDeploymentProfileMigratesDisableWebSSH(t *testing.T) {
	var profile DeploymentProfile
	if err := json.Unmarshal([]byte(`{"platform":"linux","disable_web_ssh":true}`), &profile); err != nil {
		t.Fatalf("migrate closed remote control: %v", err)
	}
	if profile.EnableRemoteControl {
		t.Fatal("disable_web_ssh true must become enable_remote_control false")
	}
	if err := json.Unmarshal([]byte(`{"platform":"linux","disable_web_ssh":false}`), &profile); err != nil {
		t.Fatalf("migrate open remote control: %v", err)
	}
	if !profile.EnableRemoteControl {
		t.Fatal("disable_web_ssh false must become enable_remote_control true")
	}
	if err := json.Unmarshal([]byte(`{"platform":"linux","enable_remote_control":true,"disable_web_ssh":false}`), &profile); err == nil {
		t.Fatal("expected a config conflict when both keys are present")
	}
}

func TestNormalizeDeploymentProfileRejectsInvalidRuntimeValues(t *testing.T) {
	profile := DeploymentProfile{
		Platform:       "linux",
		EnableInterval: true,
		Interval:       0.5,
	}
	if err := normalizeDeploymentProfile(&profile); err == nil {
		t.Fatal("expected invalid interval to be rejected")
	}

	profile = DeploymentProfile{
		Platform:          "linux",
		EnableMonthRotate: true,
		MonthRotate:       32,
	}
	if err := normalizeDeploymentProfile(&profile); err == nil {
		t.Fatal("expected invalid month rotation day to be rejected")
	}

	profile = DeploymentProfile{
		Platform:          "linux",
		EnableMonthRotate: true,
		MonthRotate:       1,
		MonthRotateTime:   "25:00:00",
	}
	if err := normalizeDeploymentProfile(&profile); err == nil {
		t.Fatal("expected invalid month rotation time to be rejected")
	}

	profile = DeploymentProfile{
		Platform:            "linux",
		EnableMonthRotate:   true,
		MonthRotate:         1,
		MonthRotateTimezone: "Not/AZone",
	}
	if err := normalizeDeploymentProfile(&profile); err == nil {
		t.Fatal("expected invalid month rotation timezone to be rejected")
	}
}

func TestDeploymentProfileAndBillingEditorShareTrafficResetDay(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:deployment-profile-reset-day?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.ClientDeploymentProfile{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := db.Create(&models.Client{UUID: "node-a", Token: "token-a"}).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}

	profile := DeploymentProfile{
		Platform:          "linux",
		EnableMonthRotate: true,
		MonthRotate:       17,
	}
	if _, err := saveDeploymentProfile(db, "node-a", profile); err != nil {
		t.Fatalf("save deployment profile: %v", err)
	}
	var client models.Client
	if err := db.Select("uuid", "traffic_reset_day", "traffic_reset_time", "traffic_reset_timezone").First(&client, "uuid = ?", "node-a").Error; err != nil {
		t.Fatalf("read client: %v", err)
	}
	if client.TrafficResetDay == nil || *client.TrafficResetDay != 17 {
		t.Fatalf("billing reset day = %v, want 17", client.TrafficResetDay)
	}
	if client.TrafficResetTime != "00:00:00" || client.TrafficResetTimezone != "Asia/Shanghai" {
		t.Fatalf("billing reset clock = %s %s, want 00:00:00 Asia/Shanghai", client.TrafficResetTime, client.TrafficResetTimezone)
	}

	if err := saveClient(db, map[string]interface{}{
		"uuid":                   "node-a",
		"traffic_reset_day":      float64(9),
		"traffic_reset_time":     "12:38:12",
		"traffic_reset_timezone": "UTC",
	}); err != nil {
		t.Fatalf("save billing reset day: %v", err)
	}
	loaded, saved, err := getDeploymentProfile(db, "node-a")
	if err != nil {
		t.Fatalf("read deployment profile: %v", err)
	}
	if !saved || !loaded.EnableMonthRotate || loaded.MonthRotate != 9 {
		t.Fatalf("deployment reset day = enabled:%v day:%d, want enabled:true day:9", loaded.EnableMonthRotate, loaded.MonthRotate)
	}
	if loaded.MonthRotateTime != "12:38:12" || loaded.MonthRotateTimezone != "UTC" {
		t.Fatalf("deployment reset clock = %s %s, want 12:38:12 UTC", loaded.MonthRotateTime, loaded.MonthRotateTimezone)
	}
}

func TestAdoptDeploymentRuntimeConfigInitializesAndReconcilesDelivery(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:deployment-profile-agent-state?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.ClientDeploymentProfile{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := db.Create(&models.Client{UUID: "node-state", Token: "token-state"}).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}

	day, interval := 8, 12.0
	include, exclude, mounts := "eth0", "lo", "/;/data"
	memoryCache, gpu := true, true
	adopted, err := adoptDeploymentRuntimeConfig(db, "node-state", "linux", v2.ConfigParams{
		MonthRotate:        &day,
		Interval:           &interval,
		IncludeNics:        &include,
		ExcludeNics:        &exclude,
		IncludeMountpoints: &mounts,
		MemoryIncludeCache: &memoryCache,
		EnableGPU:          &gpu,
	})
	if err != nil {
		t.Fatalf("adopt runtime config: %v", err)
	}
	if !adopted {
		t.Fatal("first runtime config was not adopted")
	}
	profile, saved, err := getDeploymentProfile(db, "node-state")
	if err != nil {
		t.Fatalf("load adopted profile: %v", err)
	}
	if !saved || !profile.EnableMonthRotate || profile.MonthRotate != day ||
		!profile.EnableInterval || profile.Interval != interval ||
		!profile.EnableIncludeNics || profile.IncludeNics != include ||
		!profile.EnableExcludeNics || profile.ExcludeNics != exclude ||
		!profile.EnableIncludeMountpoints || profile.IncludeMountpoints != mounts ||
		!profile.MemoryIncludeCache || !profile.EnableGPU {
		t.Fatalf("incomplete adopted profile: %+v", profile)
	}

	staleInterval := 5.0
	adopted, err = adoptDeploymentRuntimeConfig(db, "node-state", "windows", v2.ConfigParams{Interval: &staleInterval})
	if err != nil {
		t.Fatalf("submit stale runtime config: %v", err)
	}
	if adopted {
		t.Fatal("stale runtime config overwrote a managed profile")
	}
	profile, _, err = getDeploymentProfile(db, "node-state")
	if err != nil {
		t.Fatalf("reload managed profile: %v", err)
	}
	if profile.Platform != "linux" || profile.Interval != interval {
		t.Fatalf("managed profile changed after stale report: %+v", profile)
	}

	if err := db.Create(&models.Client{UUID: "node-legacy-match", Token: "token-legacy-match"}).Error; err != nil {
		t.Fatalf("create matching legacy client: %v", err)
	}
	legacyMatch := DeploymentProfile{Platform: "linux", EnableInterval: true, Interval: 10}
	encodedMatch, err := json.Marshal(legacyMatch)
	if err != nil {
		t.Fatalf("encode matching legacy profile: %v", err)
	}
	if err := db.Create(&models.ClientDeploymentProfile{Client: "node-legacy-match", Config: string(encodedMatch)}).Error; err != nil {
		t.Fatalf("create matching legacy profile: %v", err)
	}
	matchingInterval := 10.0
	adopted, err = adoptDeploymentRuntimeConfig(db, "node-legacy-match", "linux", v2.ConfigParams{Interval: &matchingInterval})
	if err != nil || adopted {
		t.Fatalf("reconcile matching legacy profile = %v, %v", adopted, err)
	}
	var matchingState models.ClientDeploymentProfile
	if err := db.First(&matchingState, "client = ?", "node-legacy-match").Error; err != nil {
		t.Fatalf("load matching legacy state: %v", err)
	}
	if matchingState.Revision != 1 || matchingState.DeliveryStatus != DeploymentDeliveryApplied || matchingState.FinishedAt == nil {
		t.Fatalf("matching legacy delivery state = %+v", matchingState)
	}

	if err := db.Create(&models.Client{UUID: "node-legacy-mismatch", Token: "token-legacy-mismatch"}).Error; err != nil {
		t.Fatalf("create mismatching legacy client: %v", err)
	}
	legacyMismatch := DeploymentProfile{Platform: "linux", EnableInterval: true, Interval: 15}
	encodedMismatch, err := json.Marshal(legacyMismatch)
	if err != nil {
		t.Fatalf("encode mismatching legacy profile: %v", err)
	}
	if err := db.Create(&models.ClientDeploymentProfile{Client: "node-legacy-mismatch", Config: string(encodedMismatch)}).Error; err != nil {
		t.Fatalf("create mismatching legacy profile: %v", err)
	}
	reportedInterval := 20.0
	adopted, err = adoptDeploymentRuntimeConfig(db, "node-legacy-mismatch", "linux", v2.ConfigParams{Interval: &reportedInterval})
	if err != nil || adopted {
		t.Fatalf("reconcile mismatching legacy profile = %v, %v", adopted, err)
	}
	var mismatchingState models.ClientDeploymentProfile
	if err := db.First(&mismatchingState, "client = ?", "node-legacy-mismatch").Error; err != nil {
		t.Fatalf("load mismatching legacy state: %v", err)
	}
	if mismatchingState.Revision != 1 || mismatchingState.DeliveryStatus != DeploymentDeliverySaved || mismatchingState.FinishedAt != nil {
		t.Fatalf("mismatching legacy delivery state = %+v", mismatchingState)
	}
}

func TestDeploymentConfigDeliveryTracksOnlyRuntimeChangesAndRejectsStaleResults(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:deployment-profile-delivery?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.ClientDeploymentProfile{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := db.Create(&models.Client{UUID: "node-delivery", Token: "token-delivery"}).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}

	profile := DeploymentProfile{Platform: "linux", EnableInterval: true, Interval: 10}
	_, state, runtimeChanged, err := saveDeploymentProfileForDispatch(db, "node-delivery", profile)
	if err != nil {
		t.Fatalf("save initial profile: %v", err)
	}
	if !runtimeChanged || state.Revision != 1 || state.Status != DeploymentDeliverySaved {
		t.Fatalf("initial delivery state = %+v, runtimeChanged=%v", state, runtimeChanged)
	}
	if marked, err := markDeploymentConfigSent(db, "node-delivery", 1); err != nil || !marked {
		t.Fatalf("mark sent = %v, %v", marked, err)
	}
	if completed, err := completeDeploymentConfig(db, "node-delivery", v2.ConfigResultParams{
		Revision: 1,
		Status:   DeploymentDeliveryApplied,
	}); err != nil || !completed {
		t.Fatalf("complete revision 1 = %v, %v", completed, err)
	}

	profile.EnableRemoteControl = true
	_, state, runtimeChanged, err = saveDeploymentProfileForDispatch(db, "node-delivery", profile)
	if err != nil {
		t.Fatalf("save installation-only change: %v", err)
	}
	if runtimeChanged || state.Revision != 1 || state.Status != DeploymentDeliveryApplied {
		t.Fatalf("installation-only state = %+v, runtimeChanged=%v", state, runtimeChanged)
	}

	profile.Interval = 20
	_, state, runtimeChanged, err = saveDeploymentProfileForDispatch(db, "node-delivery", profile)
	if err != nil {
		t.Fatalf("save runtime change: %v", err)
	}
	if !runtimeChanged || state.Revision != 2 || state.Status != DeploymentDeliverySaved {
		t.Fatalf("second delivery state = %+v, runtimeChanged=%v", state, runtimeChanged)
	}
	if completed, err := completeDeploymentConfig(db, "node-delivery", v2.ConfigResultParams{
		Revision: 1,
		Status:   DeploymentDeliveryFailed,
		Error:    "stale result",
	}); err != nil || completed {
		t.Fatalf("stale result completion = %v, %v", completed, err)
	}
	if completed, err := completeDeploymentConfig(db, "node-delivery", v2.ConfigResultParams{
		Revision: 2,
		Status:   DeploymentDeliveryFailed,
		Error:    " invalid interface\n ",
	}); err != nil || !completed {
		t.Fatalf("complete revision 2 = %v, %v", completed, err)
	}

	_, state, runtimeChanged, err = saveDeploymentProfileForDispatch(db, "node-delivery", profile)
	if err != nil {
		t.Fatalf("retry failed profile: %v", err)
	}
	if !runtimeChanged || state.Revision != 3 || state.Status != DeploymentDeliverySaved {
		t.Fatalf("retry delivery state = %+v, runtimeChanged=%v", state, runtimeChanged)
	}
}

func TestSaveClientResetClockBumpsRevisionOnceAndLeavesUnchanged(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:deployment-reset-clock-revision?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.ClientDeploymentProfile{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	day := 15
	if err := db.Create(&models.Client{
		UUID: "node-reset", Token: "token-reset",
		TrafficResetDay: &day, TrafficResetTime: "00:00:00", TrafficResetTimezone: "UTC",
	}).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}
	profile := DeploymentProfile{
		Platform: "linux", EnableInterval: true, Interval: 9,
		EnableMonthRotate: true, MonthRotate: 15,
		MonthRotateTime: "00:00:00", MonthRotateTimezone: "UTC",
	}
	if _, state, changed, err := saveDeploymentProfileForDispatch(db, "node-reset", profile); err != nil {
		t.Fatalf("save initial profile: %v", err)
	} else if !changed || state.Revision != 1 {
		t.Fatalf("initial revision = %+v changed=%v", state, changed)
	}
	if marked, err := markDeploymentConfigSent(db, "node-reset", 1); err != nil || !marked {
		t.Fatalf("mark sent: %v %v", marked, err)
	}
	if completed, err := completeDeploymentConfig(db, "node-reset", v2.ConfigResultParams{
		Revision: 1, Status: DeploymentDeliveryApplied,
	}); err != nil || !completed {
		t.Fatalf("complete revision 1: %v %v", completed, err)
	}
	now := time.Now().UTC()
	if err := db.Model(&models.ClientDeploymentProfile{}).Where("client = ?", "node-reset").Updates(map[string]any{
		"revision": 7, "delivery_status": DeploymentDeliveryApplied, "finished_at": now,
	}).Error; err != nil {
		t.Fatalf("seed revision 7: %v", err)
	}

	dispatch, err := saveClientWithDispatch(db, map[string]interface{}{
		"uuid":               "node-reset",
		"traffic_reset_time": "12:38:12",
	}, "")
	if err != nil {
		t.Fatalf("change reset time: %v", err)
	}
	if !dispatch.RuntimeChanged || !dispatch.HasProfile || dispatch.Delivery.Revision != 8 {
		t.Fatalf("dispatch after time change = %+v", dispatch)
	}
	if dispatch.Config.Revision != 8 || dispatch.Config.MonthRotateTime == nil || *dispatch.Config.MonthRotateTime != "12:38:12" {
		t.Fatalf("runtime config = %+v", dispatch.Config)
	}
	if dispatch.Config.Interval == nil || *dispatch.Config.Interval != 9 {
		t.Fatalf("reset clock dispatch dropped interval: %+v", dispatch.Config)
	}

	pulled, err := runtimeConfigForAgent(db, "node-reset")
	if err != nil || pulled == nil {
		t.Fatalf("pull config: %v %#v", err, pulled)
	}
	if pulled.Revision != 8 || pulled.MonthRotateTime == nil || *pulled.MonthRotateTime != "12:38:12" {
		t.Fatalf("offline pull = %+v", pulled)
	}

	dispatch, err = saveClientWithDispatch(db, map[string]interface{}{
		"uuid":               "node-reset",
		"traffic_reset_time": "12:38:12",
	}, "")
	if err != nil {
		t.Fatalf("repeat identical time: %v", err)
	}
	if dispatch.RuntimeChanged || dispatch.Delivery.Revision != 8 {
		t.Fatalf("identical save bumped revision: %+v", dispatch)
	}

	dispatch, err = saveClientWithDispatch(db, map[string]interface{}{
		"uuid":                   "node-reset",
		"traffic_reset_timezone": "Asia/Shanghai",
	}, "")
	if err != nil {
		t.Fatalf("change timezone: %v", err)
	}
	if !dispatch.RuntimeChanged || dispatch.Delivery.Revision != 9 {
		t.Fatalf("timezone change = %+v", dispatch)
	}

	dispatch, err = saveClientWithDispatch(db, map[string]interface{}{
		"uuid":              "node-reset",
		"traffic_reset_day": float64(16),
	}, "")
	if err != nil {
		t.Fatalf("change day: %v", err)
	}
	if !dispatch.RuntimeChanged || dispatch.Delivery.Revision != 10 {
		t.Fatalf("day change = %+v", dispatch)
	}
}

func TestSaveClientResetClockKeepsClientAndRevisionAtomic(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:deployment-reset-clock-atomic?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.ClientDeploymentProfile{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	day := 15
	if err := db.Create(&models.Client{
		UUID: "node-atomic", Token: "token-atomic",
		TrafficResetDay: &day, TrafficResetTime: "00:00:00", TrafficResetTimezone: "UTC",
	}).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}
	profile := DeploymentProfile{Platform: "linux", EnableMonthRotate: true, MonthRotate: 15, MonthRotateTime: "00:00:00", MonthRotateTimezone: "UTC"}
	if _, _, _, err := saveDeploymentProfileForDispatch(db, "node-atomic", profile); err != nil {
		t.Fatalf("save profile: %v", err)
	}

	failNext := false
	if err := db.Callback().Update().Before("gorm:update").Register("lite_fail_reset_profile", func(tx *gorm.DB) {
		if failNext && tx.Statement != nil && tx.Statement.Table == "client_deployment_profiles" {
			_ = tx.AddError(errInjectedResetClock)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove("lite_fail_reset_profile")
	})

	failNext = true
	err = saveClient(db, map[string]interface{}{
		"uuid":               "node-atomic",
		"traffic_reset_time": "12:38:12",
	})
	if err == nil {
		t.Fatal("expected injected profile update failure")
	}

	var client models.Client
	if err := db.First(&client, "uuid = ?", "node-atomic").Error; err != nil {
		t.Fatalf("reload client: %v", err)
	}
	if client.TrafficResetTime != "00:00:00" {
		t.Fatalf("client time = %q, want rolled back 00:00:00", client.TrafficResetTime)
	}
	var stored models.ClientDeploymentProfile
	if err := db.First(&stored, "client = ?", "node-atomic").Error; err != nil {
		t.Fatalf("reload profile: %v", err)
	}
	if stored.Revision != 1 {
		t.Fatalf("revision = %d, want rolled back 1", stored.Revision)
	}
}
