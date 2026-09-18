package config

import (
	"testing"
	"time"
)

func TestQuietHours_NilReceiver(t *testing.T) {
	var qh *QuietHours
	if qh.IsActiveAt(time.Now()) {
		t.Error("nil QuietHours should never be active")
	}
}

func TestQuietHoursPolicyCopiesConfig(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Europe/Berlin"}

	policy := QuietHoursPolicy(qh)
	if policy == nil {
		t.Fatal("QuietHoursPolicy() returned nil")
	}
	if !policy.Enabled || policy.Start != qh.Start || policy.End != qh.End || policy.Timezone != qh.Timezone {
		t.Fatalf("QuietHoursPolicy() = %#v, want values from %#v", policy, qh)
	}

	qh.Start = "23:00"
	if got := policy.Start; got != "22:00" {
		t.Fatalf("QuietHoursPolicy() aliased config start, got %q", got)
	}
}

func TestQuietHours_DisabledFlag(t *testing.T) {
	qh := &QuietHours{Enabled: false, Start: "22:00", End: "07:00", Timezone: "UTC"}
	now := time.Date(2026, 2, 14, 23, 0, 0, 0, time.UTC)
	if qh.IsActiveAt(now) {
		t.Error("disabled quiet hours should never be active")
	}
}

func TestQuietHours_SameDayWindow(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "08:00", End: "17:00", Timezone: "UTC"}

	tests := []struct {
		name   string
		hour   int
		minute int
		want   bool
	}{
		{"before window", 7, 59, false},
		{"at start (inclusive)", 8, 0, true},
		{"mid window", 12, 30, true},
		{"at end (exclusive)", 17, 0, false},
		{"after window", 20, 0, false},
		{"midnight", 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 2, 14, tt.hour, tt.minute, 0, 0, time.UTC)
			got := qh.IsActiveAt(now)
			if got != tt.want {
				t.Errorf("IsActiveAt(%02d:%02d) = %v, want %v", tt.hour, tt.minute, got, tt.want)
			}
		})
	}
}

func TestQuietHours_MidnightCrossingWindow(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "UTC"}

	tests := []struct {
		name   string
		hour   int
		minute int
		want   bool
	}{
		{"before window (daytime)", 14, 0, false},
		{"at start (inclusive)", 22, 0, true},
		{"late night", 23, 30, true},
		{"midnight", 0, 0, true},
		{"early morning", 3, 0, true},
		{"at end (exclusive)", 7, 0, false},
		{"after window (morning)", 8, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 2, 14, tt.hour, tt.minute, 0, 0, time.UTC)
			got := qh.IsActiveAt(now)
			if got != tt.want {
				t.Errorf("IsActiveAt(%02d:%02d) = %v, want %v", tt.hour, tt.minute, got, tt.want)
			}
		})
	}
}

func TestQuietHours_12hFormat(t *testing.T) {
	tests := []struct {
		name   string
		start  string
		end    string
		hour   int
		minute int
		want   bool
	}{
		{"10pm-7am: at 23:00", "10pm", "7am", 23, 0, true},
		{"10pm-7am: at 14:00", "10pm", "7am", 14, 0, false},
		{"10pm-7am: at 06:30", "10pm", "7am", 6, 30, true},
		{"10pm-7am: at 07:00 (end exclusive)", "10pm", "7am", 7, 0, false},
		{"10:30 PM-6:30 AM: at 22:30", "10:30 PM", "6:30 AM", 22, 30, true},
		{"10:30 PM-6:30 AM: at 22:29", "10:30 PM", "6:30 AM", 22, 29, false},
		{"8am-5pm: at 12:00", "8am", "5pm", 12, 0, true},
		{"8am-5pm: at 20:00", "8am", "5pm", 20, 0, false},
		{"12am-6am (midnight-6): at 00:00", "12am", "6am", 0, 0, true},
		{"12am-6am: at 03:00", "12am", "6am", 3, 0, true},
		{"12pm-1pm (noon-1): at 12:00", "12pm", "1pm", 12, 0, true},
		{"12pm-1pm: at 12:30", "12pm", "1pm", 12, 30, true},
		{"12pm-1pm: at 13:00 (end exclusive)", "12pm", "1pm", 13, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qh := &QuietHours{Enabled: true, Start: tt.start, End: tt.end, Timezone: "UTC"}
			now := time.Date(2026, 2, 14, tt.hour, tt.minute, 0, 0, time.UTC)
			got := qh.IsActiveAt(now)
			if got != tt.want {
				t.Errorf("IsActiveAt(%02d:%02d) with start=%q end=%q = %v, want %v",
					tt.hour, tt.minute, tt.start, tt.end, got, tt.want)
			}
		})
	}
}

