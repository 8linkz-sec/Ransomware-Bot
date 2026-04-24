package status

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Tracker handles status tracking and persistence with separate files for API and RSS
type Tracker struct {
	apiStatus   *APIStatus
	rssStatus   *RSSStatus
	retryStatus *RetryStatus
	dataDir     string
	mutex       sync.RWMutex

	// Performance: O(1) lookup index for parsed RSS items
	// Key: feedURL + "\x00" + itemKey → index in ParsedItems slice
	parsedIndex map[string]int

	// Dirty flags for batched persistence (reduces disk I/O from per-item to per-batch)
	rssDirty        bool // RSS status needs saving to disk
	apiDirty        bool // API status needs saving to disk
	retryDirty      bool // Retry status needs saving to disk
	parsedSortDirty bool // ParsedItems need re-sorting before read/save
}

// RetryStatus holds the persistent retry queue and dead letter storage.
type RetryStatus struct {
	LastUpdated    time.Time              `json:"last_updated"`
	RetryQueue     map[string]RetryItem   `json:"retry_queue"`      // compositeKey → item
	DeadLetterItems []DeadLetterItem      `json:"dead_letter_items"`
}

// RetryItem represents a failed webhook send queued for retry.
type RetryItem struct {
	ItemKey     string          `json:"item_key"`
	WebhookURL  string          `json:"-"`                    // not persisted (security)
	Messenger   string          `json:"messenger"`            // "discord" or "slack"
	ItemType    string          `json:"item_type"`            // "api" or "rss"
	Title       string          `json:"title"`
	RetryCount  int             `json:"retry_count"`
	LastError   string          `json:"last_error"`
	FirstFailed string          `json:"first_failed"`
	LastRetried string          `json:"last_retried"`
	Payload     json.RawMessage `json:"payload,omitempty"`    // Serialized entry data for retry (API items only)
}

// DeadLetterItem is a send that permanently failed after exhausting all retries.
type DeadLetterItem struct {
	ItemKey     string `json:"item_key"`
	ItemType    string `json:"item_type"`
	Messenger   string `json:"messenger"`
	Title       string `json:"title"`
	RetryCount  int    `json:"retry_count"`
	LastError   string `json:"last_error"`
	FirstFailed string `json:"first_failed"`
	DeadAt      string `json:"dead_at"`
}

// APIStatus represents status for ransomware API with single-source tracking
// Only SentItems is used for tracking - items are checked directly against sent status
type APIStatus struct {
	LastUpdated  time.Time                  `json:"last_updated"`
	LastCheck    time.Time                  `json:"last_check"`
	LastSuccess  *time.Time                 `json:"last_success"`
	LastError    *string                    `json:"last_error"`
	EntriesFound int                        `json:"entries_found"`
	SentItems    map[string]WebhookSentInfo `json:"sent_items"` // Items sent to webhooks (per webhook URL)
}

// WebhookSentInfo contains information about items sent to webhooks
type WebhookSentInfo struct {
	Title      string    `json:"title"`
	SentAt     time.Time `json:"sent_at"`
	ItemKey    string    `json:"item_key"`     // Added for reverse lookup
	WebhookURL string    `json:"-"`            // Not stored in JSON (security) - only used in memory
}

// RSSStatus represents status for RSS feeds with two-phase tracking
type RSSStatus struct {
	LastUpdated time.Time                `json:"last_updated"`
	Feeds       map[string]FeedInfo      `json:"feeds"`
	ParsedItems []StoredRSSEntry         `json:"parsed_items"`
	SentItems   map[string]RSSWebhookSentInfo `json:"sent_items"` // Per webhook tracking
}

// RSSWebhookSentInfo contains information about RSS items sent to webhooks
type RSSWebhookSentInfo struct {
	Title      string    `json:"title"`       // RSS Article Title
	FeedTitle  string    `json:"feed_title"`  // RSS Feed Name (extra info)
	SentAt     time.Time `json:"sent_at"`
	ItemKey    string    `json:"item_key"`    // Added for reverse lookup
	WebhookURL string    `json:"-"`           // Not stored in JSON (security) - only used in memory
}

