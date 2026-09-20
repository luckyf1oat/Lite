package trafficledger

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPointer(value int) *int { return &value }

func TestCalibrationSnapshotUsesFrontendTrafficFieldNames(t *testing.T) {
	payload, err := json.Marshal(CalibrationSnapshot{
		Raw:       Usage{Up: 1, Down: 2},
		Effective: Usage{Up: 3, Down: 4},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"client":"",
		"cycle":"",
		"cycle_start":"0001-01-01T00:00:00Z",
		"cycle_end":"0001-01-01T00:00:00Z",
		"raw":{"up":1,"down":2},
		"adjustment":{"up":0,"down":0},
		"effective":{"up":3,"down":4},
		"history":null
	}`, string(payload))
}

func TestCurrentTrafficCycleClampsResetDayAtMonthEnd(t *testing.T) {
	resetDay := intPointer(31)
	start, cycle, err := CurrentTrafficCycle(resetDay, time.Date(2026, 2, 28, 12, 0, 0, 0, BeijingLocation))
	require.NoError(t, err)
	assert.Equal(t, "2026-02-28", cycle)
	assert.Equal(t, time.Date(2026, 2, 28, 0, 0, 0, 0, BeijingLocation), start)

	start, cycle, err = CurrentTrafficCycle(resetDay, time.Date(2026, 2, 27, 23, 59, 0, 0, BeijingLocation))
	require.NoError(t, err)
	assert.Equal(t, "2026-01-31", cycle)
	assert.Equal(t, time.Date(2026, 1, 31, 0, 0, 0, 0, BeijingLocation), start)
}

func TestNextCycleStartFollowsResetDay(t *testing.T) {
	assert.Equal(t,
		time.Date(2026, 9, 15, 0, 0, 0, 0, BeijingLocation),
		NextCycleStart(time.Date(2026, 8, 15, 0, 0, 0, 0, BeijingLocation), 15),
	)
	assert.Equal(t,
		time.Date(2026, 2, 28, 0, 0, 0, 0, BeijingLocation),
		NextCycleStart(time.Date(2026, 1, 31, 0, 0, 0, 0, BeijingLocation), 31),
	)
}

func TestTrafficCycleInclusiveEndUsesTheDayBeforeTheNextReset(t *testing.T) {
	assert.Equal(t,
		time.Date(2026, 2, 27, 0, 0, 0, 0, BeijingLocation),
		trafficCycleInclusiveEnd(time.Date(2026, 1, 31, 0, 0, 0, 0, BeijingLocation), 31),
	)
	assert.Equal(t,
		time.Date(2026, 3, 30, 0, 0, 0, 0, BeijingLocation),
		trafficCycleInclusiveEnd(time.Date(2026, 2, 28, 0, 0, 0, 0, BeijingLocation), 31),
	)
}

func TestCalibrationStopsApplyingAfterTheNextResetDay(t *testing.T) {
	resetDay := intPointer(15)
	assert.True(t, calibrationAppliesToCurrentCycle(resetDay, "2026-07-15", time.Date(2026, 8, 14, 23, 59, 0, 0, BeijingLocation)))
	assert.False(t, calibrationAppliesToCurrentCycle(resetDay, "2026-07-15", time.Date(2026, 8, 15, 0, 0, 0, 0, BeijingLocation)))
	assert.True(t, calibrationAppliesToCurrentCycle(resetDay, "2026-08-15", time.Date(2026, 8, 15, 0, 0, 0, 0, BeijingLocation)))
}

func TestAllocateNegativeCalibrationWalksBackwardWithoutNegativeDays(t *testing.T) {
	days := []calibrationDay{
		{Day: "2026-08-01", Effective: Usage{Up: 100}},
		{Day: "2026-08-02", Effective: Usage{Up: 70}},
		{Day: "2026-08-03", Effective: Usage{Up: 50}},
	}
	allocation, err := allocateCalibration(days, -120, func(day calibrationDay) int64 { return day.Effective.Up })
	require.NoError(t, err)
	assert.Equal(t, int64(-50), allocation["2026-08-03"])
	assert.Equal(t, int64(-70), allocation["2026-08-02"])
	assert.Zero(t, allocation["2026-08-01"])

	total := int64(0)
	for _, day := range days {
		value := addSignedNonNegative(day.Effective.Up, allocation[day.Day])
		assert.GreaterOrEqual(t, value, int64(0))
		total += value
	}
	assert.Equal(t, int64(100), total)
}

func TestAdjustedLedgerUsageKeepsDailyAndRangeTotalsConsistent(t *testing.T) {
	db := openLedgerTestDB(t, "calibration-ledger-consistency")
	rows := []models.TrafficDailyLedger{
		{Client: "client-a", Day: "2026-08-01", UpBytes: 100, DownBytes: 200},
		{Client: "client-a", Day: "2026-08-02", UpBytes: 300, DownBytes: 400},
	}
	require.NoError(t, db.Create(&rows).Error)
	adjustments := []models.TrafficCalibrationAdjustment{
		{CalibrationID: "a", Client: "client-a", Cycle: "2026-08-01", Day: "2026-08-01", UpDelta: 50, DownDelta: -25},
		{CalibrationID: "b", Client: "client-a", Cycle: "2026-08-01", Day: "2026-08-02", UpDelta: -75, DownDelta: 100},
	}
	require.NoError(t, db.Create(&adjustments).Error)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, BeijingLocation)
	middle := start.AddDate(0, 0, 1)
	end := start.AddDate(0, 0, 2)

	first, err := AdjustedLedgerUsage(context.Background(), db, "client-a", start, middle)
	require.NoError(t, err)
	second, err := AdjustedLedgerUsage(context.Background(), db, "client-a", middle, end)
	require.NoError(t, err)
	total, err := AdjustedLedgerUsage(context.Background(), db, "client-a", start, end)
	require.NoError(t, err)
	assert.Equal(t, Usage{Up: first.Up + second.Up, Down: first.Down + second.Down}, total)
	assert.Equal(t, Usage{Up: 375, Down: 675}, total)
}

func TestShiftCumulativeCounterMakesNewestPointExact(t *testing.T) {
	assert.Equal(t, int64(180), ShiftCumulativeCounter(130, 150, 200))
	assert.Equal(t, int64(0), ShiftCumulativeCounter(20, 100, 50))
	assert.Equal(t, int64(50), ShiftCumulativeCounter(100, 100, 50))
}

func TestCalibrationDaysIncludeBeijingTodayWhenVendorResetIsLaterTheSameDay(t *testing.T) {
	now := time.Date(2026, 9, 21, 14, 0, 0, 0, time.UTC)
	today := BeijingDay(now)
	cycleStart := time.Date(2026, 9, 21, 12, 38, 12, 0, time.UTC)
	require.True(t, cycleStart.After(today), "vendor reset after Beijing midnight is the production panic condition")

	old := map[string]*calibrationDay{}
	for day := cycleStart; !day.After(today); day = day.AddDate(0, 0, 1) {
		old[dayKey(day)] = &calibrationDay{Day: dayKey(day)}
	}
	require.Nil(t, old[dayKey(today)], "the previous AddDate loop skipped Beijing today")

	daysByKey, dayKeys := calibrationDaysByKey(cycleStart, today)
	require.NotNil(t, daysByKey[dayKey(today)])
	require.NotPanics(t, func() {
		applyCurrentDayUsage(daysByKey, dayKeys, today, Usage{Up: 7, Down: 9})
	})
	assert.Equal(t, int64(7), daysByKey[dayKey(today)].Raw.Up)
}

func TestApplyCurrentDayUsageCreatesMissingBeijingTodaySlot(t *testing.T) {
	today := BeijingDay(time.Date(2026, 9, 21, 2, 52, 0, 0, BeijingLocation))
	daysByKey := map[string]*calibrationDay{}
	require.NotPanics(t, func() {
		applyCurrentDayUsage(daysByKey, nil, today, Usage{Up: 3})
	})
	require.NotNil(t, daysByKey[dayKey(today)])
	assert.Equal(t, int64(3), daysByKey[dayKey(today)].Raw.Up)
}

func TestCurrentTrafficCycleForEmptyResetClockAndTimezone(t *testing.T) {
	day := 15
	now := time.Date(2026, 9, 21, 2, 52, 0, 0, BeijingLocation)
	start, cycle, err := CurrentTrafficCycleFor(models.Client{
		UUID:                 "node",
		TrafficResetDay:      &day,
		TrafficResetTime:     "",
		TrafficResetTimezone: "",
	}, now)
	require.NoError(t, err)
	assert.Equal(t, "2026-09-15", cycle)
	assert.True(t, start.Equal(time.Date(2026, 9, 15, 0, 0, 0, 0, BeijingLocation)))
}

func TestCurrentCalibratedCycleUsagesEmptyResetClockDoesNotPanic(t *testing.T) {
	db := openLedgerTestDB(t, "calibration-empty-reset-clock")
	InvalidateCalibratedCycleCache()
	t.Cleanup(InvalidateCalibratedCycleCache)

	day := 21
	require.NoError(t, db.Model(&models.Client{}).Where("uuid = ?", "client-a").Updates(map[string]any{
		"traffic_reset_day":      day,
		"traffic_reset_time":     "",
		"traffic_reset_timezone": "",
	}).Error)

	now := time.Date(2026, 9, 21, 14, 0, 0, 0, time.UTC)
	_, cycle, err := CurrentTrafficCycleFor(models.Client{
		UUID:                 "client-a",
		TrafficResetDay:      &day,
		TrafficResetTime:     "",
		TrafficResetTimezone: "",
	}, now)
	require.NoError(t, err)
	require.NoError(t, db.Create(&models.TrafficCalibrationAdjustment{
		CalibrationID: "cal-empty",
		Client:        "client-a",
		Cycle:         cycle,
		Day:           "2026-09-21",
		UpDelta:       1,
	}).Error)

	var usages map[string]Usage
	require.NotPanics(t, func() {
		var loadErr error
		usages, loadErr = CurrentCalibratedCycleUsages(context.Background(), db, now)
		require.NoError(t, loadErr)
	})
	assert.NotNil(t, usages)
}

func TestCycleEndDayDoesNotDereferenceNilResetDay(t *testing.T) {
	start := time.Date(2026, 9, 15, 0, 0, 0, 0, BeijingLocation)
	require.NotPanics(t, func() {
		end := cycleEndDay(models.Client{}, start)
		assert.False(t, end.IsZero())
	})
}
