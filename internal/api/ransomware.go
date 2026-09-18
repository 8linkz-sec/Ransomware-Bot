// Ransomware API processing is split into two phases:
// Phase 1: Fetch and store entries from API (prevents data loss)
// Phase 2: Send stored entries to configured webhooks (with retry capability)
//
// This separation ensures no data is lost if webhook delivery fails,
// enabling recovery and retry mechanisms.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
	"golang.org/x/text/language"
)

// ransomwareAPITime handles the ransomware.live API's inconsistent time formats.
//
// The ransomware.live API returns timestamps in multiple formats:
// - With microseconds: "2025-08-02 12:52:04.158280"
// - Without microseconds: "2025-08-02 12:52:04"
// - RFC3339 with sub-seconds: "2026-05-14T11:54:55.383096+00:00"
// - RFC3339: "2026-05-14T11:54:55+00:00"
type ransomwareAPITime struct {
	time.Time
}

// UnmarshalJSON parses the API's time format
func (ct *ransomwareAPITime) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		ct.Time = time.Time{}
		return nil
	}

	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("invalid ransomware API timestamp value: expected JSON string or null: %w", err)
	}
	if s == "null" || s == "" {
		ct.Time = time.Time{}
		return nil
	}

	parsed, err := timeutil.ParseFlexibleTimestamp(s)
	if err == nil {
		ct.Time = parsed
		return nil
	}

	return fmt.Errorf(
		"invalid ransomware API timestamp %q (expected one of: %s): %w",
		s,
		strings.Join(timeutil.AcceptedFlexibleTimestampLayouts(), ", "),
		err,
	)
}

const (
	ransomwareLiveAPI = "https://api-pro.ransomware.live"

	maxAPIIdentityFieldRunes    = 256
	maxAPIActivityFieldRunes    = 256
	maxAPITimestampFieldRunes   = 128
	maxAPIDescriptionFieldRunes = 5000
	maxAPIURLFieldRunes         = 2048
	maxAPIVictimsPerResponse    = 1000
)

// RansomwareEntry is kept here as a compatibility alias; the stable model is
// owned by internal/model, not the HTTP API adapter.
type RansomwareEntry = model.RansomwareEntry

type ransomwareResponse struct {
	Victims []ransomwareAPIEntry `json:"victims"`
}

type ransomwareAPIEntry struct {
	ID          string            `json:"id"`
	Group       string            `json:"group"`
	Victim      string            `json:"victim"`
	Country     string            `json:"country"`
	Activity    string            `json:"activity"`
	AttackDate  string            `json:"attackdate"`
	Discovered  ransomwareAPITime `json:"discovered"`
	ClaimURL    string            `json:"post_url"`
	URL         string            `json:"website"`
	Description string            `json:"description"`
	Screenshot  string            `json:"screenshot"`
	Published   ransomwareAPITime `json:"published"`
}

func (entry ransomwareAPIEntry) toRansomwareEntry() RansomwareEntry {
	return RansomwareEntry{
		ID:          entry.ID,
		Group:       entry.Group,
		Victim:      entry.Victim,
		Country:     entry.Country,
		Activity:    entry.Activity,
		AttackDate:  entry.AttackDate,
		Discovered:  entry.Discovered.Time,
		ClaimURL:    entry.ClaimURL,
		WebsiteURL:  entry.URL,
		Description: entry.Description,
		Screenshot:  entry.Screenshot,
		Published:   entry.Published.Time,
	}
}

// DisplayRansomwareTitle returns a stable human-facing identity for an entry.
func DisplayRansomwareTitle(entry RansomwareEntry) string {
	return model.DisplayRansomwareTitle(entry)
}

type ransomwareLatestResponse struct {
	Victims json.RawMessage `json:"victims"`
}

// GetLatestEntries fetches the latest ransomware entries from the API
//
// Returns all entries from the API. Deduplication is handled by the caller
// using the status tracker's IsAPIItemSentToWebhook method.
func (c *Client) GetLatestEntries(ctx context.Context) ([]RansomwareEntry, error) {
	start := time.Now()
	baseURL := c.baseURL
	if baseURL == "" {
		baseURL = ransomwareLiveAPI
	}
	url := baseURL + "/victims/recent"

	log.WithField("url", url).Debug("Fetching latest ransomware entries")

	var response ransomwareLatestResponse
	err := c.makeRequest(ctx, url, &response)

	// Log the request
	c.logRequest(url, time.Since(start), err)

	if err != nil {
		return nil, err
	}
	if len(response.Victims) == 0 || string(response.Victims) == "null" {
		return nil, errors.New("ransomware API response missing required victims field")
	}

	var rawVictims []json.RawMessage
	if err := json.Unmarshal(response.Victims, &rawVictims); err != nil {
		return nil, fmt.Errorf("failed to decode victims field: %w", err)
	}
	if len(rawVictims) > maxAPIVictimsPerResponse {
		log.WithFields(log.Fields{
			"total_victims": len(rawVictims),
			"kept_victims":  maxAPIVictimsPerResponse,
		}).Warn("Ransomware API victim count exceeds processing cap")
		rawVictims = rawVictims[:maxAPIVictimsPerResponse]
	}
	validVictims := make([]RansomwareEntry, 0, len(rawVictims))
	invalidCount := 0
	for i, rawVictim := range rawVictims {
		var apiEntry ransomwareAPIEntry
		if err := json.Unmarshal(rawVictim, &apiEntry); err != nil {
			invalidCount++
			log.WithError(err).WithField("index", i).Warn("Skipping undecodable ransomware API entry")
			continue
		}
		entry := normalizeRansomwareEntry(apiEntry.toRansomwareEntry())
		if err := validateRansomwareEntry(entry); err != nil {
			invalidCount++
			log.WithError(err).WithField("index", i).Warn("Skipping malformed ransomware API entry")
			continue
		}
		validVictims = append(validVictims, entry)
	}
	if len(rawVictims) > 0 && len(validVictims) == 0 {
		return nil, fmt.Errorf("ransomware API response contained no valid victims (%d invalid)", invalidCount)
	}

	log.WithFields(log.Fields{
		"total_entries":   len(validVictims),
		"invalid_entries": invalidCount,
	}).Info("Processed ransomware API response")

	return validVictims, nil
}