// StoredRSSEntry represents a complete RSS entry for storage
type StoredRSSEntry struct {
	Key         string   `json:"key"`      // Unique key for tracking
	FeedURL     string   `json:"feed_url"` // Which feed this came from
	Title       string   `json:"title"`
	Link        string   `json:"link"`
	Description string   `json:"description"`
	Published   string   `json:"published"` // Store as string for JSON
	Author      string   `json:"author"`
	Categories  []string `json:"categories"`
	GUID        string   `json:"guid"`
	FeedTitle   string   `json:"feed_title"`
	ParsedAt    string   `json:"parsed_at"` // When it was parsed
}

// makeCompositeKey creates a secure composite key from itemKey and webhookURL
// Uses SHA256 hash to prevent issues with special characters in keys
// Optimized to avoid string concatenation allocations
func makeCompositeKey(itemKey, webhookURL string) string {
	h := sha256.New()
	h.Write([]byte(itemKey))
	h.Write([]byte{0}) // null separator
	h.Write([]byte(webhookURL))
	return hex.EncodeToString(h.Sum(nil))
}

// parsedIndexKey creates a lookup key for the parsedIndex map
func parsedIndexKey(feedURL, itemKey string) string {
	return feedURL + "\x00" + itemKey
}

// NewTracker creates a new status tracker instance with separate files
func NewTracker(dataDir string) (*Tracker, error) {
	// Create data directory if it doesn't exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	tracker := &Tracker{
		dataDir:     dataDir,
		apiStatus:   NewAPIStatus(),
		rssStatus:   NewRSSStatus(),
		retryStatus: NewRetryStatus(),
		parsedIndex: make(map[string]int),
	}

	// Load existing status files if they exist
	if err := tracker.loadAPIStatus(); err != nil {
		log.WithError(err).Warn("Failed to load existing API status, starting fresh")
	}

	if err := tracker.loadRSSStatus(); err != nil {
		log.WithError(err).Warn("Failed to load existing RSS status, starting fresh")
	}

	if err := tracker.loadRetryStatus(); err != nil {
		log.WithError(err).Warn("Failed to load existing retry status, starting fresh")
	}

	return tracker, nil
}

// NewRetryStatus creates a new empty retry status structure.
func NewRetryStatus() *RetryStatus {
	return &RetryStatus{
		LastUpdated:     time.Now(),
		RetryQueue:      make(map[string]RetryItem),
		DeadLetterItems: make([]DeadLetterItem, 0),
	}
}

// NewAPIStatus creates a new empty API status structure
func NewAPIStatus() *APIStatus {
	return &APIStatus{
		LastUpdated: time.Now(),
		SentItems:   make(map[string]WebhookSentInfo),
	}
}

// NewRSSStatus creates a new empty RSS status structure
func NewRSSStatus() *RSSStatus {
	return &RSSStatus{
		LastUpdated: time.Now(),
		Feeds:       make(map[string]FeedInfo),
		ParsedItems: make([]StoredRSSEntry, 0),
		SentItems:   make(map[string]RSSWebhookSentInfo),
	}
}

// UpdateFeedStatus updates the status for a specific RSS feed
func (t *Tracker) UpdateFeedStatus(feedURL string, success bool, entriesFound int, errorMsg string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	now := time.Now()

	// Get existing feed info or create new one
	feedInfo, exists := t.rssStatus.Feeds[feedURL]
	if !exists {
		feedInfo = FeedInfo{
			SuccessRate: 1.0, // Start with 100% success rate
		}
	}

	// Update check time
	feedInfo.LastCheck = now
	feedInfo.EntriesFound = entriesFound

	if success {
		// Update success information
		feedInfo.LastSuccess = &now
		feedInfo.LastError = nil

		// Calculate new success rate (simple moving average approach)
		if exists {
			feedInfo.SuccessRate = (feedInfo.SuccessRate * 0.9) + (1.0 * 0.1)
		} else {
			feedInfo.SuccessRate = 1.0
		}
	} else {
		// Update error information
		feedInfo.LastError = &errorMsg

		// Calculate new success rate with failure
		if exists {
			feedInfo.SuccessRate = feedInfo.SuccessRate * 0.9 // Reduce success rate
		} else {
			feedInfo.SuccessRate = 0.0
		}
	}

	// Store updated feed info
	t.rssStatus.Feeds[feedURL] = feedInfo
	t.rssStatus.LastUpdated = now
	t.rssDirty = true
}

