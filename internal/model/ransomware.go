package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const (
	ransomwareEntryIDKeyPrefix           = "id:"
	ransomwareEntryFallbackV2KeyPrefix   = "fallback:v2:"
	ransomwareEntryHashFieldSeparator    = byte(0)
	ransomwareLegacyFallbackKeySeparator = "|"
)

// RansomwareEntry represents one normalized ransomware incident.
type RansomwareEntry struct {
	ID          string    `json:"id"`
	Group       string    `json:"group"`
	Victim      string    `json:"victim"`
	Country     string    `json:"country"`
	Activity    string    `json:"activity"`
	AttackDate  string    `json:"attackdate"`
	Discovered  time.Time `json:"discovered"`
	ClaimURL    string    `json:"post_url"`
	WebsiteURL  string    `json:"website"`
	Description string    `json:"description"`
	Screenshot  string    `json:"screenshot"`
	Published   time.Time `json:"published"`
}

// DisplayRansomwareTitle returns a stable human-facing identity for an entry.
func DisplayRansomwareTitle(entry RansomwareEntry) string {
	group := strings.TrimSpace(entry.Group)
	if group == "" {
		group = "Unknown group"
	}
	victim := strings.TrimSpace(entry.Victim)
	if victim == "" {
		victim = "Unknown victim"
	}
	return group + " -> " + victim
}

// GenerateRansomwareEntryKey creates a unique key for ransomware entry deduplication.
func GenerateRansomwareEntryKey(entry RansomwareEntry) string {
	if entry.ID != "" {
		return ransomwareEntryIDKeyPrefix + entry.ID
	}
	return ransomwareEntryFallbackV2KeyPrefix + hashRansomwareEntryFallbackFields(entry)
}

// GenerateRansomwareEntryLookupKeys returns the primary key and legacy keys for status-file compatibility.
func GenerateRansomwareEntryLookupKeys(entry RansomwareEntry) []string {
	primary := GenerateRansomwareEntryKey(entry)
	if entry.ID != "" {
		return []string{primary}
	}

	legacy := legacyRansomwareFallbackEntryKey(entry)
	if legacy == primary {
		return []string{primary}
	}
	return []string{primary, legacy}
}

func hashRansomwareEntryFallbackFields(entry RansomwareEntry) string {
	h := sha256.New()
	for _, value := range []string{
		entry.Group,
		entry.Victim,
		entry.Country,
		entry.AttackDate,
		entry.ClaimURL,
		entry.WebsiteURL,
		entry.Description,
		entry.Screenshot,
		entry.Discovered.Format(time.RFC3339Nano),
		entry.Published.Format(time.RFC3339Nano),
	} {
		h.Write([]byte(value))
		h.Write([]byte{ransomwareEntryHashFieldSeparator})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func legacyRansomwareFallbackEntryKey(entry RansomwareEntry) string {
	return strings.Join(
		[]string{entry.Group, entry.Victim, entry.Country, entry.AttackDate},
		ransomwareLegacyFallbackKeySeparator,
	)
}
