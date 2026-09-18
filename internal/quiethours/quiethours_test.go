package quiethours

import (
	"testing"
	"time"
)

func TestPolicyIsActiveAtSameDayWindow(t *testing.T) {
	policy := &Policy{Enabled: true, Start: "08:00", End: "17:00", Timezone: "UTC"}

	if !policy.IsActiveAt(time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)) {
		t.Fatal("same-day window should be active at inclusive start")
	}
	if policy.IsActiveAt(time.Date(2026, 1, 2, 17, 0, 0, 0, time.UTC)) {
		t.Fatal("same-day window should be inactive at exclusive end")
	}
}

func TestPolicyIsActiveAtMidnightCrossingWindow(t *testing.T) {
	policy := &Policy{Enabled: true, Start: "22:00", End: "07:00", Timezone: "UTC"}

	if !policy.IsActiveAt(time.Date(2026, 1, 2, 23, 0, 0, 0, time.UTC)) {
		t.Fatal("midnight-crossing window should be active after start")
	}
	if !policy.IsActiveAt(time.Date(2026, 1, 3, 3, 0, 0, 0, time.UTC)) {
		t.Fatal("midnight-crossing window should be active before end")
	}
	if policy.IsActiveAt(time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("midnight-crossing window should be inactive outside the window")
	}
}

func TestPolicyFailOpenOnInvalidConfig(t *testing.T) {
	now := time.Date(2026, 1, 2, 23, 0, 0, 0, time.UTC)

	tests := []*Policy{
		nil,
		{Enabled: false, Start: "22:00", End: "07:00", Timezone: "UTC"},
		{Enabled: true, Start: "bad", End: "07:00", Timezone: "UTC"},
		{Enabled: true, Start: "22:00", End: "bad", Timezone: "UTC"},
		{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Bad/Zone"},
	}

	for _, policy := range tests {
		if policy.IsActiveAt(now) {
			t.Fatalf("Policy.IsActiveAt() = true for invalid or disabled policy %#v, want false", policy)
		}
	}
}

func TestParseTimeStringAccepts12HourAnd24HourFormats(t *testing.T) {
	tests := map[string]int{
		"00:00":    0,
		"22:30":    1350,
		"10pm":     1320,
		"10:30 PM": 1350,
		"7am":      420,
	}

	for input, want := range tests {
		got, err := ParseTimeString(input)
		if err != nil {
			t.Fatalf("ParseTimeString(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseTimeString(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestPolicyIsActiveNilAndDisabled(t *testing.T) {
	// IsActive delegates to IsActiveAt(time.Now()); nil and disabled policies
	// are false at any point in time, so this stays deterministic.
	var nilPolicy *Policy
	if nilPolicy.IsActive() {
		t.Fatal("nil Policy.IsActive() = true, want false")
	}

	disabled := &Policy{Enabled: false, Start: "00:00", End: "23:59", Timezone: "UTC"}
	if disabled.IsActive() {
		t.Fatal("disabled Policy.IsActive() = true, want false")
	}
}

func TestPolicyIsActiveAtEmptyTimezoneDefaultsToUTC(t *testing.T) {
	policy := &Policy{Enabled: true, Start: "08:00", End: "17:00", Timezone: ""}

	if !policy.IsActiveAt(time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatal("empty timezone should default to UTC and be active at 09:00 UTC")
	}
	if policy.IsActiveAt(time.Date(2026, 1, 2, 18, 0, 0, 0, time.UTC)) {
		t.Fatal("empty timezone should default to UTC and be inactive at 18:00 UTC")
	}
}

func TestPolicyIsActiveAtAppliesConfiguredTimezone(t *testing.T) {
	policy := &Policy{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Europe/Berlin"}

	// 2026-01-15 21:30 UTC is 22:30 CET (UTC+1): inside the window locally,
	// even though 21:30 would be outside in UTC.
	if !policy.IsActiveAt(time.Date(2026, 1, 15, 21, 30, 0, 0, time.UTC)) {
		t.Fatal("21:30 UTC (22:30 CET) should be inside the Berlin window")
	}
	// 2026-06-15 20:30 UTC is 22:30 CEST (UTC+2, DST).
	if !policy.IsActiveAt(time.Date(2026, 6, 15, 20, 30, 0, 0, time.UTC)) {
		t.Fatal("20:30 UTC (22:30 CEST) should be inside the Berlin window")
	}
	// 2026-01-15 22:30 UTC is 23:30 CET: still inside. Control below is midday.
	if policy.IsActiveAt(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("13:00 CET should be outside the Berlin window")
	}
	// Boundaries in local time: inclusive start, exclusive end.
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("LoadLocation(Europe/Berlin) error = %v", err)
	}
	if !policy.IsActiveAt(time.Date(2026, 1, 15, 22, 0, 0, 0, berlin)) {
		t.Fatal("window start 22:00 local should be active (inclusive)")
	}
	if policy.IsActiveAt(time.Date(2026, 1, 16, 7, 0, 0, 0, berlin)) {
		t.Fatal("window end 07:00 local should be inactive (exclusive)")
	}
}

func TestParseTimeString12HourEdgeCases(t *testing.T) {
	tests := map[string]int{
		"12am":    0,   // midnight
		"12pm":    720, // noon
		"12:30am": 30,
		"12:30pm": 750,
		"1AM":     60,
	}

	for input, want := range tests {
		got, err := ParseTimeString(input)
		if err != nil {
			t.Fatalf("ParseTimeString(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseTimeString(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestParseTimeStringRejectsInvalidInput(t *testing.T) {
	tests := []string{
		"",         // empty
		"   ",      // whitespace only
		"13pm",     // 12h hour above 12
		"0am",      // 12h hour below 1
		"24:00",    // 24h hour above 23
		"-1:00",    // negative hour
		"aa:00",    // non-numeric hour
		"10",       // 24h format requires a colon
		"10:5",     // minute must have two digits
		"10:5pm",   // minute must have two digits (12h)
		"22:75",    // minute out of range
		"22:xx",    // non-numeric two-char minute
		"10:20:30", // too many clock parts
		":30",      // empty hour part
	}

	for _, input := range tests {
		if _, err := ParseTimeString(input); err == nil {
			t.Fatalf("ParseTimeString(%q) error = nil, want error", input)
		}
	}
}

func TestParse12HourTimeRequiresMeridiemSuffix(t *testing.T) {
	// ParseTimeString routes suffix-less input to the 24h parser, so the
	// defensive default branch is only reachable via a direct call.
	if _, err := parse12HourTime("22:00"); err == nil {
		t.Fatal("parse12HourTime(22:00) error = nil, want error")
	}
}

func TestPolicyIsActiveAtZeroLengthWindowIsNeverActive(t *testing.T) {
	policy := &Policy{Enabled: true, Start: "08:00", End: "08:00", Timezone: "UTC"}

	if policy.IsActiveAt(time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)) {
		t.Fatal("start == end window should never be active")
	}
}

func TestParseTimeStringRejectsSignedClockParts(t *testing.T) {
	for _, input := range []string{"07:+5", "+7:05", "07:-5", "-0:00"} {
		if _, err := ParseTimeString(input); err == nil {
			t.Fatalf("ParseTimeString(%q) error = nil, want error", input)
		}
	}
	if minutes, err := ParseTimeString("07:05"); err != nil || minutes != 425 {
		t.Fatalf("ParseTimeString(%q) = %d, %v, want 425, nil", "07:05", minutes, err)
	}
}
