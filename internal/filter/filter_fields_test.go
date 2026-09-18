package filter

import (
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

// --- Generic field registry: full extractor coverage ---

func TestMatchesAPIEntry_FieldRegistryCoversEveryRegisteredField(t *testing.T) {
	published := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	discovered := time.Date(2025, 12, 24, 18, 30, 0, 0, time.UTC)
	entry := api.RansomwareEntry{
		ID:          "abc-123",
		Group:       "LockBit",
		Victim:      "City Hospital",
		Country:     "DE",
		Activity:    "Data Leak",
		AttackDate:  "2025-12-20",
		ClaimURL:    "https://example.onion/post/123",
		WebsiteURL:  "https://hospital.example",
		Description: "Records exfiltrated",
		Screenshot:  "https://screens.example/shot.png",
		Published:   published,
		Discovered:  discovered,
	}

	tests := []struct {
		field string
		rule  string
	}{
		{"id", "abc"},
		{"group", "lockbit"},
		{"victim", "hospital"},
		{"country", "de"},
		{"activity", "leak"},
		{"attack_date", "2025-12-20"},
		{"claim_url", "post/123"},
		{"website", "hospital.example"},
		{"description", "exfiltrated"},
		{"screenshot", "shot.png"},
		{"published", "2026-03-04T05:06:07Z"},
		{"discovered", "2025-12-24T18:30:00Z"},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			include := &Rules{IncludeFields: map[string][]string{tt.field: {tt.rule}}}
			if !MatchesAPIEntry(include, entry) {
				t.Fatalf("include field %q with rule %q should match entry", tt.field, tt.rule)
			}

			exclude := &Rules{ExcludeFields: map[string][]string{tt.field: {tt.rule}}}
			if MatchesAPIEntry(exclude, entry) {
				t.Fatalf("exclude field %q with rule %q should reject entry", tt.field, tt.rule)
			}
		})
	}
}

func TestMatchesRSSEntry_FieldRegistryCoversEveryRegisteredField(t *testing.T) {
	published := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entry := rss.Entry{
		Title:       "New ransomware report",
		Link:        "https://example.test/report",
		Description: "Operational update",
		Published:   published,
		Author:      "CERT Team",
		Categories:  []string{"security", "malware"},
		GUID:        "guid-42",
		FeedTitle:   "Threat Feed",
		FeedURL:     "https://feeds.example.test/rss.xml",
	}

	tests := []struct {
		field string
		rule  string
	}{
		{"title", "ransomware"},
		{"link", "example.test/report"},
		{"description", "operational"},
		{"published", "2026-01-02T03:04:05Z"},
		{"author", "cert"},
		{"category", "malware"},
		{"categories", "security"},
		{"guid", "guid-42"},
		{"feed_title", "threat"},
		{"feed_url", "feeds.example.test"},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			include := &Rules{IncludeFields: map[string][]string{tt.field: {tt.rule}}}
			if !MatchesRSSEntry(include, entry) {
				t.Fatalf("include field %q with rule %q should match entry", tt.field, tt.rule)
			}

			exclude := &Rules{ExcludeFields: map[string][]string{tt.field: {tt.rule}}}
			if MatchesRSSEntry(exclude, entry) {
				t.Fatalf("exclude field %q with rule %q should reject entry", tt.field, tt.rule)
			}
		})
	}
}

func TestMatchesAPIEntry_UnknownFieldName(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "City Hospital"}

	include := &Rules{IncludeFields: map[string][]string{"no_such_field": {"anything"}}}
	if MatchesAPIEntry(include, entry) {
		t.Error("unknown include field should never match, rejecting the entry")
	}

	exclude := &Rules{ExcludeFields: map[string][]string{"no_such_field": {"anything"}}}
	if !MatchesAPIEntry(exclude, entry) {
		t.Error("unknown exclude field should never match, accepting the entry")
	}
}

func TestMatchesRSSEntry_UnknownFieldName(t *testing.T) {
	entry := rss.Entry{Title: "New ransomware report"}

	include := &Rules{IncludeFields: map[string][]string{"no_such_field": {"anything"}}}
	if MatchesRSSEntry(include, entry) {
		t.Error("unknown include field should never match, rejecting the entry")
	}

	exclude := &Rules{ExcludeFields: map[string][]string{"no_such_field": {"anything"}}}
	if !MatchesRSSEntry(exclude, entry) {
		t.Error("unknown exclude field should never match, accepting the entry")
	}
}

func TestMatchesAPIEntry_EmptyIncludeFieldRuleListIsIgnored(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "Acme Corp"}

	f := &Rules{IncludeFields: map[string][]string{"victim": {}}}
	if !MatchesAPIEntry(f, entry) {
		t.Error("include field with empty rule list should be skipped, accepting the entry")
	}
}

func TestMatchesAPIEntry_BlankFieldRuleAndEmptyFieldValue(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "Acme Corp"}

	blankRule := &Rules{IncludeFields: map[string][]string{"victim": {"  "}}}
	if MatchesAPIEntry(blankRule, entry) {
		t.Error("blank include field rule should not match any value")
	}

	emptyValue := &Rules{IncludeFields: map[string][]string{"activity": {"leak"}}}
	if MatchesAPIEntry(emptyValue, entry) {
		t.Error("include field rule should not match an empty field value")
	}

	blankExclude := &Rules{ExcludeFields: map[string][]string{"victim": {"  "}}}
	if !MatchesAPIEntry(blankExclude, entry) {
		t.Error("blank exclude field rule should not reject any entry")
	}
}

// --- containsFoldTrimmed and formatFilterTime unit tests ---

func TestContainsFoldTrimmed(t *testing.T) {
	tests := []struct {
		name  string
		value string
		rule  string
		want  bool
	}{
		{"case-insensitive substring", " City Hospital ", "hospital", true},
		{"trimmed rule", "City Hospital", " Hospital ", true},
		{"no substring", "Acme Corp", "hospital", false},
		{"empty rule never matches", "City Hospital", "", false},
		{"whitespace rule never matches", "City Hospital", "   ", false},
		{"empty value never matches", "", "hospital", false},
		{"whitespace value never matches", "   ", "hospital", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsFoldTrimmed(tt.value, tt.rule); got != tt.want {
				t.Fatalf("containsFoldTrimmed(%q, %q) = %v, want %v", tt.value, tt.rule, got, tt.want)
			}
		})
	}
}

func TestFormatFilterTime(t *testing.T) {
	if got := formatFilterTime(time.Time{}); got != "" {
		t.Fatalf("formatFilterTime(zero) = %q, want empty string", got)
	}

	value := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if got := formatFilterTime(value); got != "2026-03-04T05:06:07Z" {
		t.Fatalf("formatFilterTime() = %q, want RFC3339 value", got)
	}
}

func TestMatchesAPIEntry_ZeroTimestampFieldsDoNotMatch(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "Acme Corp"}

	f := &Rules{IncludeFields: map[string][]string{"published": {"2026"}}}
	if MatchesAPIEntry(f, entry) {
		t.Error("zero published timestamp should not match a timestamp include rule")
	}

	f = &Rules{IncludeFields: map[string][]string{"discovered": {"2026"}}}
	if MatchesAPIEntry(f, entry) {
		t.Error("zero discovered timestamp should not match a timestamp include rule")
	}
}