func TestQuietHours_Mixed24hAnd12h(t *testing.T) {
	// 24h start, 12h end
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "7am", Timezone: "UTC"}
	now := time.Date(2026, 2, 14, 23, 0, 0, 0, time.UTC)
	if !qh.IsActiveAt(now) {
		t.Error("mixed 24h/12h format should work: 23:00 should be in 22:00-7am window")
	}
}

func TestQuietHours_TimezoneConversion(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Europe/Berlin"}

	berlin, _ := time.LoadLocation("Europe/Berlin")

	// 23:00 Berlin time -> should be active
	nowBerlin := time.Date(2026, 1, 15, 23, 0, 0, 0, berlin)
	if !qh.IsActiveAt(nowBerlin) {
		t.Error("23:00 Berlin should be in quiet hours")
	}

	// 08:00 Berlin time -> should NOT be active
	morningBerlin := time.Date(2026, 1, 15, 8, 0, 0, 0, berlin)
	if qh.IsActiveAt(morningBerlin) {
		t.Error("08:00 Berlin should NOT be in quiet hours")
	}

	// Pass UTC time that corresponds to 23:00 Berlin (CET = UTC+1 in January)
	// 22:00 UTC = 23:00 CET
	utcTime := time.Date(2026, 1, 15, 22, 0, 0, 0, time.UTC)
	if !qh.IsActiveAt(utcTime) {
		t.Error("22:00 UTC (= 23:00 Berlin CET) should be in quiet hours")
	}

	// 09:00 UTC = 10:00 CET -> should NOT be active
	utcMorning := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	if qh.IsActiveAt(utcMorning) {
		t.Error("09:00 UTC (= 10:00 Berlin CET) should NOT be in quiet hours")
	}
}

func TestQuietHours_EmptyTimezoneDefaultsUTC(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: ""}

	now := time.Date(2026, 2, 14, 23, 0, 0, 0, time.UTC)
	if !qh.IsActiveAt(now) {
		t.Error("23:00 UTC should be in quiet hours when timezone is empty (defaults to UTC)")
	}
}

func TestQuietHours_InvalidTimezoneFailOpen(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Invalid/Zone"}

	now := time.Date(2026, 2, 14, 23, 0, 0, 0, time.UTC)
	if qh.IsActiveAt(now) {
		t.Error("invalid timezone should fail-open (return false)")
	}
}

func TestQuietHours_InvalidTimeFailOpen(t *testing.T) {
	now := time.Date(2026, 2, 14, 23, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		qh   *QuietHours
	}{
		{"invalid start", &QuietHours{Enabled: true, Start: "25:00", End: "07:00", Timezone: "UTC"}},
		{"invalid end", &QuietHours{Enabled: true, Start: "22:00", End: "13pm", Timezone: "UTC"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.qh.IsActiveAt(now) {
				t.Fatal("invalid quiet-hours time should fail-open (return false)")
			}
		})
	}
}

func TestQuietHours_BoundaryZeroStart(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "00:00", End: "06:00", Timezone: "UTC"}

	tests := []struct {
		name   string
		hour   int
		minute int
		want   bool
	}{
		{"at midnight", 0, 0, true},
		{"03:00", 3, 0, true},
		{"at end", 6, 0, false},
		{"afternoon", 14, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 2, 14, tt.hour, tt.minute, 0, 0, time.UTC)
			got := qh.IsActiveAt(now)
			if got != tt.want {
				t.Errorf("IsActiveAt(%02d:%02d) = %v, want %v", tt.hour, tt.minute, got, tt.want)
			}
		})
	}
}

// parseTimeString unit tests

