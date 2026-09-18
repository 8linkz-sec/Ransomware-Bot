package formatfields

import (
	"testing"
	"time"

	// Embed tzdata so timezone-dependent assertions (e.g. Europe/Berlin) do not
	// rely on the host's timezone database.
	_ "time/tzdata"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
)

func TestRansomwareFieldValueForSharedValues(t *testing.T) {
	discovered := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	published := discovered.Add(time.Hour)
	entry := api.RansomwareEntry{
		ID:          "42",
		Group:       "LockBit",
		Victim:      "Example Corp",
		Country:     "DE",
		Activity:    "Manufacturing",
		AttackDate:  "2026-01-02 03:04:05",
		Discovered:  discovered,
		Published:   published,
		ClaimURL:    "https://leak.example/post",
		WebsiteURL:  "https://example.com",
		Description: "<p>details</p>",
		Screenshot:  "https://cdn.example/screen.png",
	}

	tests := []struct {
		field          string
		wantLabel      string
		wantValue      string
		wantParsedTime bool
	}{
		{field: FieldID, wantLabel: "ID", wantValue: "42"},
		{field: FieldGroup, wantLabel: "Group", wantValue: "LockBit"},
		{field: FieldVictim, wantLabel: "Victim", wantValue: "Example Corp"},
		{field: FieldCountry, wantLabel: "Country", wantValue: "Germany"},
		{field: FieldActivity, wantLabel: "Activity", wantValue: "Manufacturing"},
		{field: FieldAttackDateLegacy, wantLabel: "Attack Date", wantValue: "2026-01-02 03:04:05 UTC", wantParsedTime: true},
		{field: FieldDiscovered, wantLabel: "Discovered", wantValue: "2026-01-02 03:04:05 UTC", wantParsedTime: true},
		{field: FieldPublished, wantLabel: "Published", wantValue: "2026-01-02 04:04:05 UTC", wantParsedTime: true},
		{field: FieldPostURL, wantLabel: "Ransom URL", wantValue: "hxxps://leak.example/post"},
		{field: FieldWebsite, wantLabel: "Website", wantValue: "https://example.com"},
		{field: FieldURL, wantLabel: "Website", wantValue: "https://example.com"},
		{field: FieldDescription, wantLabel: "Description", wantValue: "<p>details</p>"},
		{field: FieldScreenshot, wantLabel: "Screenshot", wantValue: "https://cdn.example/screen.png"},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			got, ok := RansomwareFieldValueFor(tt.field, entry, false)
			if !ok {
				t.Fatal("RansomwareFieldValueFor() ok = false")
			}
			if got.Label != tt.wantLabel {
				t.Fatalf("Label = %q, want %q", got.Label, tt.wantLabel)
			}
			if got.Value != tt.wantValue {
				t.Fatalf("Value = %q, want %q", got.Value, tt.wantValue)
			}
			if got.HasParsedTime != tt.wantParsedTime {
				t.Fatalf("HasParsedTime = %v, want %v", got.HasParsedTime, tt.wantParsedTime)
			}
		})
	}
}

func TestRansomwareFieldValueForRejectsUnknownField(t *testing.T) {
	if _, ok := RansomwareFieldValueFor("unknown", api.RansomwareEntry{}, false); ok {
		t.Fatal("RansomwareFieldValueFor() ok = true, want false")
	}
}

func TestRansomwareFieldValueForLocaleUsesLocalizedCountryName(t *testing.T) {
	got, ok := RansomwareFieldValueForLocale(FieldCountry, api.RansomwareEntry{Country: "DE"}, false, "de")
	if !ok {
		t.Fatal("RansomwareFieldValueForLocale() ok = false")
	}
	if got.Value != "Deutschland" {
		t.Fatalf("country value = %q, want Deutschland", got.Value)
	}
}

func TestRansomwareFieldValueForDisplayUsesConfiguredTimestampFormat(t *testing.T) {
	got, ok := RansomwareFieldValueForDisplay(
		FieldDiscovered,
		api.RansomwareEntry{Discovered: time.Date(2026, 1, 2, 2, 4, 5, 0, time.UTC)},
		false,
		"en",
		"02.01.2006 15:04 MST",
		"Europe/Berlin",
	)
	if !ok {
		t.Fatal("RansomwareFieldValueForDisplay() ok = false")
	}
	if got.Value != "02.01.2026 03:04 CET" {
		t.Fatalf("timestamp value = %q, want localized display time", got.Value)
	}
}

func TestRansomwareFieldLabelReturnsInputForUnknownField(t *testing.T) {
	if got := RansomwareFieldLabel("custom_field"); got != "custom_field" {
		t.Fatalf("RansomwareFieldLabel(custom_field) = %q, want the input echoed back", got)
	}
}

func TestRansomwareFieldValueForEmptyOptionalFields(t *testing.T) {
	entry := api.RansomwareEntry{}

	tests := []string{FieldCountry, FieldDiscovered, FieldPublished}
	for _, field := range tests {
		t.Run(field, func(t *testing.T) {
			got, ok := RansomwareFieldValueFor(field, entry, true)
			if !ok {
				t.Fatal("RansomwareFieldValueFor() ok = false")
			}
			if got.Value != "" {
				t.Fatalf("Value = %q, want empty for unset field", got.Value)
			}
			if got.HasParsedTime {
				t.Fatal("HasParsedTime = true, want false for unset field")
			}
		})
	}
}

func TestRansomwareFieldValueForUnparsableAttackDateKeepsRawValue(t *testing.T) {
	got, ok := RansomwareFieldValueFor(FieldAttackDate, api.RansomwareEntry{AttackDate: "sometime soon"}, false)
	if !ok {
		t.Fatal("RansomwareFieldValueFor() ok = false")
	}
	if got.Value != "sometime soon" {
		t.Fatalf("Value = %q, want raw attack date passed through", got.Value)
	}
	if got.HasParsedTime {
		t.Fatal("HasParsedTime = true, want false for unparsable attack date")
	}
}