// UpdateAPIStatus updates the status for the ransomware API
func (t *Tracker) UpdateAPIStatus(success bool, entriesFound int, errorMsg string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	now := time.Now()

	// Update API check time
	t.apiStatus.LastCheck = now
	t.apiStatus.EntriesFound = entriesFound

	if success {
		// Update success information
		t.apiStatus.LastSuccess = &now
		t.apiStatus.LastError = nil
	} else {
		// Update error information
		t.apiStatus.LastError = &errorMsg
	}

	t.apiStatus.LastUpdated = now
	t.apiDirty = true
}

// IsAPIItemSentToWebhook checks if an API item was already sent to a specific webhook
func (t *Tracker) IsAPIItemSentToWebhook(itemKey, webhookURL string) bool {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	compositeKey := makeCompositeKey(itemKey, webhookURL)
	_, exists := t.apiStatus.SentItems[compositeKey]
	return exists
}

// MarkAPIItemSent marks an API item as sent to a webhook
func (t *Tracker) MarkAPIItemSent(itemKey, itemTitle, webhookURL string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Ensure SentItems map exists
	if t.apiStatus.SentItems == nil {
		t.apiStatus.SentItems = make(map[string]WebhookSentInfo)
	}

	// Create composite key using secure hash
	compositeKey := makeCompositeKey(itemKey, webhookURL)

	// Mark item as sent to this specific webhook
	t.apiStatus.SentItems[compositeKey] = WebhookSentInfo{
		Title:      itemTitle,
		SentAt:     time.Now(),
		ItemKey:    itemKey,
		WebhookURL: webhookURL,
	}
	t.apiStatus.LastUpdated = time.Now()
	t.apiDirty = true
}


// RSS Two-Phase Tracking Methods

// IsRSSItemParsed checks if an RSS item was already parsed from feed (O(1) via index)
func (t *Tracker) IsRSSItemParsed(feedURL, itemKey string) bool {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	_, exists := t.parsedIndex[parsedIndexKey(feedURL, itemKey)]
	return exists
}

// MarkRSSItemParsed marks an RSS item as parsed from feed with complete entry data.
// Uses O(1) index lookup for duplicates and defers sorting/saving for batch efficiency.
func (t *Tracker) MarkRSSItemParsed(feedURL, itemKey string, entry StoredRSSEntry) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Ensure ParsedItems slice exists
	if t.rssStatus.ParsedItems == nil {
		t.rssStatus.ParsedItems = make([]StoredRSSEntry, 0)
	}

	entry.Key = itemKey
	entry.FeedURL = feedURL
	entry.ParsedAt = time.Now().Format("2006-01-02 15:04:05.999999")

	idxKey := parsedIndexKey(feedURL, itemKey)
	if idx, exists := t.parsedIndex[idxKey]; exists {
		// Update existing entry at known index (O(1))
		t.rssStatus.ParsedItems[idx] = entry
	} else {
		// Append new entry and record index (O(1))
		t.parsedIndex[idxKey] = len(t.rssStatus.ParsedItems)
		t.rssStatus.ParsedItems = append(t.rssStatus.ParsedItems, entry)
	}

	t.rssStatus.LastUpdated = time.Now()
	t.parsedSortDirty = true
	t.rssDirty = true
}

// MarkRSSItemSentToWebhook marks an RSS item as sent to a specific webhook
func (t *Tracker) MarkRSSItemSentToWebhook(itemKey, itemTitle, feedTitle, webhookURL string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Ensure SentItems map exists
	if t.rssStatus.SentItems == nil {
		t.rssStatus.SentItems = make(map[string]RSSWebhookSentInfo)
	}

	// Create secure composite key using SHA256 hash
	compositeKey := makeCompositeKey(itemKey, webhookURL)

	// Mark item as sent to this specific webhook
	t.rssStatus.SentItems[compositeKey] = RSSWebhookSentInfo{
		Title:      itemTitle,
		FeedTitle:  feedTitle,
		SentAt:     time.Now(),
		ItemKey:    itemKey,    // Store for reverse lookup
		WebhookURL: webhookURL, // Store for lookups
	}
	t.rssStatus.LastUpdated = time.Now()
	t.rssDirty = true
}

