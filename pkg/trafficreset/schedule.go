package trafficreset

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
)

const (
	DefaultTimezone = "Asia/Shanghai"
	DefaultTime     = "00:00:00"
)

// Schedule is a monthly traffic-reset instant: day-of-month plus clock in an IANA zone.
type Schedule struct {
	Day      int
	Hour     int
	Minute   int
	Second   int
	Timezone string
}

func Enabled(day *int) bool {
	return day != nil && *day >= 1 && *day <= 31
}

func ParseClock(value string) (hour, minute, second int, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, 0, nil
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("traffic reset time must be HH:MM or HH:MM:SS")
	}
	if _, err = fmt.Sscanf(parts[0], "%d", &hour); err != nil {
		return 0, 0, 0, fmt.Errorf("traffic reset time must be HH:MM or HH:MM:SS")
	}
	if _, err = fmt.Sscanf(parts[1], "%d", &minute); err != nil {
		return 0, 0, 0, fmt.Errorf("traffic reset time must be HH:MM or HH:MM:SS")
	}
	if len(parts) == 3 {
		if _, err = fmt.Sscanf(parts[2], "%d", &second); err != nil {
			return 0, 0, 0, fmt.Errorf("traffic reset time must be HH:MM or HH:MM:SS")
		}
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 || second < 0 || second > 59 {
		return 0, 0, 0, fmt.Errorf("traffic reset time is out of range")
	}
	return hour, minute, second, nil
}

func FormatClock(hour, minute, second int) string {
	return fmt.Sprintf("%02d:%02d:%02d", hour, minute, second)
}

func NormalizeClock(value string) (string, error) {
	hour, minute, second, err := ParseClock(value)
	if err != nil {
		return "", err
	}
	return FormatClock(hour, minute, second), nil
}

func NormalizeTimezone(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultTimezone, nil
	}
	if _, err := time.LoadLocation(value); err != nil {
		return "", fmt.Errorf("unknown traffic reset timezone %q", value)
	}
	return value, nil
}

func Location(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" || name == DefaultTimezone || name == "Asia/Chongqing" || name == "PRC" {
		return time.FixedZone(DefaultTimezone, 8*60*60)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone(DefaultTimezone, 8*60*60)
	}
	return loc
}

func FromFields(day *int, clock, timezone string) Schedule {
	hour, minute, second, err := ParseClock(clock)
	if err != nil {
		hour, minute, second = 0, 0, 0
	}
	tz, err := NormalizeTimezone(timezone)
	if err != nil {
		tz = DefaultTimezone
	}
	resolved := 0
	if day != nil {
		resolved = *day
	}
	return Schedule{
		Day:      resolved,
		Hour:     hour,
		Minute:   minute,
		Second:   second,
		Timezone: tz,
	}
}

func nextYearMonth(year int, month time.Month) (int, time.Month) {
	if month == 12 {
		return year + 1, 1
	}
	return year, month + 1
}

func previousYearMonth(year int, month time.Month) (int, time.Month) {
	if month == 1 {
		return year - 1, 12
	}
	return year, month - 1
}

func (s Schedule) loc() *time.Location {
	return Location(s.Timezone)
}

func (s Schedule) Active() bool {
	return s.Day >= 1 && s.Day <= 31
}

func daysInMonth(year int, month time.Month, loc *time.Location) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
}

func (s Schedule) boundary(year int, month time.Month) time.Time {
	loc := s.loc()
	day := s.Day
	if last := daysInMonth(year, month, loc); day > last {
		day = last
	}
	return time.Date(year, month, day, s.Hour, s.Minute, s.Second, 0, loc)
}

// Last is the most recent reset instant at or before now.
func (s Schedule) Last(now time.Time) time.Time {
	if !s.Active() {
		return time.Time{}
	}
	local := now.In(s.loc())
	this := s.boundary(local.Year(), local.Month())
	if !local.Before(this) {
		return this
	}
	year, month := previousYearMonth(local.Year(), local.Month())
	return s.boundary(year, month)
}

// Next is the upcoming reset instant after now.
func (s Schedule) Next(now time.Time) time.Time {
	if !s.Active() {
		return time.Time{}
	}
	local := now.In(s.loc())
	this := s.boundary(local.Year(), local.Month())
	if local.Before(this) {
		return this
	}
	year, month := nextYearMonth(local.Year(), local.Month())
	return s.boundary(year, month)
}

func (s Schedule) CycleKey(now time.Time) string {
	last := s.Last(now)
	if last.IsZero() {
		return ""
	}
	return last.In(s.loc()).Format(time.DateOnly)
}

func (s Schedule) FormatNext(now time.Time) string {
	next := s.Next(now)
	if next.IsZero() {
		return ""
	}
	return next.In(s.loc()).Format(time.RFC3339)
}
