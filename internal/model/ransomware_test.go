package model

import (
	"strings"
	"testing"
	"time"
)

func TestRansomwareEntryKeyUsesStableDomainModel(t *testing.T) {
	entry := RansomwareEntry{
		Group:       "Example Group",
		Victim:      "Example Victim",
		Country:     "DE",
		AttackDate:  "2026-01-02",
		ClaimURL:    "https://example.test/post",
		WebsiteURL:  "https://victim.example",
		Description: "Incident details",
		Screenshot:  "https://example.test/screenshot.png",
		Discovered:  time.Date(2026, 1, 3, 4, 5, 6, 0, time.UTC),
		Published:   time.Date(2026, 1, 4, 4, 5, 6, 0, time.UTC),
	}

	key := GenerateRansomwareEntryKey(entry)

	if !strings.HasPrefix(key, "fallback:v2:") {
		t.Fatalf("GenerateRansomwareEntryKey() = %q, want fallback:v2 prefix", key)
	}
	if strings.Contains(key, entry.Group) || strings.Contains(key, entry.Victim) {
		t.Fatalf("GenerateRansomwareEntryKey() leaked source identity in %q", key)
	}
}

func TestRansomwareEntryLookupKeysIncludeLegacyFallback(t *testing.T) {
	entry := RansomwareEntry{
		Group:      "Example Group",
		Victim:     "Example Victim",
		Country:    "DE",
		AttackDate: "2026-01-02",
	}

	keys := GenerateRansomwareEntryLookupKeys(entry)

	if len(keys) != 2 {
		t.Fatalf("GenerateRansomwareEntryLookupKeys() returned %d keys, want current and legacy", len(keys))
	}
	if !strings.HasPrefix(keys[0], "fallback:v2:") {
		t.Fatalf("primary key = %q, want fallback:v2 prefix", keys[0])
	}
	if keys[1] != "Example Group|Example Victim|DE|2026-01-02" {
		t.Fatalf("legacy key = %q, want natural legacy key", keys[1])
	}
}

func TestDisplayRansomwareTitle(t *testing.T) {
	tests := []struct {
		name  string
		entry RansomwareEntry
		want  string
	}{
		{
			name:  "group and victim trimmed",
			entry: RansomwareEntry{Group: "  LockBit  ", Victim: "  Example Corp  "},
			want:  "LockBit -> Example Corp",
		},
		{
			name:  "missing group uses placeholder",
			entry: RansomwareEntry{Victim: "Example Corp"},
			want:  "Unknown group -> Example Corp",
		},
		{
			name:  "missing victim uses placeholder",
			entry: RansomwareEntry{Group: "LockBit"},
			want:  "LockBit -> Unknown victim",
		},
		{
			name:  "empty entry uses both placeholders",
			entry: RansomwareEntry{},
			want:  "Unknown group -> Unknown victim",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayRansomwareTitle(tt.entry); got != tt.want {
				t.Fatalf("DisplayRansomwareTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRansomwareEntryKeyPrefersID(t *testing.T) {
	entry := RansomwareEntry{
		ID:     "abc-123",
		Group:  "Example Group",
		Victim: "Example Victim",
	}

	if got := GenerateRansomwareEntryKey(entry); got != "id:abc-123" {
		t.Fatalf("GenerateRansomwareEntryKey() = %q, want id:abc-123", got)
	}
}

func TestRansomwareEntryLookupKeysWithIDReturnOnlyPrimaryKey(t *testing.T) {
	entry := RansomwareEntry{
		ID:         "abc-123",
		Group:      "Example Group",
		Victim:     "Example Victim",
		Country:    "DE",
		AttackDate: "2026-01-02",
	}

	keys := GenerateRansomwareEntryLookupKeys(entry)

	if len(keys) != 1 {
		t.Fatalf("GenerateRansomwareEntryLookupKeys() returned %d keys, want only the ID key", len(keys))
	}
	if keys[0] != "id:abc-123" {
		t.Fatalf("primary key = %q, want id:abc-123", keys[0])
	}
}

func TestRansomwareEntryFallbackKeyDependsOnAllHashedFields(t *testing.T) {
	base := RansomwareEntry{
		Group:      "Example Group",
		Victim:     "Example Victim",
		Country:    "DE",
		AttackDate: "2026-01-02",
		Discovered: time.Date(2026, 1, 3, 4, 5, 6, 0, time.UTC),
	}
	changed := base
	changed.Discovered = base.Discovered.Add(time.Second)

	if GenerateRansomwareEntryKey(base) == GenerateRansomwareEntryKey(changed) {
		t.Fatal("fallback keys should differ when Discovered changes")
	}
	first := GenerateRansomwareEntryKey(base)
	second := GenerateRansomwareEntryKey(base)
	if first != second {
		t.Fatal("fallback keys must be deterministic for identical entries")
	}
}