// IsRSSItemSentToWebhook checks if an RSS item was already sent to a specific webhook
func (t *Tracker) IsRSSItemSentToWebhook(itemKey, webhookURL string) bool {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	compositeKey := makeCompositeKey(itemKey, webhookURL)
	_, exists := t.rssStatus.SentItems[compositeKey]
	return exists
}

// GetUnsentRSSItemsForWebhook returns RSS entries not yet sent to a specific webhook, sorted chronologically.
// Uses write lock to allow deferred sorting if needed.
func (t *Tracker) GetUnsentRSSItemsForWebhook(webhookURL string) []StoredRSSEntry {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.ensureSorted()

	var unsent []StoredRSSEntry
	for _, entry := range t.rssStatus.ParsedItems {
		compositeKey := makeCompositeKey(entry.Key, webhookURL)
		if _, exists := t.rssStatus.SentItems[compositeKey]; !exists {
			unsent = append(unsent, entry)
		}
	}

	log.WithFields(log.Fields{
		"unsent_count": len(unsent),
	}).Debug("Retrieved unsent RSS items for webhook")
	return unsent
}

// ensureSorted sorts ParsedItems if the sort-dirty flag is set, then rebuilds the index.
// NOTE: Must be called with t.mutex locked (write).
func (t *Tracker) ensureSorted() {
	if !t.parsedSortDirty {
		return
	}
	t.sortRSSParsedItems()
	t.rebuildParsedIndex()
	t.parsedSortDirty = false
}

// rebuildParsedIndex rebuilds the O(1) lookup index from the ParsedItems slice.
// NOTE: Must be called with t.mutex locked.
func (t *Tracker) rebuildParsedIndex() {
	t.parsedIndex = make(map[string]int, len(t.rssStatus.ParsedItems))
	for i, entry := range t.rssStatus.ParsedItems {
		t.parsedIndex[parsedIndexKey(entry.FeedURL, entry.Key)] = i
	}
}

// SavePendingChanges flushes any dirty in-memory state to disk.
// Call this after batch operations (parse cycle, send cycle) to persist changes.
func (t *Tracker) SavePendingChanges() {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.rssDirty {
		t.ensureSorted()
		if err := t.saveRSSStatus(); err != nil {
			log.WithError(err).Error("Failed to save pending RSS status")
		}
		t.rssDirty = false
	}

	if t.apiDirty {
		if err := t.saveAPIStatus(); err != nil {
			log.WithError(err).Error("Failed to save pending API status")
		}
		t.apiDirty = false
	}

	if t.retryDirty {
		if err := t.saveRetryStatus(); err != nil {
			log.WithError(err).Error("Failed to save pending retry status")
		}
		t.retryDirty = false
	}
}

// sortRSSParsedItems sorts the parsed RSS items by published time (oldest first)
func (t *Tracker) sortRSSParsedItems() {
	// Pre-parse all times to avoid parsing during sort comparison
	type itemWithTime struct {
		item StoredRSSEntry
		time time.Time
	}

	timeFormats := []string{
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC1123Z,
		time.RFC1123,
		"Mon, 02 Jan 2006 15:04:05 -0700",
		"2006-01-02T15:04:05Z",
		"2006-01-02",
	}

	items := make([]itemWithTime, len(t.rssStatus.ParsedItems))
	for i, entry := range t.rssStatus.ParsedItems {
		var parsedTime time.Time
		for _, format := range timeFormats {
			if t, err := time.Parse(format, entry.Published); err == nil {
				parsedTime = t
				break
			}
		}
		// If no format matched, fall back to ParsedAt which is always in a known format
		if parsedTime.IsZero() && entry.ParsedAt != "" {
			parsedTime, _ = time.Parse("2006-01-02 15:04:05.999999", entry.ParsedAt)
		}
		items[i] = itemWithTime{entry, parsedTime}
	}

	// Sort by pre-parsed times
	sort.Slice(items, func(i, j int) bool {
		return items[i].time.Before(items[j].time)
	})

	// Reconstruct sorted slice
	for i, item := range items {
		t.rssStatus.ParsedItems[i] = item.item
	}
}

// CleanupOldEntries performs cleanup of old entries for both API and RSS status.
// This should be called once after processing a batch of items, not after each individual item.
// This approach is much more efficient than cleaning up after every single item (O(n) vs O(n*m)).
func (t *Tracker) CleanupOldEntries() {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Cleanup API sent items
	t.cleanupOldAPISentItems()

	// Cleanup RSS items (synchronized to prevent orphaned entries)
	t.cleanupRSSItemsSynchronized()

	log.Debug("Completed batch cleanup of old entries")
}

