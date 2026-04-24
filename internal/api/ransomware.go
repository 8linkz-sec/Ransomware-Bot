// Package api provides ransomware.live API integration with two-phase tracking
// for reliable data processing and Discord delivery.
//
// Two-Phase Processing:
// Phase 1: Fetch and store entries from API (prevents data loss)
// Phase 2: Send stored entries to Discord (with retry capability)
//
// This separation ensures no data is lost if Discord delivery fails,
// enabling recovery and retry mechanisms.
package api

import (
	"context"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// CustomTime handles the API's inconsistent time formats
//
// The ransomware.live API returns timestamps in two possible formats:
// - With microseconds: "2025-08-02 12:52:04.158280"
// - Without microseconds: "2025-08-02 12:52:04"
//
// This custom unmarshaler handles both formats gracefully.
type CustomTime struct {
	time.Time
}

// UnmarshalJSON parses the API's time format
func (ct *CustomTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), "\"")
	if s == "null" || s == "" {
		ct.Time = time.Time{}
		return nil
	}

	// Parse the API format: "2025-08-02 12:52:04.158280"
	t, err := time.Parse("2006-01-02 15:04:05.999999", s)
	if err != nil {
		// Fallback to format without microseconds: "2025-08-02 12:52:04"
		t, err = time.Parse("2006-01-02 15:04:05", s)
		if err != nil {
			return err
		}
	}
	ct.Time = t
	return nil
}

const (
	// RansomwareLiveAPI is the base URL for the ransomware.live API
	RansomwareLiveAPI = "https://api-pro.ransomware.live"
)

// RansomwareEntry represents a single ransomware incident from the API
type RansomwareEntry struct {
	ID          string     `json:"id"`
	Group       string     `json:"group"`
	Victim      string     `json:"victim"`
	Country     string     `json:"country"`
	Activity    string     `json:"activity"`
	AttackDate  string     `json:"attackdate"`
	Discovered  CustomTime `json:"discovered"`
	ClaimURL    string     `json:"post_url"` // Maps to post_url from API
	URL         string     `json:"website"`  // Maps to website from API
	Description string     `json:"description"`
	Screenshot  string     `json:"screenshot"`
	Published   CustomTime `json:"published"`
}

// RansomwareResponse represents the API response structure
type RansomwareResponse struct {
	Client  string            `json:"client"`
	Count   int               `json:"count"`
	Order   string            `json:"order"`
	Victims []RansomwareEntry `json:"victims"`
}

// GetLatestEntries fetches the latest ransomware entries from the API
//
// Returns all entries from the API. Deduplication is handled by the caller
// using the status tracker's IsAPIItemSentToWebhook method.
func (c *Client) GetLatestEntries(ctx context.Context) ([]RansomwareEntry, error) {
	start := time.Now()
	url := RansomwareLiveAPI + "/victims/recent"

	log.WithField("url", url).Debug("Fetching latest ransomware entries")

	var response RansomwareResponse
	err := c.makeRequest(ctx, url, &response)

	// Log the request
	c.logRequest(url, time.Since(start), err)

	if err != nil {
		return nil, err
	}

	log.WithFields(log.Fields{
		"total_entries": len(response.Victims),
	}).Info("Processed ransomware API response")

	return response.Victims, nil
}

// GenerateEntryKey creates a unique key for an entry to use in deduplication
//
// Key generation strategy (in priority order):
// 1. Use API-provided ID if available (most reliable)
// 2. Fallback to composite key: group + victim + discovery time
//
// This ensures consistent deduplication even if API format changes.
func GenerateEntryKey(entry RansomwareEntry) string {
	// Use ID if available
	if entry.ID != "" {
		return "id:" + entry.ID
	}

	// Use a stable combination with unambiguous separator and all fields always present
	// to avoid key collisions from field values containing the separator
	return entry.Group + "|" + entry.Victim + "|" + entry.Country + "|" + entry.AttackDate
}
