package config

import (
	"strings"
	"time"

	// Embed IANA timezone database for cross-platform support (Windows, minimal Docker images).
	_ "time/tzdata"
)

// IsActive returns true when the current time falls within the quiet hours window.
// Returns false if the receiver is nil or Enabled is false.
func (q *QuietHours) IsActive() bool {
	return q.IsActiveAt(time.Now())
}

// IsActiveAt returns true when the given time falls within the quiet hours window.
// Exported for testability with deterministic timestamps.
// Fail-open: returns false on invalid timezone so messages are delivered rather
// than silently held.
func (q *QuietHours) IsActiveAt(now time.Time) bool {
	if q == nil || !q.Enabled {
		return false
	}

	tz := q.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false
	}

	local := now.In(loc)
	currentMinutes := local.Hour()*60 + local.Minute()

	startMinutes := parseTimeString(q.Start)
	endMinutes := parseTimeString(q.End)

	if startMinutes <= endMinutes {
		// Same-day window, e.g. 08:00-17:00
		return currentMinutes >= startMinutes && currentMinutes < endMinutes
	}
	// Midnight-crossing window, e.g. 22:00-07:00
	return currentMinutes >= startMinutes || currentMinutes < endMinutes
}

// parseTimeString converts a time string to minutes-of-day.
// Supports 24h format ("22:00", "07:00") and 12h format ("10pm", "10PM",
// "10:30pm", "10:30 PM", "7am", "7:00 AM").
// Returns -1 on invalid input (validation ensures valid format at config load time).
func parseTimeString(s string) int {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)

	// Try 12h format: ends with "am" or "pm"
	if strings.HasSuffix(lower, "am") || strings.HasSuffix(lower, "pm") {
		isPM := strings.HasSuffix(lower, "pm")
		// Strip am/pm suffix and optional space before it
		timePart := strings.TrimSpace(lower[:len(lower)-2])

		h, m := 0, 0
		if idx := strings.IndexByte(timePart, ':'); idx >= 0 {
			// "10:30" part
			h = parseDigits(timePart[:idx])
			m = parseDigits(timePart[idx+1:])
		} else {
			// "10" part (no minutes)
			h = parseDigits(timePart)
		}

		if h < 1 || h > 12 || m < 0 || m > 59 {
			return -1
		}

		// Convert 12h to 24h
		if h == 12 {
			h = 0 // 12am = 0, 12pm = 12
		}
		if isPM {
			h += 12
		}
		return h*60 + m
	}

	// 24h format: "HH:MM" or "H:MM"
	if idx := strings.IndexByte(s, ':'); idx >= 1 && idx <= 2 && len(s) >= 4 && len(s) <= 5 {
		h := parseDigits(s[:idx])
		m := parseDigits(s[idx+1:])
		if h < 0 || h > 23 || m < 0 || m > 59 {
			return -1
		}
		return h*60 + m
	}

	return -1
}

// parseDigits parses a string of digits into an integer.
// Returns -1 if the string is empty or contains non-digit characters.
func parseDigits(s string) int {
	if len(s) == 0 {
		return -1
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