// cleanupRSSItemsSynchronized performs synchronized cleanup of both parsed_items and sent_items
// to prevent orphaned entries that could cause duplicate sends.
// NOTE: Must be called with t.mutex locked
func (t *Tracker) cleanupRSSItemsSynchronized() {
	const maxItems = 10000

	initialParsedCount := len(t.rssStatus.ParsedItems)
	initialSentCount := len(t.rssStatus.SentItems)

	log.WithFields(log.Fields{
		"parsed_items_count": initialParsedCount,
		"sent_items_count":   initialSentCount,
		"max_items":          maxItems,
	}).Debug("Starting synchronized RSS cleanup")

	// If parsed_items is under limit, nothing to do
	if initialParsedCount <= maxItems {
		log.Debug("RSS cleanup not needed - under limit")
		return
	}

	// Keep only the newest maxItems in parsed_items
	// (list is already sorted by time - oldest first)
	startIdx := initialParsedCount - maxItems
	itemsToRemove := t.rssStatus.ParsedItems[:startIdx]
	t.rssStatus.ParsedItems = t.rssStatus.ParsedItems[startIdx:]

	log.WithFields(log.Fields{
		"items_to_remove":   len(itemsToRemove),
		"oldest_item_key":   itemsToRemove[0].Key,
		"oldest_item_title": itemsToRemove[0].Title,
	}).Debug("Removing old parsed items")

	// Remove corresponding entries from sent_items to keep lists synchronized
	sentItemsRemoved := 0
	for _, removedItem := range itemsToRemove {
		// Find and delete all sent_items that belong to this parsed_item
		for compositeKey, sentInfo := range t.rssStatus.SentItems {
			if sentInfo.ItemKey == removedItem.Key {
				delete(t.rssStatus.SentItems, compositeKey)
				sentItemsRemoved++
				log.WithFields(log.Fields{
					"item_key":      removedItem.Key,
					"composite_key": compositeKey,
				}).Debug("Removed sent_item entry for deleted parsed_item")
			}
		}
	}

	// Rebuild index after slice modification
	t.rebuildParsedIndex()
	t.rssDirty = true

	log.WithFields(log.Fields{
		"parsed_items_removed": len(itemsToRemove),
		"sent_items_removed":   sentItemsRemoved,
		"parsed_items_kept":    len(t.rssStatus.ParsedItems),
		"sent_items_kept":      len(t.rssStatus.SentItems),
	}).Info("Synchronized RSS cleanup completed")
}

// Legacy RSS methods for backward compatibility

// IsItemProcessed checks if an RSS item was already processed (legacy method)
func (t *Tracker) IsItemProcessed(feedURL, itemKey string) bool {
	// Delegate to new two-phase method - check if parsed
	return t.IsRSSItemParsed(feedURL, itemKey)
}

// MarkItemProcessed marks an RSS item as processed (legacy method)
func (t *Tracker) MarkItemProcessed(feedURL, itemKey, itemTitle string) {
	// For legacy compatibility, we'll just mark as parsed
	// Note: This doesn't create a full StoredRSSEntry, so it's limited
	log.WithFields(log.Fields{
		"feed_url":   feedURL,
		"item_key":   itemKey,
		"item_title": itemTitle,
	}).Debug("Legacy MarkItemProcessed called - limited functionality")

	// We can't create a full entry without more data, so we'll just log
	// The new two-phase system should be used instead
}