func normalizeRansomwareEntry(entry RansomwareEntry) RansomwareEntry {
	entry.ID = textutil.TruncateText(strings.TrimSpace(entry.ID), maxAPIIdentityFieldRunes)
	entry.Group = textutil.TruncateText(strings.TrimSpace(entry.Group), maxAPIIdentityFieldRunes)
	entry.Victim = textutil.TruncateText(strings.TrimSpace(entry.Victim), maxAPIIdentityFieldRunes)
	entry.Country = normalizedAPICountryCode(entry.Country)
	entry.Activity = textutil.TruncateText(strings.TrimSpace(entry.Activity), maxAPIActivityFieldRunes)
	entry.AttackDate = textutil.TruncateText(strings.TrimSpace(entry.AttackDate), maxAPITimestampFieldRunes)
	entry.ClaimURL = boundedAPIURLField(entry.ClaimURL)
	entry.WebsiteURL = boundedAPIURLField(entry.WebsiteURL)
	entry.Description = textutil.TruncateText(strings.TrimSpace(entry.Description), maxAPIDescriptionFieldRunes)
	entry.Screenshot = boundedAPIURLField(entry.Screenshot)
	return entry
}

// normalizedAPICountryCode validates the API's raw country value against ISO
// 3166-1 instead of blindly truncating it to 2 runes. golang.org/x/text's
// language.ParseRegion accepts both alpha-2 and alpha-3 codes (any case) and
// canonicalizes a recognised one to its alpha-2 form (e.g. "AUT" -> "AT"),
// which is what the old blind truncate-to-2 got wrong: it turned "AUT"
// (Austria) into "AU" -- a different, real country's own ISO2 code
// (Australia), not a degraded-but-safe "unknown". Anything that is not a
// recognised country region (IsCountry() false, e.g. groups like "EU" or
// private-use codes) is cleared to "" -- an absent country is correct, a
// wrong one is not. Mirrors boundedAPIURLField's reject-to-empty idiom for
// the same class of problem (an oversized/malformed value that looks valid
// but points somewhere wrong).
func normalizedAPICountryCode(rawCountry string) string {
	code := strings.TrimSpace(rawCountry)
	if code == "" {
		return ""
	}
	region, err := language.ParseRegion(code)
	if err != nil || !region.IsCountry() {
		return ""
	}
	return region.String()
}

func boundedAPIURLField(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if len([]rune(rawURL)) > maxAPIURLFieldRunes {
		return ""
	}
	return rawURL
}

func validateRansomwareEntry(entry RansomwareEntry) error {
	if strings.TrimSpace(entry.ID) != "" {
		return nil
	}
	if strings.TrimSpace(entry.Group) == "" || strings.TrimSpace(entry.Victim) == "" {
		return errors.New("entry without id must include group and victim")
	}
	if entry.Country != "" && !isISO2CountryCode(entry.Country) {
		return fmt.Errorf("invalid country code %q", entry.Country)
	}
	if strings.TrimSpace(entry.AttackDate) == "" &&
		strings.TrimSpace(entry.ClaimURL) == "" &&
		strings.TrimSpace(entry.WebsiteURL) == "" &&
		entry.Discovered.IsZero() &&
		entry.Published.IsZero() {
		return errors.New("entry without id must include at least one stable fallback discriminator")
	}
	return nil
}

func isISO2CountryCode(country string) bool {
	if len(country) != 2 {
		return false
	}
	return country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z'
}

// GenerateEntryKey creates a unique key for an entry to use in deduplication
//
// Key generation strategy (in priority order):
// 1. Use API-provided ID if available (most reliable)
// 2. Fallback to a versioned content hash over group, victim, country,
// attack date, URLs/media, description, and normalized API timestamps.
//
// Fallback fields are hashed with NUL separators so field contents cannot
// collide through separator injection. GenerateEntryLookupKeys also returns
// the legacy group|victim|country|attackdate fallback for old status files.
func GenerateEntryKey(entry RansomwareEntry) string {
	return model.GenerateRansomwareEntryKey(entry)
}

// GenerateEntryLookupKeys returns the primary key plus legacy keys that may
// already exist in persisted status files from older versions.
func GenerateEntryLookupKeys(entry RansomwareEntry) []string {
	return model.GenerateRansomwareEntryLookupKeys(entry)
}
