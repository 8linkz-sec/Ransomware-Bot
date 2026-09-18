package quiethours

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	// Embed IANA timezone database for cross-platform support (Windows, minimal Docker images).
	_ "time/tzdata"
)

// Policy defines a delivery pause window independent of configuration storage.
type Policy struct {
	Enabled  bool
	Start    string
	End      string
	Timezone string
}

// IsActive returns true when the current time falls within the quiet hours window.
// Returns false if the receiver is nil or Enabled is false.
func (p *Policy) IsActive() bool {
	return p.IsActiveAt(time.Now())
}

// IsActiveAt returns true when the given time falls within the quiet hours window.
// Fail-open: returns false on invalid timezone or invalid times so messages are
// delivered rather than silently held.
func (p *Policy) IsActiveAt(now time.Time) bool {
	if p == nil || !p.Enabled {
		return false
	}

	tz := p.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false
	}

	local := now.In(loc)
	currentMinutes := local.Hour()*60 + local.Minute()

	startMinutes, err := ParseTimeString(p.Start)
	if err != nil {
		return false
	}
	endMinutes, err := ParseTimeString(p.End)
	if err != nil {
		return false
	}

	if startMinutes <= endMinutes {
		// Same-day window, e.g. 08:00-17:00
		return currentMinutes >= startMinutes && currentMinutes < endMinutes
	}
	// Midnight-crossing window, e.g. 22:00-07:00
	return currentMinutes >= startMinutes || currentMinutes < endMinutes
}

// ParseTimeString converts a time string to minutes-of-day.
// Supports 24h format ("22:00", "07:00") and 12h format ("10pm", "10PM",
// "10:30pm", "10:30 PM", "7am", "7:00 AM").
func ParseTimeString(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty time")
	}

	if hasMeridiemSuffix(s) {
		return parse12HourTime(s)
	}
	return parse24HourTime(s)
}

func hasMeridiemSuffix(s string) bool {
	compact := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	return strings.HasSuffix(compact, "am") || strings.HasSuffix(compact, "pm")
}

func parse12HourTime(s string) (int, error) {
	compact := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	suffix := ""
	switch {
	case strings.HasSuffix(compact, "am"):
		suffix = "am"
	case strings.HasSuffix(compact, "pm"):
		suffix = "pm"
	default:
		return 0, fmt.Errorf("invalid time %q", s)
	}

	timePart := strings.TrimSuffix(compact, suffix)
	hour, minute, err := parseClockParts(timePart, 1, 12)
	if err != nil {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	if suffix == "am" && hour == 12 {
		hour = 0
	}
	if suffix == "pm" && hour != 12 {
		hour += 12
	}
	return hour*60 + minute, nil
}

func parse24HourTime(s string) (int, error) {
	value := strings.TrimSpace(s)
	if !strings.Contains(value, ":") {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	hour, minute, err := parseClockParts(value, 0, 23)
	if err != nil {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	return hour*60 + minute, nil
}

func parseClockParts(s string, minHour, maxHour int) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) > 2 || !isDigitsOnly(parts[0]) {
		return 0, 0, fmt.Errorf("invalid clock")
	}

	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < minHour || hour > maxHour {
		return 0, 0, fmt.Errorf("invalid hour")
	}

	minute := 0
	if len(parts) == 2 {
		if len(parts[1]) != 2 || !isDigitsOnly(parts[1]) {
			return 0, 0, fmt.Errorf("invalid minute")
		}
		minute, err = strconv.Atoi(parts[1])
		if err != nil || minute < 0 || minute > 59 {
			return 0, 0, fmt.Errorf("invalid minute")
		}
	}
	return hour, minute, nil
}

// isDigitsOnly reports whether s is non-empty and every byte is an ASCII
// digit. strconv.Atoi also accepts a leading "+" or "-", which parseClockParts
// must reject: an hour or minute is a fixed-width numeral, never a signed one.
func isDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