// cleanupOldAPISentItems removes old sent items based on count and age
// NOTE: Must be called with t.mutex locked
func (t *Tracker) cleanupOldAPISentItems() {
	const maxSentItems = 100000       // Keep max 100000 sent API items (increased for multi-webhook support)
	const maxAge = 30 * 24 * time.Hour // Keep max 30 days

	// First, remove items older than maxAge
	now := time.Now()
	removedByAge := 0
	for k, v := range t.apiStatus.SentItems {
		if now.Sub(v.SentAt) > maxAge {
			delete(t.apiStatus.SentItems, k)
			removedByAge++
		}
	}

	if removedByAge > 0 {
		t.apiDirty = true
		log.WithField("removed", removedByAge).Debug("Cleaned up API sent items older than 30 days")
	}

	// Then apply count-based cleanup if still too many
	if len(t.apiStatus.SentItems) > maxSentItems {
		// Create slice of items with timestamps for sorting
		type itemWithTime struct {
			key  string
			info WebhookSentInfo
		}
		items := make([]itemWithTime, 0, len(t.apiStatus.SentItems))
		for k, v := range t.apiStatus.SentItems {
			items = append(items, itemWithTime{k, v})
		}

		// Sort by timestamp (oldest first)
		sort.Slice(items, func(i, j int) bool {
			return items[i].info.SentAt.Before(items[j].info.SentAt)
		})

		// Keep only the most recent maxSentItems
		newMap := make(map[string]WebhookSentInfo, maxSentItems)
		startIdx := len(items) - maxSentItems
		for i := startIdx; i < len(items); i++ {
			newMap[items[i].key] = items[i].info
		}
		t.apiStatus.SentItems = newMap
		t.apiDirty = true

		log.WithFields(log.Fields{
			"removed": len(items) - maxSentItems,
			"kept":    maxSentItems,
		}).Debug("Cleaned up old API sent items by count")
	}
}

// saveAPIStatus saves the API status to JSON file
func (t *Tracker) saveAPIStatus() error {
	filePath := filepath.Join(t.dataDir, "api_status.json")

	data, err := json.MarshalIndent(t.apiStatus, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal API status: %w", err)
	}

	// Write to temporary file first, then rename for atomic update
	tempFile := filePath + ".tmp"
	if err := os.WriteFile(tempFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write temporary API status file: %w", err)
	}

	// Attempt to rename with retry logic (helps on Windows with AV/backup software)
	var renameErr error
	for attempt := 0; attempt < 3; attempt++ {
		renameErr = os.Rename(tempFile, filePath)
		if renameErr == nil {
			break // Success
		}
		log.WithFields(log.Fields{
			"attempt": attempt + 1,
			"error":   renameErr,
		}).Warn("Failed to rename API status file, retrying...")
		time.Sleep(100 * time.Millisecond)
	}

	// Cleanup temp file on failure
	if renameErr != nil {
		if cleanupErr := os.Remove(tempFile); cleanupErr != nil {
			log.WithError(cleanupErr).Error("Failed to cleanup temporary API status file")
		}
		return fmt.Errorf("failed to rename API status file after 3 attempts: %w", renameErr)
	}

	log.WithField("file", filePath).Debug("API status saved to file with sorted entries")
	return nil
}

// saveRSSStatus saves the RSS status to JSON file
func (t *Tracker) saveRSSStatus() error {
	filePath := filepath.Join(t.dataDir, "rss_status.json")

	data, err := json.MarshalIndent(t.rssStatus, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal RSS status: %w", err)
	}

	// Write to temporary file first, then rename for atomic update
	tempFile := filePath + ".tmp"
	if err := os.WriteFile(tempFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write temporary RSS status file: %w", err)
	}

	// Attempt to rename with retry logic (helps on Windows with AV/backup software)
	var renameErr error
	for attempt := 0; attempt < 3; attempt++ {
		renameErr = os.Rename(tempFile, filePath)
		if renameErr == nil {
			break // Success
		}
		log.WithFields(log.Fields{
			"attempt": attempt + 1,
			"error":   renameErr,
		}).Warn("Failed to rename RSS status file, retrying...")
		time.Sleep(100 * time.Millisecond)
	}

	// Cleanup temp file on failure
	if renameErr != nil {
		if cleanupErr := os.Remove(tempFile); cleanupErr != nil {
			log.WithError(cleanupErr).Error("Failed to cleanup temporary RSS status file")
		}
		return fmt.Errorf("failed to rename RSS status file after 3 attempts: %w", renameErr)
	}

	log.WithField("file", filePath).Debug("RSS status saved to file")
	return nil
}