func TestParseTimeString_24h(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"00:00", 0},
		{"07:00", 420},
		{"12:30", 750},
		{"22:00", 1320},
		{"23:59", 1439},
	}
	for _, tt := range tests {
		got, err := parseTimeString(tt.input)
		if err != nil {
			t.Fatalf("parseTimeString(%q) error = %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("parseTimeString(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseTimeString_12h(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"12am", 0},     // midnight
		{"12:00 AM", 0}, // midnight
		{"1am", 60},
		{"7am", 420},
		{"12pm", 720},     // noon
		{"12:00 PM", 720}, // noon
		{"1pm", 780},
		{"10pm", 1320},     // 22:00
		{"10:30pm", 1350},  // 22:30
		{"10:30 PM", 1350}, // 22:30
		{"11:59pm", 1439},  // 23:59
		{"11:59 pm", 1439}, // 23:59 with space
	}
	for _, tt := range tests {
		got, err := parseTimeString(tt.input)
		if err != nil {
			t.Fatalf("parseTimeString(%q) error = %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("parseTimeString(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseTimeString_Invalid(t *testing.T) {
	invalids := []string{"", "abc", "25:00", "12:60", "13pm", "0am", "noon", "midnight"}
	for _, s := range invalids {
		if got, err := parseTimeString(s); err == nil {
			t.Errorf("parseTimeString(%q) = %d, nil error; want error", s, got)
		}
	}
}

// Validation tests

func TestValidateQuietHours_Nil(t *testing.T) {
	if err := validateQuietHours("test", nil); err != nil {
		t.Errorf("nil quiet hours should pass validation: %v", err)
	}
}

func TestValidateQuietHours_Disabled(t *testing.T) {
	// Disabled quiet hours skip time validation
	qh := &QuietHours{Enabled: false, Start: "invalid", End: "also invalid"}
	if err := validateQuietHours("test", qh); err != nil {
		t.Errorf("disabled quiet hours should pass validation regardless of values: %v", err)
	}
}

func TestValidateQuietHours_Valid(t *testing.T) {
	tests := []struct {
		name string
		qh   *QuietHours
	}{
		{"24h same day", &QuietHours{Enabled: true, Start: "08:00", End: "17:00", Timezone: "UTC"}},
		{"24h midnight crossing", &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Europe/Berlin"}},
		{"24h empty timezone", &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: ""}},
		{"24h US timezone", &QuietHours{Enabled: true, Start: "20:00", End: "06:00", Timezone: "America/New_York"}},
		{"12h simple", &QuietHours{Enabled: true, Start: "10pm", End: "7am", Timezone: "UTC"}},
		{"12h with minutes", &QuietHours{Enabled: true, Start: "10:30 PM", End: "6:30 AM", Timezone: "UTC"}},
		{"mixed formats", &QuietHours{Enabled: true, Start: "22:00", End: "7am", Timezone: "UTC"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateQuietHours("test", tt.qh); err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
		})
	}
}

func TestValidateQuietHours_InvalidFormat(t *testing.T) {
	tests := []struct {
		name  string
		start string
		end   string
	}{
		{"empty start", "", "17:00"},
		{"letters", "ab:cd", "17:00"},
		{"hour too high 24h", "25:00", "17:00"},
		{"minute too high 24h", "08:00", "17:60"},
		{"no colon 4 digits", "0800", "17:00"},
		{"13pm invalid", "13pm", "7am"},
		{"0am invalid", "08:00", "0am"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qh := &QuietHours{Enabled: true, Start: tt.start, End: tt.end, Timezone: "UTC"}
			if err := validateQuietHours("test", qh); err == nil {
				t.Errorf("expected error for start=%q end=%q", tt.start, tt.end)
			}
		})
	}
}

func TestValidateQuietHours_StartEqualsEnd(t *testing.T) {
	tests := []struct {
		name  string
		start string
		end   string
	}{
		{"24h equal", "12:00", "12:00"},
		{"12h equal to 24h", "12pm", "12:00"}, // both resolve to 720 minutes
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qh := &QuietHours{Enabled: true, Start: tt.start, End: tt.end, Timezone: "UTC"}
			if err := validateQuietHours("test", qh); err == nil {
				t.Errorf("start=%q end=%q should be rejected (same resolved time)", tt.start, tt.end)
			}
		})
	}
}

func TestValidateQuietHours_InvalidTimezone(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Not/Real"}
	if err := validateQuietHours("test", qh); err == nil {
		t.Error("invalid timezone should be rejected")
	}
}
