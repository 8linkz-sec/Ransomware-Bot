package config

import (
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/quiethours"
)

// QuietHoursPolicy converts JSON quiet-hours configuration into a delivery
// window policy.
func QuietHoursPolicy(q *QuietHours) *quiethours.Policy {
	if q == nil {
		return nil
	}
	return &quiethours.Policy{
		Enabled:  q.Enabled,
		Start:    q.Start,
		End:      q.End,
		Timezone: q.Timezone,
	}
}

// IsActive returns true when the current time falls within the quiet hours window.
// Returns false if the receiver is nil or Enabled is false.
func (q *QuietHours) IsActive() bool {
	return QuietHoursPolicy(q).IsActive()
}

// IsActiveAt returns true when the given time falls within the quiet hours window.
// Exported for testability with deterministic timestamps.
// Fail-open: returns false on invalid timezone so messages are delivered rather
// than silently held.
func (q *QuietHours) IsActiveAt(now time.Time) bool {
	return QuietHoursPolicy(q).IsActiveAt(now)
}

func parseTimeString(s string) (int, error) {
	return quiethours.ParseTimeString(s)
}