// loadAPIStatus loads API status from JSON file if it exists
func (t *Tracker) loadAPIStatus() error {
	filePath := filepath.Join(t.dataDir, "api_status.json")

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.WithField("file", filePath).Info("API status file does not exist, starting with empty status")
		return nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read API status file: %w", err)
	}

	var loadedStatus APIStatus
	if err := json.Unmarshal(data, &loadedStatus); err != nil {
		return fmt.Errorf("failed to unmarshal API status: %w", err)
	}

	// Ensure data structures exist
	if loadedStatus.SentItems == nil {
		loadedStatus.SentItems = make(map[string]WebhookSentInfo)
	}

	// Lock mutex before modifying shared state
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.apiStatus = &loadedStatus

	log.WithFields(log.Fields{
		"file":         filePath,
		"last_updated": t.apiStatus.LastUpdated,
		"sent_items":   len(t.apiStatus.SentItems),
	}).Info("API status loaded from file")

	return nil
}


// loadRSSStatus loads RSS status from JSON file if it exists
func (t *Tracker) loadRSSStatus() error {
	filePath := filepath.Join(t.dataDir, "rss_status.json")

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.WithField("file", filePath).Info("RSS status file does not exist, starting with empty status")
		return nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read RSS status file: %w", err)
	}

	var loadedStatus RSSStatus
	if err := json.Unmarshal(data, &loadedStatus); err != nil {
		return fmt.Errorf("failed to unmarshal RSS status: %w", err)
	}

	// Ensure data structures exist
	if loadedStatus.Feeds == nil {
		loadedStatus.Feeds = make(map[string]FeedInfo)
	}
	if loadedStatus.ParsedItems == nil {
		loadedStatus.ParsedItems = make([]StoredRSSEntry, 0)
	}
	if loadedStatus.SentItems == nil {
		loadedStatus.SentItems = make(map[string]RSSWebhookSentInfo)
	}

	// Lock mutex before modifying shared state
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Sort parsed items by published time to ensure chronological order
	t.rssStatus = &loadedStatus
	t.sortRSSParsedItems()
	t.rebuildParsedIndex()

	// Build set of sent item keys for correct lookup (SentItems uses composite keys, not item keys)
	sentKeys := make(map[string]bool, len(t.rssStatus.SentItems))
	for _, sentInfo := range t.rssStatus.SentItems {
		sentKeys[sentInfo.ItemKey] = true
	}

	// Count unsent items
	unsentCount := 0
	for _, entry := range t.rssStatus.ParsedItems {
		if !sentKeys[entry.Key] {
			unsentCount++
		}
	}

	log.WithFields(log.Fields{
		"file":         filePath,
		"last_updated": t.rssStatus.LastUpdated,
		"feeds":        len(t.rssStatus.Feeds),
		"parsed_items": len(t.rssStatus.ParsedItems),
		"sent_items":   len(t.rssStatus.SentItems),
		"unsent_items": unsentCount,
	}).Info("RSS status loaded from file in chronological order")

	return nil
}

// --- Retry Queue & Dead Letter Methods ---

// EnqueueRetry adds or updates a failed item in the retry queue.
// Returns true if the item was added/updated, false if it was moved to dead letter.
// Optional payload stores serialized entry data for retry when original source is unavailable.
func (t *Tracker) EnqueueRetry(itemKey, webhookURL, messenger, itemType, title, lastError string, maxAttempts int, retryWindow time.Duration, payload ...[]byte) bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	compositeKey := makeCompositeKey(itemKey, webhookURL)
	now := time.Now()
	nowStr := now.Format("2006-01-02 15:04:05")

	existing, exists := t.retryStatus.RetryQueue[compositeKey]
	if exists {
		existing.RetryCount++
		existing.LastError = lastError
		existing.LastRetried = nowStr
		// Payload is preserved from first enqueue — entry data doesn't change

		// Check if max attempts exceeded
		if maxAttempts > 0 && existing.RetryCount >= maxAttempts {
			t.moveToDeadLetterLocked(compositeKey, existing)
			return false
		}

		// Check if retry window exceeded
		if retryWindow > 0 {
			firstFailed, err := time.Parse("2006-01-02 15:04:05", existing.FirstFailed)
			if err == nil && now.Sub(firstFailed) > retryWindow {
				t.moveToDeadLetterLocked(compositeKey, existing)
				return false
			}
		}

		t.retryStatus.RetryQueue[compositeKey] = existing
	} else {
		item := RetryItem{
			ItemKey:     itemKey,
			WebhookURL:  webhookURL,
			Messenger:   messenger,
			ItemType:    itemType,
			Title:       title,
			RetryCount:  1,
			LastError:   lastError,
			FirstFailed: nowStr,
			LastRetried: nowStr,
		}
		if len(payload) > 0 && payload[0] != nil {
			item.Payload = json.RawMessage(payload[0])
		}
		t.retryStatus.RetryQueue[compositeKey] = item
	}

	t.retryStatus.LastUpdated = now
	t.retryDirty = true
	return true
}

// RemoveFromRetryQueue removes an item from the retry queue (after successful send).
func (t *Tracker) RemoveFromRetryQueue(itemKey, webhookURL string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	compositeKey := makeCompositeKey(itemKey, webhookURL)
	if _, exists := t.retryStatus.RetryQueue[compositeKey]; exists {
		delete(t.retryStatus.RetryQueue, compositeKey)
		t.retryStatus.LastUpdated = time.Now()
		t.retryDirty = true
	}
}

// GetRetryItemsByType returns all items in the retry queue for a given item type,
// regardless of webhook URL (which is not persisted for security).
func (t *Tracker) GetRetryItemsByType(itemType string) []RetryItem {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	var items []RetryItem
	for _, item := range t.retryStatus.RetryQueue {
		if item.ItemType == itemType {
			items = append(items, item)
		}
	}
	return items
}

// moveToDeadLetterLocked moves an item from retry queue to dead letter.
// NOTE: Must be called with t.mutex locked.
func (t *Tracker) moveToDeadLetterLocked(compositeKey string, item RetryItem) {
	t.retryStatus.DeadLetterItems = append(t.retryStatus.DeadLetterItems, DeadLetterItem{
		ItemKey:     item.ItemKey,
		ItemType:    item.ItemType,
		Messenger:   item.Messenger,
		Title:       item.Title,
		RetryCount:  item.RetryCount,
		LastError:   item.LastError,
		FirstFailed: item.FirstFailed,
		DeadAt:      time.Now().Format("2006-01-02 15:04:05"),
	})
	delete(t.retryStatus.RetryQueue, compositeKey)
	t.retryStatus.LastUpdated = time.Now()
	t.retryDirty = true

	log.WithFields(log.Fields{
		"item_key":    item.ItemKey,
		"title":       item.Title,
		"item_type":   item.ItemType,
		"messenger":   item.Messenger,
		"retry_count": item.RetryCount,
		"last_error":  item.LastError,
	}).Warn("Item moved to dead letter after exhausting retries")
}

// --- Retry Persistence ---

// loadRetryStatus loads retry status from JSON file if it exists.
func (t *Tracker) loadRetryStatus() error {
	filePath := filepath.Join(t.dataDir, "retry_status.json")

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.WithField("file", filePath).Info("Retry status file does not exist, starting with empty queue")
		return nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read retry status file: %w", err)
	}

	var loadedStatus RetryStatus
	if err := json.Unmarshal(data, &loadedStatus); err != nil {
		return fmt.Errorf("failed to unmarshal retry status: %w", err)
	}

	if loadedStatus.RetryQueue == nil {
		loadedStatus.RetryQueue = make(map[string]RetryItem)
	}
	if loadedStatus.DeadLetterItems == nil {
		loadedStatus.DeadLetterItems = make([]DeadLetterItem, 0)
	}

	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.retryStatus = &loadedStatus

	log.WithFields(log.Fields{
		"file":              filePath,
		"retry_queue_size":  len(t.retryStatus.RetryQueue),
		"dead_letter_count": len(t.retryStatus.DeadLetterItems),
	}).Info("Retry status loaded from file")

	return nil
}

// saveRetryStatus saves retry status to JSON file.
func (t *Tracker) saveRetryStatus() error {
	filePath := filepath.Join(t.dataDir, "retry_status.json")

	data, err := json.MarshalIndent(t.retryStatus, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal retry status: %w", err)
	}

	tempFile := filePath + ".tmp"
	if err := os.WriteFile(tempFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write temporary retry status file: %w", err)
	}

	var renameErr error
	for attempt := 0; attempt < 3; attempt++ {
		renameErr = os.Rename(tempFile, filePath)
		if renameErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if renameErr != nil {
		if cleanupErr := os.Remove(tempFile); cleanupErr != nil {
			log.WithError(cleanupErr).Error("Failed to cleanup temporary retry status file")
		}
		return fmt.Errorf("failed to rename retry status file after 3 attempts: %w", renameErr)
	}

	return nil
}

