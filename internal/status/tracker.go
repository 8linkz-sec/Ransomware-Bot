package status

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

// Tracker handles status tracking and persistence with separate files for API and RSS
type Tracker struct {
	apiStatus   *apiStatus
	rssStatus   *rssStatus
	retryStore  *retryStore
	dataDir     string
	inMemory    bool
	retention   RetentionPolicy
	auditMu     sync.Mutex
	saveMu      sync.Mutex
	mutex       sync.RWMutex
	auditFlight auditFlight // in-flight deferred audit writes started outside t.mutex; see appendNormalizedDeliveryAuditEvent

	// auditRotation is an immutable snapshot of the audit-log rotation bounds,
	// deliberately decoupled from t.retention (t.mutex-protected): the audit
	// write path never takes t.mutex, so the rotation knobs it needs must be
	// readable without it. Written by setAuditRotationSnapshot from newTracker
	// and UpdateRetention.
	auditRotation atomic.Pointer[auditRotationSnapshot]

	// Performance: O(1) lookup index for parsed RSS items
	// Key: feedURL + "\x00" + itemKey â†’ index in ParsedItems slice
	parsedIndex         map[string]int
	parsedFeedIndex     map[string][]int
	parsedFeedTypeIndex map[string][]int
	parsedUntypedIndex  []int
	apiSentHeap         apiSentItemHeap
	apiLoaded           bool
	apiSaveSkipWarned   bool

	// Dirty flags for batched persistence (reduces disk I/O from per-item to per-batch)
	rssDirty            bool // RSS status needs saving to disk
	apiDirty            bool // API status needs saving to disk
	parsedIndexDirty    bool // Recovery scope indexes need rebuilding before feed-scoped reads
	parsedSortDirty     bool // ParsedItems need re-sorting before read/save
	parsedFullSortDirty bool // ParsedItems need a full sort fallback
	parsedDirtyStart    int  // First appended ParsedItems index that needs tail sorting
}

type apiSentHeapItem struct {
	compositeKey string
	sentAt       time.Time
}

type apiSentItemHeap []apiSentHeapItem

func (h apiSentItemHeap) Len() int {
	return len(h)
}

func (h apiSentItemHeap) Less(i, j int) bool {
	if h[i].sentAt.Equal(h[j].sentAt) {
		return h[i].compositeKey < h[j].compositeKey
	}
	return h[i].sentAt.Before(h[j].sentAt)
}

func (h apiSentItemHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *apiSentItemHeap) Push(value interface{}) {
	item, ok := value.(apiSentHeapItem)
	if !ok {
		panic(fmt.Sprintf("apiSentItemHeap: unexpected element type %T", value))
	}
	*h = append(*h, item)
}

func (h *apiSentItemHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// StatusSummary is a read-only operational overview of persisted bot state.
type StatusSummary struct {
	APISentItems    int
	RSSFeeds        int
	RSSFeedErrors   int
	RSSParsedItems  int
	RSSSentItems    int
	RetryQueueItems int
	DeadLetterItems int
	LastAPIError    string
	LastRSSError    string
}

// TrackerDirtyState reports which persisted status groups have pending writes.
type TrackerDirtyState struct {
	API   bool
	RSS   bool
	Retry bool
}

// APIStatusSnapshot is a read-only copy of API source health state.
type APIStatusSnapshot struct {
	LastUpdated       time.Time
	LastCheck         time.Time
	LastSuccess       *time.Time
	LastError         *string
	LastErrorCategory string
	LastStatusCode    int
	LastRetryable     *bool
	LastTimeout       *bool
	EntriesFound      int
	SentItems         int
}

// APISentItemSnapshot is a read-only copy of one API delivery marker.
type APISentItemSnapshot struct {
	SentAt        time.Time
	DestinationID string
}

// RSSSentItemSnapshot is a read-only copy of one RSS delivery marker.
type RSSSentItemSnapshot struct {
	Title         string
	FeedTitle     string
	SentAt        time.Time
	DestinationID string
	ItemKey       string
	Skipped       bool
	Derived       bool
}

// RSSParsedSortState exposes the internal parsed-item sort invariant for
// focused tests without requiring tests to read Tracker fields directly.
type RSSParsedSortState struct {
	SortDirty     bool
	FullSortDirty bool
	DirtyStart    int
}

// RetryStatusSnapshot is a read-only retry store overview.
type RetryStatusSnapshot struct {
	LastUpdated     time.Time
	RetryQueueItems int
	DeadLetterItems int
}

// DeliveryAuditEvent is one append-only delivery decision record.
//
// DestinationID must be a stable logical destination such as
// "slack.ransomware" or "discord.rss.general". Raw webhook URLs are stripped
// during normalization so bearer credentials cannot be written to audit files.
type DeliveryAuditEvent struct {
	OccurredAt    time.Time         `json:"occurred_at"`
	EventType     string            `json:"event_type"`
	Source        string            `json:"source,omitempty"`
	ItemType      string            `json:"item_type,omitempty"`
	ItemKey       string            `json:"item_key,omitempty"`
	Title         string            `json:"title,omitempty"`
	Messenger     string            `json:"messenger,omitempty"`
	DestinationID string            `json:"destination_id,omitempty"`
	FeedType      string            `json:"feed_type,omitempty"`
	FeedURL       string            `json:"feed_url,omitempty"`
	Outcome       string            `json:"outcome,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	Count         int               `json:"count,omitempty"`
	Details       map[string]string `json:"details,omitempty"`
}

const (
	DeliveryAuditEventAlertCandidate = "alert_candidate"
	DeliveryAuditEventDeliveryState  = "delivery_state"
	DeliveryAuditEventCleanup        = "cleanup"

	DeliveryAuditOutcomeDelivered    = "delivered"
	DeliveryAuditOutcomeFiltered     = "filtered"
	DeliveryAuditOutcomeQuietHours   = "quiet_hours"
	DeliveryAuditOutcomeStale        = "stale"
	DeliveryAuditOutcomeRetryQueued  = "retry_queued"
	DeliveryAuditOutcomeDeadLettered = "dead_lettered"
	DeliveryAuditOutcomePruned       = "pruned"
	DeliveryAuditOutcomeDeduplicated = "deduplicated"

	DeliveryAuditReasonFilterMismatch      = "filter_mismatch"
	DeliveryAuditReasonQuietHours          = "quiet_hours"
	DeliveryAuditReasonStale               = "stale"
	DeliveryAuditReasonWebhookFailure      = "webhook_failure"
	DeliveryAuditReasonRetrySuccess        = "retry_success"
	DeliveryAuditReasonAlreadySent         = "already_sent"
	DeliveryAuditReasonAlreadyDeadLettered = "already_dead_lettered"
	DeliveryAuditReasonRSSRetentionPruned  = "rss_retention_pruned"
	DeliveryAuditReasonSentItemRetention   = "sent_item_retention_pruned"
	// DeliveryAuditReasonQuietHoursCycleCapped is distinct from
	// DeliveryAuditReasonQuietHours (the per-item reason) so an operator can
	// grep specifically for "how much did I not see" during a quiet-hours
	// window that exceeded the per-cycle audit cap.
	DeliveryAuditReasonQuietHoursCycleCapped = "quiet_hours_cycle_capped"
)

const (
	defaultMaxAPISentItems      = 100000
	defaultMaxRSSParsedItems    = 10000
	defaultRSSParsedMaxAge      = 365 * 24 * time.Hour
	defaultMaxRSSSentItems      = 100000
	defaultMaxRetryQueueItems   = 10000
	defaultRetryQueueMaxAge     = 30 * 24 * time.Hour
	defaultMaxDeadLetterItems   = 10000
	defaultDeadLetterMaxAge     = 30 * 24 * time.Hour
	privateDataDirMode          = 0700
	statusAPIFileName           = "api_status.json"
	statusRSSFileName           = "rss_status.json"
	statusRetryFileName         = "retry_status.json"
	statusAuditFileName         = "delivery_audit.jsonl"
	statusTempFileSuffix        = ".tmp"
	statusFileMode              = 0600
	statusRenameAttempts        = 3
	statusRenameRetryDelay      = 100 * time.Millisecond
	statusTimestampLayout       = timeutil.LayoutDateTime
	statusTimestampLayoutMicros = timeutil.LayoutDateTimeMicros
	rssFailureCooldownThreshold = 3
	rssFailureCooldownBase      = 30 * time.Minute
	rssFailureCooldownMax       = 6 * time.Hour
	// maxConsecutiveNotModifiedDuration bounds how long a cached RSS HTTP
	// validator (ETag/Last-Modified) is trusted before it is dropped once,
	// forcing the next poll to be unconditional. Without this, a bogus
	// validator that an origin unconditionally matches -- including one that
	// answers 304 while echoing back exactly the validator it was sent, which
	// RFC 7232 recommends and nginx/Apache/Cloudflare all do -- never
	// self-heals: every following poll reports success with zero entries and
	// the feed silently stops delivering while every health signal stays green.
	//
	// The trip condition is wall-clock time, not a poll count: rss_poll_interval
	// has an enforced floor (1m, config.minPollInterval) but no ceiling, so a
	// count-based threshold has no bounded recovery time (336 polls, the count
	// this replaced, was 7 days at the documented 30m default but 84 days at a
	// legal 6h interval and 168 days at 12h). A fixed 24h duration recovers a
	// stuck feed in about the same time regardless of how the operator has
	// rss_poll_interval configured. The asymmetry is deliberate: an eager trip
	// costs one extra full feed body, once, after which the feed re-caches a
	// validator normally; a lax one costs a silently dead feed for as long as
	// the threshold allows. A well-behaved feed -- any poll that returns
	// entries, or a validator that actually differs from what is stored --
	// resets the streak and never reaches this duration, so this changes
	// nothing for it.
	maxConsecutiveNotModifiedDuration = 24 * time.Hour
	maxAuditStringRunes               = 512
	maxAuditDetails                   = 20
	// defaultAuditLogMaxSizeMB is 60, not the 50 originally proposed: at the
	// measured 569.9 bytes/line, 50 MiB x 20 backups holds only 307-368 days
	// for a heavy deployment, so the count cap binds before the 365-day age
	// cap ever does. 60 MiB x 20 backups delivers 368-441 days (midpoint
	// ~401) while keeping the uncompressed ceiling (60 x 21 = 1,260 MiB,
	// ~1,321 MB decimal -- size a decimal-MB volume for at least 1,322 MB)
	// well under half of what a 50/50 setting would have allowed. This
	// constant is genuinely MiB, not decimal MB, despite its name and the
	// "_MB" JSON key it feeds (int64(...) * 1024 * 1024 in
	// auditRotationSnapshotFromPolicy); corrected 2026-09-04 (audit-retention
	// mutation review, finding F9) after the original comment quoted these
	// day/MB figures as if MaxSizeMB were decimal MB.
	defaultAuditLogMaxSizeMB  = 60
	defaultAuditLogMaxBackups = 20
	defaultAuditLogMaxAgeDays = 365
)

// RetentionPolicy bounds persisted local status data.
type RetentionPolicy struct {
	MaxAPISentItems    int
	MaxRSSParsedItems  int
	RSSParsedMaxAge    time.Duration
	MaxRSSSentItems    int
	MaxRetryQueueItems int
	RetryQueueMaxAge   time.Duration
	MaxDeadLetterItems int
	DeadLetterMaxAge   time.Duration
	AuditLogMaxSizeMB  int
	AuditLogMaxBackups int
	AuditLogMaxAgeDays int
	AuditLogCompress   *bool // nil means "use the default" (true); see normalizeRetentionPolicy
}

// DefaultRetentionPolicy returns the status retention defaults used when no
// explicit policy is configured. Always allocates a fresh *bool for
// AuditLogCompress: a shared package-level pointer would let any caller that
// flips *p.AuditLogCompress silently change the default for every other
// caller.
func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		MaxAPISentItems:    defaultMaxAPISentItems,
		MaxRSSParsedItems:  defaultMaxRSSParsedItems,
		RSSParsedMaxAge:    defaultRSSParsedMaxAge,
		MaxRSSSentItems:    defaultMaxRSSSentItems,
		MaxRetryQueueItems: defaultMaxRetryQueueItems,
		RetryQueueMaxAge:   defaultRetryQueueMaxAge,
		MaxDeadLetterItems: defaultMaxDeadLetterItems,
		DeadLetterMaxAge:   defaultDeadLetterMaxAge,
		AuditLogMaxSizeMB:  defaultAuditLogMaxSizeMB,
		AuditLogMaxBackups: defaultAuditLogMaxBackups,
		AuditLogMaxAgeDays: defaultAuditLogMaxAgeDays,
		AuditLogCompress:   boolPtr(true),
	}
}

func boolPtr(v bool) *bool { return &v }

func normalizeRetentionPolicy(policy RetentionPolicy) RetentionPolicy {
	defaults := DefaultRetentionPolicy()
	if policy.MaxAPISentItems <= 0 {
		policy.MaxAPISentItems = defaults.MaxAPISentItems
	}
	if policy.MaxRSSParsedItems <= 0 {
		policy.MaxRSSParsedItems = defaults.MaxRSSParsedItems
	}
	if policy.RSSParsedMaxAge <= 0 {
		policy.RSSParsedMaxAge = defaults.RSSParsedMaxAge
	}
	if policy.MaxRSSSentItems <= 0 {
		policy.MaxRSSSentItems = defaults.MaxRSSSentItems
	}
	if policy.MaxRetryQueueItems <= 0 {
		policy.MaxRetryQueueItems = defaults.MaxRetryQueueItems
	}
	if policy.RetryQueueMaxAge <= 0 {
		policy.RetryQueueMaxAge = defaults.RetryQueueMaxAge
	}
	if policy.MaxDeadLetterItems <= 0 {
		policy.MaxDeadLetterItems = defaults.MaxDeadLetterItems
	}
	if policy.DeadLetterMaxAge <= 0 {
		policy.DeadLetterMaxAge = defaults.DeadLetterMaxAge
	}
	if policy.AuditLogMaxSizeMB <= 0 {
		policy.AuditLogMaxSizeMB = defaults.AuditLogMaxSizeMB
	}
	if policy.AuditLogMaxBackups <= 0 {
		policy.AuditLogMaxBackups = defaults.AuditLogMaxBackups
	}
	if policy.AuditLogMaxAgeDays <= 0 {
		policy.AuditLogMaxAgeDays = defaults.AuditLogMaxAgeDays
	}
	if policy.AuditLogCompress == nil {
		policy.AuditLogCompress = defaults.AuditLogCompress
	}
	return policy
}

func statusNow() time.Time {
	return time.Now().UTC()
}

// apiStatus represents status for ransomware API with single-source tracking
// Only SentItems is used for tracking - items are checked directly against sent status
type apiStatus struct {
	LastUpdated       time.Time                  `json:"last_updated"`
	LastCheck         time.Time                  `json:"last_check"`
	LastSuccess       *time.Time                 `json:"last_success"`
	LastError         *string                    `json:"last_error"`
	LastErrorCategory string                     `json:"last_error_category,omitempty"`
	LastStatusCode    int                        `json:"last_status_code,omitempty"`
	LastRetryable     *bool                      `json:"last_retryable,omitempty"`
	LastTimeout       *bool                      `json:"last_timeout,omitempty"`
	EntriesFound      int                        `json:"entries_found"`
	SentItems         map[string]webhookSentInfo `json:"sent_items"` // Items sent to webhooks per destination
	FetchedItems      []json.RawMessage          `json:"fetched_items,omitempty"`
}

// webhookSentInfo contains information about items sent to webhooks
type webhookSentInfo struct {
	SentAt        time.Time `json:"sent_at"`
	DestinationID string    `json:"destination_id,omitempty"`
}

// rssStatus represents status for RSS feeds with two-phase tracking
type rssStatus struct {
	LastUpdated time.Time                     `json:"last_updated"`
	Feeds       map[string]FeedInfo           `json:"feeds"`
	ParsedItems []StoredRSSEntry              `json:"parsed_items"`
	SentItems   map[string]rssWebhookSentInfo `json:"sent_items"` // Per webhook tracking
}

// rssWebhookSentInfo contains information about RSS items sent to webhooks
type rssWebhookSentInfo struct {
	Title         string    `json:"title,omitempty"`      // RSS Article Title
	FeedTitle     string    `json:"feed_title,omitempty"` // RSS Feed Name (extra info)
	SentAt        time.Time `json:"sent_at"`
	DestinationID string    `json:"destination_id,omitempty"`
	ItemKey       string    `json:"item_key"`          // Added for reverse lookup
	Skipped       bool      `json:"skipped,omitempty"` // Filter-mismatch marker, not a delivery
	Derived       bool      `json:"derived,omitempty"` // Written for a suppressed entry, not a delivery of its own
}

// StoredRSSEntry represents a complete RSS entry for storage
type StoredRSSEntry struct {
	Key         string   `json:"key"`      // Stable deduplication key for tracking
	FeedURL     string   `json:"feed_url"` // Which feed this came from
	FeedType    string   `json:"feed_type,omitempty"`
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

// UnsentRSSItem is the application-facing recovery view of a parsed RSS item.
type UnsentRSSItem struct {
	Key           string
	FeedURL       string
	FeedType      string
	Title         string
	Link          string
	Description   string
	Published     time.Time
	Author        string
	Categories    []string
	GUID          string
	FeedTitle     string
	InvalidReason string
}

const (
	maxStoredRSSURLRunes         = 2048
	maxStoredRSSTitleRunes       = 512
	maxStoredRSSDescriptionRunes = 5000
	maxStoredRSSAuthorRunes      = 256
	maxStoredRSSCategoryRunes    = 128
	maxStoredRSSCategories       = 25
	maxStoredRSSGUIDRunes        = 512
	maxStoredRSSFeedTitleRunes   = 256
)

// StoredRSSEntryFromRSS converts a normalized RSS entry into the persisted
// status shape while applying status-file bounds.
func StoredRSSEntryFromRSS(entry rss.Entry, key string) StoredRSSEntry {
	published := ""
	if !entry.Published.IsZero() {
		published = entry.Published.Format(time.RFC3339Nano)
	}

	return StoredRSSEntry{
		Key:         key,
		FeedURL:     entry.FeedURL,
		Title:       textutil.TruncateText(entry.Title, maxStoredRSSTitleRunes),
		Link:        boundedRSSURL(entry.Link),
		Description: textutil.TruncateText(entry.Description, maxStoredRSSDescriptionRunes),
		Published:   published,
		Author:      textutil.TruncateText(entry.Author, maxStoredRSSAuthorRunes),
		Categories:  boundedRSSCategories(entry.Categories),
		GUID:        textutil.TruncateText(entry.GUID, maxStoredRSSGUIDRunes),
		FeedTitle:   textutil.TruncateText(entry.FeedTitle, maxStoredRSSFeedTitleRunes),
		ParsedAt:    formatStatusTimestamp(statusNow()),
	}
}

// RSSEntryFromStored converts a persisted RSS status entry back into the
// normalized RSS domain shape used by webhook delivery.
func RSSEntryFromStored(stored StoredRSSEntry) (rss.Entry, error) {
	published, err := ParseStoredRSSTimestamp(stored.Published)
	if err != nil {
		return rss.Entry{}, err
	}

	return rss.Entry{
		Title:       stored.Title,
		Link:        stored.Link,
		Description: stored.Description,
		Published:   published,
		Author:      stored.Author,
		Categories:  cloneStringSlice(stored.Categories),
		GUID:        stored.GUID,
		FeedTitle:   stored.FeedTitle,
		FeedURL:     stored.FeedURL,
	}, nil
}

// UnsentRSSItemFromStored converts a persisted RSS status entry into the
// recovery-domain shape used by scheduler delivery.
func UnsentRSSItemFromStored(stored StoredRSSEntry) UnsentRSSItem {
	published, err := ParseStoredRSSTimestamp(stored.Published)
	item := UnsentRSSItem{
		Key:         stored.Key,
		FeedURL:     stored.FeedURL,
		FeedType:    stored.FeedType,
		Title:       stored.Title,
		Link:        stored.Link,
		Description: stored.Description,
		Published:   published,
		Author:      stored.Author,
		Categories:  cloneStringSlice(stored.Categories),
		GUID:        stored.GUID,
		FeedTitle:   stored.FeedTitle,
	}
	if err != nil {
		item.InvalidReason = err.Error()
	}
	return item
}

func (item UnsentRSSItem) RSSEntry() (rss.Entry, error) {
	if item.InvalidReason != "" {
		return rss.Entry{}, errors.New(item.InvalidReason)
	}
	return rss.Entry{
		Title:       item.Title,
		Link:        item.Link,
		Description: item.Description,
		Published:   item.Published,
		Author:      item.Author,
		Categories:  cloneStringSlice(item.Categories),
		GUID:        item.GUID,
		FeedTitle:   item.FeedTitle,
		FeedURL:     item.FeedURL,
	}, nil
}

// ParseStoredRSSTimestamp accepts both current offset-aware timestamps and
// legacy status timestamps written before RSS dates used RFC3339Nano.
func ParseStoredRSSTimestamp(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}

	if parsed, err := timeutil.ParseFlexibleTimestamp(value); err == nil {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("invalid stored RSS published timestamp %q", value)
}

func boundedRSSURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if len([]rune(rawURL)) > maxStoredRSSURLRunes {
		return ""
	}
	return rawURL
}

func boundedRSSCategories(categories []string) []string {
	limit := len(categories)
	if limit > maxStoredRSSCategories {
		limit = maxStoredRSSCategories
	}
	bounded := make([]string, 0, limit)
	for _, category := range categories {
		if len(bounded) >= maxStoredRSSCategories {
			break
		}
		category = strings.TrimSpace(category)
		if category == "" {
			continue
		}
		bounded = append(bounded, textutil.TruncateText(category, maxStoredRSSCategoryRunes))
	}
	return bounded
}

// makeCompositeKey creates a secure composite key from itemKey and destination.
// Uses SHA256 hash to prevent issues with special characters in keys.
// Optimized to avoid string concatenation allocations
func makeCompositeKey(itemKey, destination string) string {
	h := sha256.New()
	h.Write([]byte(itemKey))
	h.Write([]byte{0}) // null separator
	h.Write([]byte(destination))
	return hex.EncodeToString(h.Sum(nil))
}

// parsedIndexKey creates a lookup key for the parsedIndex map
func parsedIndexKey(feedURL, itemKey string) string {
	return feedURL + "\x00" + itemKey
}

// NewTracker creates a status tracker backed by JSON files in dataDir.
//
// It creates dataDir when missing, removes stale writer temp files, and loads
// API, RSS, and retry status files when they exist. Load or parse failures are
// logged and the affected state starts fresh in memory rather than failing
// construction; directory creation and temp-file cleanup errors are returned.
func NewTracker(dataDir string) (*Tracker, error) {
	return NewTrackerWithRetention(dataDir, DefaultRetentionPolicy())
}

// NewTrackerWithRetention creates a status tracker with explicit retention
// bounds for persisted local status data.
func NewTrackerWithRetention(dataDir string, retention RetentionPolicy) (*Tracker, error) {
	return newTracker(dataDir, true, true, retention, true, true)
}

// NewTrackerWithRetentionLazyAPI creates a status tracker that defers loading
// api_status.json until EnsureAPIStatusLoaded is called. RSS and retry status
// are still loaded during construction.
func NewTrackerWithRetentionLazyAPI(dataDir string, retention RetentionPolicy) (*Tracker, error) {
	return newTracker(dataDir, true, true, retention, false, true)
}

// NewReadOnlyTracker loads status files without removing temporary writer files.
// It follows the same load-failure behavior as NewTracker.
//
// It never creates dataDir and never changes its mode; a missing directory is
// an error.
func NewReadOnlyTracker(dataDir string) (*Tracker, error) {
	return newTracker(dataDir, false, false, DefaultRetentionPolicy(), true, false)
}

// NewReadOnlyTrackerLazyAPI is NewReadOnlyTracker without the eager
// api_status.json load. Callers that only need the retry store must use it so
// counting a handful of rows does not read up to max_api_sent_items entries.
//
// It never creates dataDir and never changes its mode; a missing directory is
// an error.
func NewReadOnlyTrackerLazyAPI(dataDir string) (*Tracker, error) {
	return newTracker(dataDir, false, false, DefaultRetentionPolicy(), false, false)
}

// UpdateRetention applies new local status retention bounds to future cleanup runs.
func (t *Tracker) UpdateRetention(retention RetentionPolicy) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.retention = normalizeRetentionPolicy(retention)
	t.setAuditRotationSnapshot(t.retention)
}

// newTracker builds a tracker over dataDir. createDir separates the writer
// constructors from the read-only ones: only a writer may create data_dir.
func newTracker(dataDir string, createDir, cleanupTemps bool, retention RetentionPolicy, loadAPI bool, memoryFallback bool) (*Tracker, error) {
	if createDir {
		// Create data directory if it doesn't exist
		if err := ensurePrivateDataDir(dataDir); err != nil {
			if memoryFallback {
				return newMemoryTrackerAfterStatusStoreFailure(dataDir, retention, err), nil
			}
			return nil, fmt.Errorf("failed to create data directory: %w", err)
		}
	} else if err := requireExistingDataDir(dataDir); err != nil {
		return nil, err
	}
	if cleanupTemps {
		if err := cleanupStatusTempFiles(dataDir); err != nil {
			if memoryFallback {
				return newMemoryTrackerAfterStatusStoreFailure(dataDir, retention, err), nil
			}
			return nil, fmt.Errorf("failed to cleanup stale status temp files: %w", err)
		}
		if err := probeWritableStatusStoreFunc(dataDir); err != nil {
			if memoryFallback {
				return newMemoryTrackerAfterStatusStoreFailure(dataDir, retention, err), nil
			}
			return nil, fmt.Errorf("status store writeability probe failed: %w", err)
		}
	}

	tracker := &Tracker{
		dataDir:             dataDir,
		apiStatus:           newAPIStatus(),
		rssStatus:           newRSSStatus(),
		retryStore:          newRetryStore(),
		parsedIndex:         make(map[string]int),
		parsedFeedIndex:     make(map[string][]int),
		parsedFeedTypeIndex: make(map[string][]int),
		retention:           normalizeRetentionPolicy(retention),
	}

	// Load existing status files if they exist
	if loadAPI {
		if err := tracker.loadAPIStatus(); err != nil {
			return nil, fmt.Errorf("failed to load existing API status: %w", err)
		}
	}

	if err := tracker.loadRSSStatus(); err != nil {
		return nil, fmt.Errorf("failed to load existing RSS status: %w", err)
	}

	if err := tracker.loadRetryStatus(); err != nil {
		return nil, fmt.Errorf("failed to load existing retry status: %w", err)
	}

	tracker.setAuditRotationSnapshot(tracker.retention)
	return tracker, nil
}

func newMemoryTrackerAfterStatusStoreFailure(dataDir string, retention RetentionPolicy, err error) *Tracker {
	tracker := NewMemoryTrackerWithRetention(retention)
	tracker.dataDir = dataDir
	log.WithError(err).WithField("data_dir", dataDir).Warn("Status store unavailable; continuing with in-memory status only")
	return tracker
}

// requireExistingDataDir is the read-only counterpart of ensurePrivateDataDir.
// It verifies that dataDir is an existing directory and never creates it and
// never changes its mode: a read-only surface that created data_dir would turn
// an unmounted volume into a healthy-looking empty state, and the chmod to 0700
// run by a different user (root in a maintenance shell) would tighten a
// directory the bot's own UID shares with the host.
//
// Its "does not exist" wording deliberately differs from main.go's
// errDataDirMissing; the two never both fire on one surface.
func requireExistingDataDir(dataDir string) error {
	info, err := os.Stat(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("data directory %q does not exist", dataDir)
		}
		return fmt.Errorf("stat %q: %w", dataDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dataDir)
	}
	return nil
}

func ensurePrivateDataDir(dataDir string) error {
	info, err := os.Stat(dataDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat %q: %w", dataDir, err)
		}
		if err := os.MkdirAll(dataDir, privateDataDirMode); err != nil {
			return fmt.Errorf("create %q: %w", dataDir, err)
		}
	} else if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dataDir)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(dataDir, privateDataDirMode); err != nil {
			return fmt.Errorf("chmod %q: %w", dataDir, err)
		}
	}
	return nil
}

var probeWritableStatusStoreFunc = probeWritableStatusStore

func probeWritableStatusStore(dataDir string) error {
	tempFile := filepath.Join(dataDir, ".ransomware-bot-status-write-test.tmp")
	targetFile := filepath.Join(dataDir, ".ransomware-bot-status-write-test")
	payload := []byte("status-write-test\n")

	_ = os.Remove(tempFile)
	_ = os.Remove(targetFile)
	if err := os.WriteFile(tempFile, payload, statusFileMode); err != nil {
		return fmt.Errorf("write temp file %q: %w", tempFile, err)
	}
	if err := os.Rename(tempFile, targetFile); err != nil {
		_ = os.Remove(tempFile)
		return fmt.Errorf("rename temp file %q to %q: %w", tempFile, targetFile, err)
	}
	if err := os.Remove(targetFile); err != nil {
		return fmt.Errorf("remove probe file %q: %w", targetFile, err)
	}
	return nil
}

// NewMemoryTracker creates a status tracker that never reads or writes files.
func NewMemoryTracker() *Tracker {
	return NewMemoryTrackerWithRetention(DefaultRetentionPolicy())
}

// NewMemoryTrackerWithRetention creates an in-memory status tracker with
// explicit retention bounds.
func NewMemoryTrackerWithRetention(retention RetentionPolicy) *Tracker {
	return &Tracker{
		apiStatus:           newAPIStatus(),
		rssStatus:           newRSSStatus(),
		retryStore:          newRetryStore(),
		inMemory:            true,
		parsedIndex:         make(map[string]int),
		parsedFeedIndex:     make(map[string][]int),
		parsedFeedTypeIndex: make(map[string][]int),
		retention:           normalizeRetentionPolicy(retention),
		apiLoaded:           true,
	}
}

// newAPIStatus creates a new empty API status structure
func newAPIStatus() *apiStatus {
	return &apiStatus{
		LastUpdated: statusNow(),
		SentItems:   make(map[string]webhookSentInfo),
	}
}

// newRSSStatus creates a new empty RSS status structure
func newRSSStatus() *rssStatus {
	return &rssStatus{
		LastUpdated: statusNow(),
		Feeds:       make(map[string]FeedInfo),
		ParsedItems: make([]StoredRSSEntry, 0),
		SentItems:   make(map[string]rssWebhookSentInfo),
	}
}

// StatusSummary returns a read-only snapshot for startup and operator logs.
func (t *Tracker) StatusSummary() StatusSummary {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	summary := StatusSummary{
		APISentItems:    len(t.apiStatus.SentItems),
		RSSFeeds:        len(t.rssStatus.Feeds),
		RSSParsedItems:  len(t.rssStatus.ParsedItems),
		RSSSentItems:    len(t.rssStatus.SentItems),
		RetryQueueItems: len(t.retryStore.status.RetryQueue),
		DeadLetterItems: len(t.retryStore.status.DeadLetterItems),
	}
	if t.apiStatus.LastError != nil {
		summary.LastAPIError = *t.apiStatus.LastError
	}
	for _, feedInfo := range t.rssStatus.Feeds {
		if feedInfo.LastError == nil {
			continue
		}
		summary.RSSFeedErrors++
		if summary.LastRSSError == "" {
			summary.LastRSSError = *feedInfo.LastError
		}
	}
	return summary
}

// DirtyState returns which status groups currently have unsaved changes.
func (t *Tracker) DirtyState() TrackerDirtyState {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	return TrackerDirtyState{
		API:   t.apiDirty,
		RSS:   t.rssDirty,
		Retry: t.retryStore.dirty,
	}
}

func (t *Tracker) clearDirtyState() {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.apiDirty = false
	t.rssDirty = false
	t.retryStore.dirty = false
}

// CanAcquireStateLockForStatusWrite reports whether the tracker state lock is
// available. It is intentionally narrow for lock-release invariant tests.
func (t *Tracker) CanAcquireStateLockForStatusWrite() bool {
	if !t.mutex.TryLock() {
		return false
	}
	t.mutex.Unlock()
	return true
}

// APIStatusSnapshot returns a read-only API source health snapshot.
func (t *Tracker) APIStatusSnapshot() APIStatusSnapshot {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	return APIStatusSnapshot{
		LastUpdated:       t.apiStatus.LastUpdated,
		LastCheck:         t.apiStatus.LastCheck,
		LastSuccess:       cloneTimePtr(t.apiStatus.LastSuccess),
		LastError:         cloneStringPtr(t.apiStatus.LastError),
		LastErrorCategory: t.apiStatus.LastErrorCategory,
		LastStatusCode:    t.apiStatus.LastStatusCode,
		LastRetryable:     cloneBoolPtr(t.apiStatus.LastRetryable),
		LastTimeout:       cloneBoolPtr(t.apiStatus.LastTimeout),
		EntriesFound:      t.apiStatus.EntriesFound,
		SentItems:         len(t.apiStatus.SentItems),
	}
}

// APISentItemSnapshot returns one API sent marker without exposing the backing map.
func (t *Tracker) APISentItemSnapshot(itemKey, destinationID string, legacyDestinations ...string) (APISentItemSnapshot, bool) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	destinations := append([]string{destinationID}, legacyDestinations...)
	for _, destination := range destinations {
		if destination == "" {
			continue
		}
		info, ok := t.apiStatus.SentItems[makeCompositeKey(itemKey, destination)]
		if !ok {
			continue
		}
		return APISentItemSnapshot(info), true
	}
	return APISentItemSnapshot{}, false
}

// RSSParsedItemsSnapshot returns cloned parsed RSS entries in readable order.
func (t *Tracker) RSSParsedItemsSnapshot() []StoredRSSEntry {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.ensureParsedReadableLocked()
	items := make([]StoredRSSEntry, 0, len(t.rssStatus.ParsedItems))
	for _, item := range t.rssStatus.ParsedItems {
		items = append(items, cloneStoredRSSEntry(item))
	}
	return items
}

// RSSSentItemSnapshot returns one RSS sent marker without exposing the backing map.
func (t *Tracker) RSSSentItemSnapshot(itemKey, destinationID string, legacyDestinations ...string) (RSSSentItemSnapshot, bool) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	destinations := append([]string{destinationID}, legacyDestinations...)
	for _, destination := range destinations {
		if destination == "" {
			continue
		}
		info, ok := t.rssStatus.SentItems[makeCompositeKey(itemKey, destination)]
		if !ok {
			continue
		}
		return RSSSentItemSnapshot(info), true
	}
	return RSSSentItemSnapshot{}, false
}

// RSSParsedSortState returns parsed RSS sort bookkeeping for focused invariant tests.
func (t *Tracker) RSSParsedSortState() RSSParsedSortState {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	return RSSParsedSortState{
		SortDirty:     t.parsedSortDirty,
		FullSortDirty: t.parsedFullSortDirty,
		DirtyStart:    t.parsedDirtyStart,
	}
}

// RecordDeliveryAuditEvent appends one durable delivery decision record.
func (t *Tracker) RecordDeliveryAuditEvent(event DeliveryAuditEvent) {
	if t == nil {
		return
	}
	if err := t.appendDeliveryAuditEvent(event); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"event_type": event.EventType,
			"item_type":  event.ItemType,
			"item_key":   event.ItemKey,
			"outcome":    event.Outcome,
			"reason":     event.Reason,
		}).Warn("Failed to append delivery audit event")
	}
}

// auditFileOpenFunc is a probe/test seam (mirrors the existing
// probeWritableStatusStoreFunc pattern, tracker.go:677) letting a test stall
// the audit file open to simulate a slow disk/mount, without changing
// production behaviour.
var auditFileOpenFunc = os.OpenFile

func (t *Tracker) appendDeliveryAuditEvent(event DeliveryAuditEvent) error {
	event = normalizeDeliveryAuditEvent(event)
	if event.EventType == "" {
		return nil
	}
	if t.inMemory {
		return nil
	}
	return t.appendNormalizedDeliveryAuditEvent(event)
}

// appendNormalizedDeliveryAuditEvent performs the open/write/close. event must
// already be normalizeDeliveryAuditEvent'd. t.auditMu serializes concurrent
// writers so lines are never interleaved; this never touches t.mutex, so it
// is safe to call after a mutator has released its lock.
func (t *Tracker) appendNormalizedDeliveryAuditEvent(event DeliveryAuditEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal delivery audit event: %w", err)
	}
	payload = append(payload, '\n')

	t.auditMu.Lock()
	defer t.auditMu.Unlock()

	filePath := filepath.Join(t.dataDir, statusAuditFileName)
	if err := t.rotateAuditFileIfNeeded(filePath, len(payload)); err != nil {
		log.WithError(err).WithField("file", filePath).
			Warn("Failed to rotate delivery audit file; continuing to append to the current file")
	}
	file, err := auditFileOpenFunc(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, statusFileMode)
	if err != nil {
		return fmt.Errorf("open delivery audit file %s: %w", filePath, err)
	}
	_, writeErr := file.Write(payload)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write delivery audit file %s: %w", filePath, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close delivery audit file %s: %w", filePath, closeErr)
	}
	return nil
}

// flushDeliveryAuditEvents writes zero or more already-normalized audit
// events WITHOUT t.mutex held. Callers must call
// t.auditFlight.reserve(len(events)) while t.mutex was still locked, release
// it, then call this before returning -- the write still happens
// synchronously, in the same goroutine, before the caller's own function
// returns, so "the function returned" still means "the audit line is
// durable," exactly as before this change. The reservation lets
// SavePendingChanges (the only other reader of this state that makes it
// durable) wait for outstanding writes so it can never persist a state file
// whose accompanying audit line is not yet on disk.
func (t *Tracker) flushDeliveryAuditEvents(events []DeliveryAuditEvent) {
	if len(events) == 0 {
		return
	}
	defer t.auditFlight.release(len(events))
	if t.inMemory {
		return
	}
	for _, event := range events {
		if event.EventType == "" {
			continue
		}
		if err := t.appendNormalizedDeliveryAuditEvent(event); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"event_type": event.EventType,
				"item_type":  event.ItemType,
				"item_key":   event.ItemKey,
				"outcome":    event.Outcome,
				"reason":     event.Reason,
			}).Warn("Failed to append delivery audit event")
		}
	}
}

// auditFlight tracks audit writes reserved (under t.mutex, via reserve) but
// not yet flushed (after release, from flushDeliveryAuditEvents outside
// t.mutex). SavePendingChanges waits for it to reach zero before snapshotting
// so it can never persist a state file reflecting a mutation whose audit line
// is not yet durable (2026-09-03 audit-write-off-mutex fix).
//
// Deliberately not a sync.WaitGroup: WaitGroup requires all Add calls for one
// "generation" to happen-before the matching Wait returns; here reserve() can
// be called by an arbitrary goroutine at an arbitrary time relative to
// SavePendingChanges' wait, which is exactly the reuse the stdlib documents
// as unsafe ("WaitGroup misuse: Add called concurrently with Wait") and can
// panic on.
//
// Liveness note (review 2026-09-03, LOW): waitForZero has no epoch cutover,
// so a continuous stream of new reserve() calls could in principle starve
// SavePendingChanges forever. This is safe today because every source of
// concurrent Tracker mutation in internal/scheduler is a bounded, per-cycle
// goroutine pool (one goroutine per retry group or per webhook target,
// joined by a sync.WaitGroup before the next cycle's mutations begin) rather
// than an unbounded stream; re-examine this assumption if that scheduling
// model ever changes (e.g. an unbounded RSS backlog worker pool).
//
// The zero value is ready to use.
type auditFlight struct {
	mu    sync.Mutex
	count int
	zero  chan struct{} // non-nil, open while count > 0; closed exactly once when count reaches 0
}

func (a *auditFlight) reserve(n int) {
	if n <= 0 {
		return
	}
	a.mu.Lock()
	if a.count == 0 {
		a.zero = make(chan struct{})
	}
	a.count += n
	a.mu.Unlock()
}

// release must be paired with a matching reserve. A call site that releases
// more than it reserved (review 2026-09-03, LOW guard) cannot leave count
// negative forever wedging SavePendingChanges: it is logged and clamped back
// to zero instead. The clamp runs before the close-on-zero check so a count
// that jumps straight past zero (e.g. reserve(1); release(2) in one call)
// still closes the generation channel -- checking count == 0 before clamping
// would miss that case and leave waitForZero() blocked forever even though
// isZero() reports true (F3, review 2026-09-03). a.zero is set to nil right
// after it is closed so a second over-release in the same generation (e.g.
// two release(1) calls after one reserve(1)) sees a.zero == nil and does not
// attempt to close an already-closed channel.
func (a *auditFlight) release(n int) {
	if n <= 0 {
		return
	}
	a.mu.Lock()
	a.count -= n
	if a.count < 0 {
		log.WithFields(log.Fields{
			"count": a.count,
			"n":     n,
		}).Error("auditFlight: release count went negative; a call site released more audit events than it reserved")
		a.count = 0
	}
	if a.count == 0 && a.zero != nil {
		close(a.zero)
		a.zero = nil
	}
	a.mu.Unlock()
}

// waitForZero blocks until no audit writes are in flight. Must be called
// WITHOUT t.mutex held.
func (a *auditFlight) waitForZero() {
	a.mu.Lock()
	ch := a.zero
	a.mu.Unlock()
	if ch == nil {
		return
	}
	<-ch
}

// isZero reports whether any audit writes are currently in flight. Called
// while the caller holds t.mutex (SavePendingChanges); a.mu is a distinct,
// always briefly-held lock never held across t.mutex or t.auditMu.
func (a *auditFlight) isZero() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.count == 0
}

// testAuditEntryPointPanicHook is a nil-by-default test seam that lets a test
// inject a panic immediately after t.mutex.Lock() and the deferred
// reserve/unlock/flush closure are set up in one of the six audit-write
// mutator entry points (tracker.go cleanupOldEntries/cleanupOldRSSEntries,
// retry_store.go EnqueueRetryForDestination/RecordRetryQueueFailureWithErrorInfo/
// DeadLetterRetryQueueEntryWithErrorInfo/MarkRetryDeadLetterForDestinationWithErrorInfo),
// to prove the deferred closure still runs -- releasing t.mutex -- even when
// the entry point's body panics (review Finding A, HIGH, 2026-09-03: manual
// per-branch unlocks are not panic-safe). Must never be set outside a test.
// This is an unsynchronized package-level global, read from all six
// concurrent entry points without a lock: safe as used today because every
// test that sets it does so before any goroutine calls into an entry point
// and restores it to nil before the next one runs, but a future test that
// sets/clears this hook concurrently with another goroutine inside one of
// the six entry points would trip -race (F5, review 2026-09-03).
var testAuditEntryPointPanicHook func()

func triggerTestAuditEntryPointPanicHook() {
	if testAuditEntryPointPanicHook != nil {
		testAuditEntryPointPanicHook()
	}
}

func normalizeDeliveryAuditEvent(event DeliveryAuditEvent) DeliveryAuditEvent {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = statusNow()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}
	event.EventType = auditString(event.EventType)
	event.Source = auditString(event.Source)
	event.ItemType = auditString(event.ItemType)
	event.ItemKey = auditString(event.ItemKey)
	event.Title = auditString(event.Title)
	event.Messenger = auditString(event.Messenger)
	event.DestinationID = auditString(deliveryDestinationIDForPersistence(event.DestinationID))
	event.FeedType = auditString(event.FeedType)
	event.FeedURL = auditString(textutil.RedactURLCredentials(event.FeedURL))
	event.Outcome = auditString(event.Outcome)
	event.Reason = auditString(event.Reason)
	event.Details = auditDetails(event.Details)
	if event.Count < 0 {
		event.Count = 0
	}
	return event
}

func auditDetails(details map[string]string) map[string]string {
	if len(details) == 0 {
		return nil
	}
	normalized := make(map[string]string, min(len(details), maxAuditDetails))
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(normalized) >= maxAuditDetails {
			break
		}
		normalized[auditString(key)] = auditString(textutil.RedactWebhookSecrets(textutil.RedactURLCredentials(details[key])))
	}
	return normalized
}

func auditString(value string) string {
	return textutil.TruncateText(strings.TrimSpace(value), maxAuditStringRunes)
}

// EnsureAPIStatusLoaded loads api_status.json for trackers created with lazy
// API status loading. It is safe to call multiple times.
func (t *Tracker) EnsureAPIStatusLoaded() error {
	t.mutex.RLock()
	loaded := t.apiLoaded || t.inMemory
	t.mutex.RUnlock()
	if loaded {
		return nil
	}

	t.saveMu.Lock()
	defer t.saveMu.Unlock()

	t.mutex.RLock()
	loaded = t.apiLoaded || t.inMemory
	t.mutex.RUnlock()
	if loaded {
		return nil
	}

	return t.loadAPIStatus()
}

// ensureAPIStatusLoadedForMutation loads api_status.json before an API state
// mutation so a lazily constructed tracker cannot persist a snapshot that is
// missing the on-disk sent-item set. Callers must not hold t.mutex or t.saveMu.
func (t *Tracker) ensureAPIStatusLoadedForMutation(operation string) {
	if err := t.EnsureAPIStatusLoaded(); err != nil {
		log.WithError(err).WithField("operation", operation).
			Error("Failed to load API status before API state mutation")
	}
}

// LegacyAPIFetchedItemPayloads returns opaque entries loaded from the retired
// api_status.json fetched_items field so callers can migrate them with current
// delivery configuration.
func (t *Tracker) LegacyAPIFetchedItemPayloads() []json.RawMessage {
	t.ensureAPIStatusLoadedForMutation("legacy_api_fetched_item_payloads")

	t.mutex.RLock()
	defer t.mutex.RUnlock()

	if len(t.apiStatus.FetchedItems) == 0 {
		return nil
	}
	payloads := make([]json.RawMessage, 0, len(t.apiStatus.FetchedItems))
	for _, payload := range t.apiStatus.FetchedItems {
		payloads = append(payloads, cloneRawMessage(payload))
	}
	return payloads
}

// ClearLegacyAPIFetchedItems removes migrated legacy fetched_items from the API
// status snapshot so the next save cannot keep or reprocess retired state.
func (t *Tracker) ClearLegacyAPIFetchedItems() {
	t.ensureAPIStatusLoadedForMutation("clear_legacy_api_fetched_items")

	t.mutex.Lock()
	defer t.mutex.Unlock()

	if len(t.apiStatus.FetchedItems) == 0 {
		return
	}
	t.apiStatus.FetchedItems = nil
	t.apiStatus.LastUpdated = statusNow()
	t.apiDirty = true
}

// UpdateFeedStatus updates the status for a specific RSS feed
func (t *Tracker) UpdateFeedStatus(feedURL string, success bool, entriesFound int, errorMsg string) {
	t.UpdateFeedStatusWithErrorInfo(feedURL, success, entriesFound, errorMsg, SourceErrorInfo{})
}

// UpdateFeedStatusWithErrorInfo updates the status for a specific RSS feed with machine-readable error metadata.
func (t *Tracker) UpdateFeedStatusWithErrorInfo(
	feedURL string,
	success bool,
	entriesFound int,
	errorMsg string,
	errorInfo SourceErrorInfo,
) {
	t.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, success, entriesFound, errorMsg, errorInfo, rss.FeedHTTPValidators{})
}

// UpdateFeedStatusWithErrorInfoAndValidators updates RSS feed status and, on a
// successful fetch, replaces cached HTTP validators for conditional polling.
func (t *Tracker) UpdateFeedStatusWithErrorInfoAndValidators(
	feedURL string,
	success bool,
	entriesFound int,
	errorMsg string,
	errorInfo SourceErrorInfo,
	validators rss.FeedHTTPValidators,
) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	now := statusNow()

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

		// Read before the validator update below mutates ETag/LastModified, so
		// both reflect what this poll was actually conditioned on and against.
		hadStoredValidator := feedInfo.ETag != "" || feedInfo.LastModified != ""
		priorETag := feedInfo.ETag
		priorLastModified := feedInfo.LastModified

		// A 304 need not repeat the validators (RFC 7232 only recommends it): an
		// empty value means "unchanged", never "dropped". Wiping one stops
		// conditional polling for good, because GetRSSFeedHTTPValidators drops a
		// feed once both are empty and every later poll re-downloads the body.
		if validators.ETag != "" {
			feedInfo.ETag = validators.ETag
		}
		if validators.LastModified != "" {
			feedInfo.LastModified = validators.LastModified
		}

		// The guard above means a bogus validator is otherwise never repaired:
		// an origin that unconditionally matches whatever it is sent produces
		// this same shape (success, zero entries, no validator fresher than
		// what is stored) forever, and looks identical to a genuine, healthy
		// 304. RFC 7232 recommends a 304 repeat the ETag, and real origins
		// (nginx, Apache, Cloudflare) do, so "no fresh validator" must be
		// judged by comparing against the stored value, not by testing for an
		// empty one: an echoed validator, byte-identical to what is already on
		// file, is not new evidence the conditional exchange is progressing.
		// Any poll that returns entries, or a validator that genuinely differs
		// from what was stored, proves the exchange is still working and
		// resets the streak.
		freshETag := validators.ETag != "" && validators.ETag != priorETag
		freshLastModified := validators.LastModified != "" && validators.LastModified != priorLastModified
		if hadStoredValidator && entriesFound == 0 && !freshETag && !freshLastModified {
			feedInfo.ConsecutiveNotModified++
			if feedInfo.ConsecutiveNotModifiedSince == nil {
				streakStart := now
				feedInfo.ConsecutiveNotModifiedSince = &streakStart
			}
			if now.Sub(*feedInfo.ConsecutiveNotModifiedSince) >= maxConsecutiveNotModifiedDuration {
				feedInfo.ETag = ""
				feedInfo.LastModified = ""
				feedInfo.ConsecutiveNotModified = 0
				feedInfo.ConsecutiveNotModifiedSince = nil
				log.WithFields(log.Fields{
					"feed_url":           textutil.RedactURLCredentials(feedURL),
					"threshold_duration": maxConsecutiveNotModifiedDuration.String(),
				}).Warn("RSS feed reported unchanged for too long; forcing an unconditional refetch to recover from a possibly stuck validator")
			}
		} else {
			feedInfo.ConsecutiveNotModified = 0
			feedInfo.ConsecutiveNotModifiedSince = nil
		}

		feedInfo.ConsecutiveFailures = 0
		feedInfo.NextAttemptAfter = nil
		clearFeedSourceErrorInfo(&feedInfo)

		// Calculate new success rate (simple moving average approach)
		if exists {
			feedInfo.SuccessRate = (feedInfo.SuccessRate * 0.9) + (1.0 * 0.1)
		} else {
			feedInfo.SuccessRate = 1.0
		}
	} else {
		// Update error information
		errorMsg = textutil.RedactWebhookSecrets(errorMsg)
		feedInfo.LastError = &errorMsg
		feedInfo.ConsecutiveFailures++
		feedInfo.NextAttemptAfter = nextRSSFeedAttemptAfter(now, feedInfo.ConsecutiveFailures)
		applySourceErrorInfoToFeed(&feedInfo, errorInfo)

		// Calculate new success rate with failure
		if exists {
			feedInfo.SuccessRate *= 0.9 // Reduce success rate
		} else {
			feedInfo.SuccessRate = 0.0
		}
	}

	// Store updated feed info
	t.rssStatus.Feeds[feedURL] = feedInfo
	t.rssStatus.LastUpdated = now
	t.rssDirty = true
}

// GetRSSFeedHTTPValidators returns cached HTTP validators for the requested feeds.
func (t *Tracker) GetRSSFeedHTTPValidators(feedURLs []string) map[string]rss.FeedHTTPValidators {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	validators := make(map[string]rss.FeedHTTPValidators, len(feedURLs))
	for _, feedURL := range feedURLs {
		feedInfo, exists := t.rssStatus.Feeds[feedURL]
		if !exists {
			continue
		}
		if feedInfo.ETag == "" && feedInfo.LastModified == "" {
			continue
		}
		validators[feedURL] = rss.FeedHTTPValidators{
			ETag:         feedInfo.ETag,
			LastModified: feedInfo.LastModified,
		}
	}
	return validators
}

// GetRSSFeedInfo returns a cloned snapshot of one feed status record.
func (t *Tracker) GetRSSFeedInfo(feedURL string) (FeedInfo, bool) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	feedInfo, exists := t.rssStatus.Feeds[feedURL]
	if !exists {
		return FeedInfo{}, false
	}
	return cloneFeedInfo(feedInfo), true
}

// ShouldPollRSSFeed reports whether a feed is outside its failure cooldown.
func (t *Tracker) ShouldPollRSSFeed(feedURL string, now time.Time) bool {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	feedInfo, exists := t.rssStatus.Feeds[feedURL]
	if !exists || feedInfo.NextAttemptAfter == nil {
		return true
	}
	if now.IsZero() {
		now = statusNow()
	}
	return !now.Before(*feedInfo.NextAttemptAfter)
}

func nextRSSFeedAttemptAfter(now time.Time, consecutiveFailures int) *time.Time {
	if consecutiveFailures < rssFailureCooldownThreshold {
		return nil
	}

	delay := rssFailureCooldownBase
	for i := rssFailureCooldownThreshold; i < consecutiveFailures; i++ {
		delay *= 2
		if delay >= rssFailureCooldownMax {
			delay = rssFailureCooldownMax
			break
		}
	}
	nextAttempt := now.Add(delay)
	return &nextAttempt
}

// PruneRSSFeedStatus removes feed health records for RSS feed URLs that are no
// longer present in the active configuration. Parsed RSS entries and sent
// markers are intentionally retained by their separate recovery/retention rules.
func (t *Tracker) PruneRSSFeedStatus(activeFeedURLs []string) int {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if len(t.rssStatus.Feeds) == 0 {
		return 0
	}

	active := make(map[string]struct{}, len(activeFeedURLs))
	for _, feedURL := range activeFeedURLs {
		if feedURL == "" {
			continue
		}
		active[feedURL] = struct{}{}
	}

	removed := 0
	for feedURL := range t.rssStatus.Feeds {
		if _, keep := active[feedURL]; keep {
			continue
		}
		delete(t.rssStatus.Feeds, feedURL)
		removed++
	}

	if removed > 0 {
		t.rssStatus.LastUpdated = statusNow()
		t.rssDirty = true
		log.WithField("removed_feed_statuses", removed).Info("Pruned RSS feed status for removed feeds")
	}

	return removed
}

// UpdateAPIStatus updates the status for the ransomware API
func (t *Tracker) UpdateAPIStatus(success bool, entriesFound int, errorMsg string) {
	t.UpdateAPIStatusWithErrorInfo(success, entriesFound, errorMsg, SourceErrorInfo{})
}

// UpdateAPIStatusWithErrorInfo updates the ransomware API status with machine-readable error metadata.
func (t *Tracker) UpdateAPIStatusWithErrorInfo(success bool, entriesFound int, errorMsg string, errorInfo SourceErrorInfo) {
	t.ensureAPIStatusLoadedForMutation("update_api_status")

	t.mutex.Lock()
	defer t.mutex.Unlock()

	now := statusNow()

	// Update API check time
	t.apiStatus.LastCheck = now
	t.apiStatus.EntriesFound = entriesFound

	if success {
		// Update success information
		t.apiStatus.LastSuccess = &now
		t.apiStatus.LastError = nil
		clearAPISourceErrorInfo(t.apiStatus)
	} else {
		// Update error information
		errorMsg = textutil.RedactWebhookSecrets(errorMsg)
		t.apiStatus.LastError = &errorMsg
		applySourceErrorInfoToAPI(t.apiStatus, errorInfo)
	}

	t.apiStatus.LastUpdated = now
	t.apiDirty = true
}

func applySourceErrorInfoToAPI(apiStatus *apiStatus, info SourceErrorInfo) {
	apiStatus.LastErrorCategory = info.ErrorCategory
	apiStatus.LastStatusCode = info.StatusCode
	apiStatus.LastRetryable = cloneBoolPtr(info.Retryable)
	apiStatus.LastTimeout = cloneBoolPtr(info.Timeout)
}

func clearAPISourceErrorInfo(apiStatus *apiStatus) {
	apiStatus.LastErrorCategory = ""
	apiStatus.LastStatusCode = 0
	apiStatus.LastRetryable = nil
	apiStatus.LastTimeout = nil
}

func applySourceErrorInfoToFeed(feedInfo *FeedInfo, info SourceErrorInfo) {
	feedInfo.LastErrorCategory = info.ErrorCategory
	feedInfo.LastStatusCode = info.StatusCode
	feedInfo.LastRetryable = cloneBoolPtr(info.Retryable)
	feedInfo.LastTimeout = cloneBoolPtr(info.Timeout)
}

func clearFeedSourceErrorInfo(feedInfo *FeedInfo) {
	feedInfo.LastErrorCategory = ""
	feedInfo.LastStatusCode = 0
	feedInfo.LastRetryable = nil
	feedInfo.LastTimeout = nil
}

// IsAPIItemSentToWebhook checks if an API item was already sent to a specific webhook
func (t *Tracker) IsAPIItemSentToWebhook(itemKey, webhookURL string) bool {
	return t.IsAPIItemSentToDestination(itemKey, webhookURL)
}

// IsAPIItemSentToDestination checks if an API item was already sent to a stable destination.
func (t *Tracker) IsAPIItemSentToDestination(itemKey, destinationID string, legacyDestinations ...string) bool {
	t.ensureAPIStatusLoadedForMutation("is_api_item_sent_to_destination")

	t.mutex.Lock()
	defer t.mutex.Unlock()

	return t.apiItemSentToDestinationLocked(itemKey, destinationID, legacyDestinations...)
}

// legacySentMarkerAppliesTo reports whether a sent marker stored under a legacy
// webhook-URL key may be adopted by destinationID. Markers written before
// destination IDs existed carry no recorded destination and migrate once; a
// legacy alias written beside a destination-ID marker records its owning
// destination, so a sibling destination sharing the same webhook URL must not
// adopt it.
func legacySentMarkerAppliesTo(recordedDestinationID, destinationID string) bool {
	recordedDestinationID = strings.TrimSpace(recordedDestinationID)
	return recordedDestinationID == "" || recordedDestinationID == destinationID
}

func (t *Tracker) apiItemSentToDestinationLocked(itemKey, destinationID string, legacyDestinations ...string) bool {
	compositeKey := makeCompositeKey(itemKey, destinationID)
	if _, exists := t.apiStatus.SentItems[compositeKey]; exists {
		return true
	}

	for _, legacyDestination := range legacyDestinations {
		if legacyDestination == "" || legacyDestination == destinationID {
			continue
		}
		legacyKey := makeCompositeKey(itemKey, legacyDestination)
		info, exists := t.apiStatus.SentItems[legacyKey]
		if !exists {
			continue
		}
		if !legacySentMarkerAppliesTo(info.DestinationID, destinationID) {
			continue
		}
		info.DestinationID = deliveryDestinationIDForPersistence(destinationID)
		t.apiStatus.SentItems[compositeKey] = info
		t.indexAPISentItemLocked(compositeKey, info)
		t.apiStatus.LastUpdated = statusNow()
		t.apiDirty = true
		return true
	}

	return false
}

// MarkAPIItemSentToWebhook marks an API item as sent to a specific webhook URL.
func (t *Tracker) MarkAPIItemSentToWebhook(itemKey, itemTitle, webhookURL string) {
	t.MarkAPIItemSentToDestination(itemKey, itemTitle, webhookURL)
}

// MarkAPIItemSentToDestination marks an API item as sent to a stable destination.
// itemTitle is accepted for call-site symmetry with the RSS path; API sent
// markers do not persist a title.
func (t *Tracker) MarkAPIItemSentToDestination(itemKey, itemTitle, destinationID string) {
	t.markAPIItemSentUnderKey(itemKey, destinationID, destinationID)
}

// MarkAPIItemSentToLegacyDestination writes the legacy webhook-URL alias beside
// an existing destination-ID marker and records the destination that owns the
// delivery, so a sibling destination sharing the same webhook URL cannot adopt
// the alias as its own delivery.
func (t *Tracker) MarkAPIItemSentToLegacyDestination(itemKey, itemTitle, legacyDestination, ownerDestinationID string) {
	t.markAPIItemSentUnderKey(itemKey, legacyDestination, ownerDestinationID)
}

// markAPIItemSentUnderKey stores one API sent marker under markerDestination and
// records ownerDestinationID as the destination the delivery belongs to.
//
// MarkAPIItemSentToWebhook passes the webhook URL as both marker key and owner
// on purpose: deliveryDestinationIDForPersistence blanks any http(s) owner, so
// such a marker stays owner-less and legacySentMarkerAppliesTo keeps migrating
// it for any asking destination. A change that stopped blanking http(s)
// destinations would silently break the legacy webhook-URL migration.
func (t *Tracker) markAPIItemSentUnderKey(itemKey, markerDestination, ownerDestinationID string) {
	t.ensureAPIStatusLoadedForMutation("mark_api_item_sent")

	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Ensure SentItems map exists
	if t.apiStatus.SentItems == nil {
		t.apiStatus.SentItems = make(map[string]webhookSentInfo)
	}

	// Create composite key using secure hash
	compositeKey := makeCompositeKey(itemKey, markerDestination)
	newOwner := deliveryDestinationIDForPersistence(ownerDestinationID)

	// A shared legacy alias records a single owner so a sibling destination
	// sharing this webhook URL cannot adopt someone else's delivery
	// (legacySentMarkerAppliesTo). When this write changes that owner, a
	// second, distinct destination has genuinely delivered/decided this same
	// item under the same shared URL -- a documented, supported config
	// (readme.md "Two endpoints of the same webhook block may point at the
	// same URL"). Refresh the superseded owner's OWN primary marker's SentAt
	// so retention (cleanupOldAPISentItems, count-bounded, oldest-first) does
	// not prune it merely because the alias kept getting rewritten by its
	// sibling while the primary sat still. A no-op if that primary was
	// already pruned; never touches the alias's own owner field.
	supersededOwner := ""
	if existing, exists := t.apiStatus.SentItems[compositeKey]; exists {
		if oldOwner := strings.TrimSpace(existing.DestinationID); oldOwner != "" && oldOwner != newOwner {
			supersededOwner = oldOwner
		}
	}

	// Mark item as sent to this specific webhook
	t.apiStatus.SentItems[compositeKey] = webhookSentInfo{
		SentAt:        statusNow(),
		DestinationID: newOwner,
	}
	t.indexAPISentItemLocked(compositeKey, t.apiStatus.SentItems[compositeKey])
	t.apiStatus.LastUpdated = statusNow()
	t.apiDirty = true

	// Refresh the superseded owner's own primary marker's SentAt AFTER the
	// write above (not before): on a platform whose clock never ties
	// (Linux), refreshing first left the refreshed entry one clock tick
	// OLDER than this write, and the next read-time legacy-adoption mint
	// (apiItemSentToDestinationLocked) inherits this write's SentAt
	// unchanged, making the once-refreshed primary the unique oldest of the
	// three surviving keys and evicting it -- reopening the exact defect
	// this refresh exists to close. Deferring the refresh to here
	// guarantees its SentAt is >= this write's, closing that gap.
	if supersededOwner != "" {
		t.refreshAPISentItemSentAtLocked(itemKey, supersededOwner)
	}
}

// refreshAPISentItemSentAtLocked bumps an existing API sent marker's SentAt to
// now, without changing its DestinationID or any other field. Used only to
// keep a destination's own primary marker from aging out of retention ahead
// of a sibling alias sharing its webhook URL; a no-op if the marker does not
// exist (already pruned, or never delivered under its own primary).
// NOTE: Must be called with t.mutex locked.
func (t *Tracker) refreshAPISentItemSentAtLocked(itemKey, destinationID string) {
	key := makeCompositeKey(itemKey, destinationID)
	info, exists := t.apiStatus.SentItems[key]
	if !exists {
		return
	}
	info.SentAt = statusNow()
	t.apiStatus.SentItems[key] = info
	t.indexAPISentItemLocked(key, info)
}

func (t *Tracker) seedAPISentItemForRetention(itemKey, destinationID string, sentAt time.Time) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.apiStatus.SentItems == nil {
		t.apiStatus.SentItems = make(map[string]webhookSentInfo)
	}
	compositeKey := makeCompositeKey(itemKey, destinationID)
	info := webhookSentInfo{
		SentAt:        sentAt.UTC(),
		DestinationID: deliveryDestinationIDForPersistence(destinationID),
	}
	t.apiStatus.SentItems[compositeKey] = info
	t.indexAPISentItemLocked(compositeKey, info)
	t.apiStatus.LastUpdated = statusNow()
	t.apiDirty = true
}

func (t *Tracker) seedStaleAPISentHeapEntryForInvariant(itemKey, destinationID string, sentAt time.Time) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.indexAPISentItemLocked(makeCompositeKey(itemKey, destinationID), webhookSentInfo{
		SentAt:        sentAt.UTC(),
		DestinationID: deliveryDestinationIDForPersistence(destinationID),
	})
}

func (t *Tracker) setAllAPISentItemTimes(sentAt time.Time) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	sentAt = sentAt.UTC()
	for compositeKey, sentInfo := range t.apiStatus.SentItems {
		sentInfo.SentAt = sentAt
		t.apiStatus.SentItems[compositeKey] = sentInfo
		t.indexAPISentItemLocked(compositeKey, sentInfo)
	}
	t.apiStatus.LastUpdated = statusNow()
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

	t.markRSSItemParsedLocked(feedURL, itemKey, entry)
}

func (t *Tracker) MarkRSSItemsParsed(feedURL string, entries map[string]StoredRSSEntry) {
	if len(entries) == 0 {
		return
	}

	t.mutex.Lock()
	defer t.mutex.Unlock()

	for itemKey, entry := range entries {
		t.markRSSItemParsedLocked(feedURL, itemKey, entry)
	}
}

func (t *Tracker) seedRSSParsedItemsForRetention(entries []StoredRSSEntry) {
	if len(entries) == 0 {
		return
	}

	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.rssStatus.ParsedItems == nil {
		t.rssStatus.ParsedItems = make([]StoredRSSEntry, 0, len(entries))
	}
	for _, entry := range entries {
		t.rssStatus.ParsedItems = append(t.rssStatus.ParsedItems, cloneStoredRSSEntry(entry))
	}
	t.rebuildParsedIndex()
	t.markRSSParsedFullSortDirtyLocked()
	t.rssStatus.LastUpdated = statusNow()
	t.rssDirty = true
}

func (t *Tracker) markRSSItemParsedLocked(feedURL, itemKey string, entry StoredRSSEntry) {
	// Ensure ParsedItems slice exists
	if t.rssStatus.ParsedItems == nil {
		t.rssStatus.ParsedItems = make([]StoredRSSEntry, 0)
	}

	entry.Key = itemKey
	entry.FeedURL = feedURL
	entry.ParsedAt = formatStatusTimestamp(statusNow())
	entry.Categories = cloneStringSlice(entry.Categories)

	idxKey := parsedIndexKey(feedURL, itemKey)
	if idx, exists := t.parsedIndex[idxKey]; exists {
		// Update existing entry at known index (O(1))
		previousSortTime := storedRSSSortTime(t.rssStatus.ParsedItems[idx])
		t.rssStatus.ParsedItems[idx] = entry
		t.parsedIndexDirty = true
		if !previousSortTime.Equal(storedRSSSortTime(entry)) {
			t.markRSSParsedFullSortDirtyLocked()
		}
	} else {
		// Append new entry and record index (O(1))
		appendIndex := len(t.rssStatus.ParsedItems)
		t.parsedIndex[idxKey] = appendIndex
		t.rssStatus.ParsedItems = append(t.rssStatus.ParsedItems, entry)
		t.parsedIndexDirty = true
		t.markRSSParsedAppendDirtyLocked(appendIndex)
	}

	t.rssStatus.LastUpdated = statusNow()
	t.rssDirty = true
}

func (t *Tracker) setRSSSentItemTime(itemKey, destinationID string, sentAt time.Time) bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	compositeKey := makeCompositeKey(itemKey, destinationID)
	info, ok := t.rssStatus.SentItems[compositeKey]
	if !ok {
		return false
	}
	info.SentAt = sentAt.UTC()
	t.rssStatus.SentItems[compositeKey] = info
	t.rssStatus.LastUpdated = statusNow()
	t.rssDirty = true
	return true
}

func (t *Tracker) GetRSSParsedItemKeys(feedURL string) map[string]struct{} {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.ensureParsedReadableLocked()
	indexes := t.parsedFeedIndex[feedURL]
	keys := make(map[string]struct{}, len(indexes))
	for _, idx := range indexes {
		if idx < 0 || idx >= len(t.rssStatus.ParsedItems) {
			continue
		}
		keys[t.rssStatus.ParsedItems[idx].Key] = struct{}{}
	}
	return keys
}

// MinimizeRSSParsedItem replaces a parsed RSS item with a deduplication-only
// record. It is used for entries that do not match any enabled delivery target,
// so full content is not persisted outside the configured processing scope.
func (t *Tracker) MinimizeRSSParsedItem(feedURL, itemKey string) bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	idx, exists := t.parsedIndex[parsedIndexKey(feedURL, itemKey)]
	if !exists || idx < 0 || idx >= len(t.rssStatus.ParsedItems) {
		return false
	}

	current := t.rssStatus.ParsedItems[idx]
	minimized := StoredRSSEntry{
		Key:       current.Key,
		FeedURL:   current.FeedURL,
		FeedType:  current.FeedType,
		Published: current.Published,
		ParsedAt:  current.ParsedAt,
	}
	if minimized.ParsedAt == "" {
		minimized.ParsedAt = formatStatusTimestamp(statusNow())
	}

	t.rssStatus.ParsedItems[idx] = minimized
	t.rssStatus.LastUpdated = statusNow()
	t.rssDirty = true
	return true
}

// SetRSSParsedItemFeedType records the feed category used when an RSS item was
// parsed so recovery does not depend on later feed URL configuration changes.
func (t *Tracker) SetRSSParsedItemFeedType(feedURL, itemKey, feedType string) bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if feedType == "" {
		return false
	}

	idx, exists := t.parsedIndex[parsedIndexKey(feedURL, itemKey)]
	if !exists || idx < 0 || idx >= len(t.rssStatus.ParsedItems) {
		return false
	}
	if t.rssStatus.ParsedItems[idx].FeedType == feedType {
		return false
	}

	t.rssStatus.ParsedItems[idx].FeedType = feedType
	t.parsedIndexDirty = true
	t.rssStatus.LastUpdated = statusNow()
	t.rssDirty = true
	return true
}

// MarkRSSItemSentToWebhook marks an RSS item as sent to a specific webhook
func (t *Tracker) MarkRSSItemSentToWebhook(itemKey, itemTitle, feedTitle, webhookURL string) {
	t.MarkRSSItemSentToDestination(itemKey, itemTitle, feedTitle, webhookURL)
}

// MarkRSSItemSentToDestination marks an RSS item as sent to a stable destination.
func (t *Tracker) MarkRSSItemSentToDestination(itemKey, itemTitle, feedTitle, destinationID string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.markRSSItemSentUnderKeyLocked(itemKey, itemTitle, feedTitle, destinationID, destinationID, false, false)
}

// MarkRSSItemSentToLegacyDestination writes the legacy webhook-URL alias beside
// an existing destination-ID delivery marker and records the destination that
// owns the delivery, so a sibling destination sharing the same webhook URL --
// in the same webhook block or in another block of a different feed type --
// cannot adopt it.
func (t *Tracker) MarkRSSItemSentToLegacyDestination(itemKey, itemTitle, feedTitle, legacyDestination, ownerDestinationID string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.markRSSItemSentUnderKeyLocked(itemKey, itemTitle, feedTitle, legacyDestination, ownerDestinationID, false, false)
}

// MarkRSSItemDedupedForDestination records the per-destination marker a
// content-signature dedup hit produces for the SUPPRESSED entry. The story was
// delivered under a different item key, so the marker suppresses exactly like a
// delivery marker, but it is flagged derived: it never becomes a
// content-signature anchor of its own at load time.
func (t *Tracker) MarkRSSItemDedupedForDestination(itemKey, itemTitle, feedTitle, destinationID string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.markRSSItemSentUnderKeyLocked(itemKey, itemTitle, feedTitle, destinationID, destinationID, false, true)
}

// MarkRSSItemDedupedForLegacyDestination is MarkRSSItemSentToLegacyDestination
// for the content-signature dedup marker; the Derived flag is carried on the
// alias row exactly as MarkRSSItemDedupedForDestination carries it on the
// primary row.
func (t *Tracker) MarkRSSItemDedupedForLegacyDestination(itemKey, itemTitle, feedTitle, legacyDestination, ownerDestinationID string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.markRSSItemSentUnderKeyLocked(itemKey, itemTitle, feedTitle, legacyDestination, ownerDestinationID, false, true)
}

// MarkRSSItemSkippedForWebhook records a per-webhook skip marker without
// persisting RSS title or feed title.
func (t *Tracker) MarkRSSItemSkippedForWebhook(itemKey, webhookURL string) {
	t.MarkRSSItemSkippedForDestination(itemKey, webhookURL)
}

// MarkRSSItemSkippedForDestination records a per-destination skip marker without
// persisting RSS title or feed title.
func (t *Tracker) MarkRSSItemSkippedForDestination(itemKey, destinationID string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.markRSSItemSentUnderKeyLocked(itemKey, "", "", destinationID, destinationID, true, false)
}

// MarkRSSItemSkippedForLegacyDestination is MarkRSSItemSentToLegacyDestination
// for the filter-mismatch skip marker; the Skipped flag is carried on the alias
// row exactly as MarkRSSItemSkippedForDestination carries it on the primary
// row. No title arguments: MarkRSSItemSkippedForDestination does not persist
// RSS title or feed title either, and the alias must match its primary byte
// for byte on every field but DestinationID.
func (t *Tracker) MarkRSSItemSkippedForLegacyDestination(itemKey, legacyDestination, ownerDestinationID string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.markRSSItemSentUnderKeyLocked(itemKey, "", "", legacyDestination, ownerDestinationID, true, false)
}

// markRSSItemSentUnderKeyLocked stores one RSS marker under markerDestination
// and records ownerDestinationID as the destination the marker belongs to.
// MarkRSSItemSentToWebhook / MarkRSSItemSkippedForWebhook pass the webhook URL
// as BOTH the marker key and the owner: deliveryDestinationIDForPersistence
// blanks any http(s) destination (retry_store.go), so such a marker stays
// owner-less and legacySentMarkerAppliesTo keeps migrating it once per asking
// destination. A change that stops blanking http(s) destinations would kill
// that migration silently.
// NOTE: Must be called with t.mutex locked
func (t *Tracker) markRSSItemSentUnderKeyLocked(
	itemKey, itemTitle, feedTitle, markerDestination, ownerDestinationID string,
	skipped, derived bool,
) {
	// Ensure SentItems map exists
	if t.rssStatus.SentItems == nil {
		t.rssStatus.SentItems = make(map[string]rssWebhookSentInfo)
	}

	// Create secure composite key using SHA256 hash
	compositeKey := makeCompositeKey(itemKey, markerDestination)
	newOwner := deliveryDestinationIDForPersistence(ownerDestinationID)

	// See markAPIItemSentUnderKey's matching comment (tracker.go) -- same
	// shared-alias overwrite, same fix, RSS twin of that one.
	supersededOwner := ""
	if existing, exists := t.rssStatus.SentItems[compositeKey]; exists {
		if oldOwner := strings.TrimSpace(existing.DestinationID); oldOwner != "" && oldOwner != newOwner {
			supersededOwner = oldOwner
		}
	}

	// Mark item as sent under this specific marker key
	t.rssStatus.SentItems[compositeKey] = rssWebhookSentInfo{
		Title:         itemTitle,
		FeedTitle:     feedTitle,
		SentAt:        statusNow(),
		DestinationID: newOwner,
		ItemKey:       itemKey, // Store for reverse lookup
		Skipped:       skipped,
		Derived:       derived,
	}
	t.rssStatus.LastUpdated = statusNow()
	t.rssDirty = true

	// Refresh the superseded owner's own primary marker's SentAt AFTER the
	// write above, not before -- see markAPIItemSentUnderKey's matching
	// comment (tracker.go) for why the ordering matters.
	if supersededOwner != "" {
		t.refreshRSSSentItemSentAtLocked(itemKey, supersededOwner)
	}
}

// refreshRSSSentItemSentAtLocked bumps an existing RSS sent marker's SentAt to
// now, without changing any other field. RSS twin of
// refreshAPISentItemSentAtLocked; see its comment.
// NOTE: Must be called with t.mutex locked.
func (t *Tracker) refreshRSSSentItemSentAtLocked(itemKey, destinationID string) {
	key := makeCompositeKey(itemKey, destinationID)
	info, exists := t.rssStatus.SentItems[key]
	if !exists {
		return
	}
	info.SentAt = statusNow()
	t.rssStatus.SentItems[key] = info
}

// IsRSSItemSentToWebhook checks if an RSS item was already sent to a specific webhook
func (t *Tracker) IsRSSItemSentToWebhook(itemKey, webhookURL string) bool {
	return t.IsRSSItemSentToDestination(itemKey, webhookURL)
}

// IsRSSItemSentToDestination checks if an RSS item was already sent to a stable destination.
func (t *Tracker) IsRSSItemSentToDestination(itemKey, destinationID string, legacyDestinations ...string) bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	return t.rssItemSentToDestinationLocked(itemKey, destinationID, legacyDestinations...)
}

func (t *Tracker) rssItemSentToDestinationLocked(itemKey, destinationID string, legacyDestinations ...string) bool {
	compositeKey := makeCompositeKey(itemKey, destinationID)
	if _, exists := t.rssStatus.SentItems[compositeKey]; exists {
		return true
	}

	for _, legacyDestination := range legacyDestinations {
		if legacyDestination == "" || legacyDestination == destinationID {
			continue
		}
		legacyKey := makeCompositeKey(itemKey, legacyDestination)
		info, exists := t.rssStatus.SentItems[legacyKey]
		if !exists {
			continue
		}
		if !legacySentMarkerAppliesTo(info.DestinationID, destinationID) {
			continue
		}
		info.DestinationID = deliveryDestinationIDForPersistence(destinationID)
		t.rssStatus.SentItems[compositeKey] = info
		t.rssStatus.LastUpdated = statusNow()
		t.rssDirty = true
		return true
	}

	return false
}

type rssSentLegacyMigration struct {
	itemKey           string
	destinationID     string
	legacyDestination string
}

func (t *Tracker) rssItemSentToDestinationReadLocked(itemKey, destinationID string, legacyDestinations ...string) (bool, string) {
	compositeKey := makeCompositeKey(itemKey, destinationID)
	if _, exists := t.rssStatus.SentItems[compositeKey]; exists {
		return true, ""
	}

	for _, legacyDestination := range legacyDestinations {
		if legacyDestination == "" || legacyDestination == destinationID {
			continue
		}
		legacyKey := makeCompositeKey(itemKey, legacyDestination)
		info, exists := t.rssStatus.SentItems[legacyKey]
		if !exists {
			continue
		}
		if !legacySentMarkerAppliesTo(info.DestinationID, destinationID) {
			continue
		}
		return true, legacyDestination
	}

	return false, ""
}

func (t *Tracker) migrateRSSSentLegacyDestinations(migrations []rssSentLegacyMigration) {
	if len(migrations) == 0 {
		return
	}

	t.mutex.Lock()
	defer t.mutex.Unlock()

	changed := false
	for _, migration := range migrations {
		if migration.itemKey == "" || migration.destinationID == "" || migration.legacyDestination == "" ||
			migration.legacyDestination == migration.destinationID {
			continue
		}

		compositeKey := makeCompositeKey(migration.itemKey, migration.destinationID)
		if _, exists := t.rssStatus.SentItems[compositeKey]; exists {
			continue
		}

		legacyKey := makeCompositeKey(migration.itemKey, migration.legacyDestination)
		info, exists := t.rssStatus.SentItems[legacyKey]
		if !exists {
			continue
		}
		if !legacySentMarkerAppliesTo(info.DestinationID, migration.destinationID) {
			continue
		}

		info.DestinationID = deliveryDestinationIDForPersistence(migration.destinationID)
		t.rssStatus.SentItems[compositeKey] = info
		changed = true
	}

	if changed {
		t.rssStatus.LastUpdated = statusNow()
		t.rssDirty = true
	}
}

// GetUnsentRSSItemsForWebhook returns RSS entries not yet sent to a specific webhook, sorted chronologically.
func (t *Tracker) GetUnsentRSSItemsForWebhook(webhookURL string) []StoredRSSEntry {
	return t.GetUnsentRSSItemsForDestination(webhookURL)
}

// GetUnsentRSSItemsForDestination returns RSS entries not yet sent to a stable destination, sorted chronologically.
func (t *Tracker) GetUnsentRSSItemsForDestination(destinationID string, legacyDestinations ...string) []StoredRSSEntry {
	return t.GetUnsentRSSItemsForDestinationFeedType(destinationID, "", nil, legacyDestinations...)
}

// GetUnsentRSSItemsForDestinationFeedType returns unsent RSS entries for one destination and feed scope.
// Entries with a persisted feed_type match that first; older entries without feed_type use feedURLs as fallback.
func (t *Tracker) GetUnsentRSSItemsForDestinationFeedType(
	destinationID string,
	feedType string,
	feedURLs map[string]struct{},
	legacyDestinations ...string,
) []StoredRSSEntry {
	for {
		t.mutex.Lock()
		t.ensureParsedReadableLocked()
		t.mutex.Unlock()

		t.mutex.RLock()
		if t.parsedSortDirty || t.parsedIndexDirty {
			t.mutex.RUnlock()
			continue
		}

		var unsent []StoredRSSEntry
		migrations := make([]rssSentLegacyMigration, 0)
		for _, idx := range t.parsedRSSIndexesForFeedScopeReadLocked(feedType, feedURLs) {
			if idx < 0 || idx >= len(t.rssStatus.ParsedItems) {
				continue
			}
			entry := t.rssStatus.ParsedItems[idx]
			sent, legacyDestination := t.rssItemSentToDestinationReadLocked(entry.Key, destinationID, legacyDestinations...)
			if !sent {
				unsent = append(unsent, cloneStoredRSSEntry(entry))
				continue
			}
			if legacyDestination != "" {
				migrations = append(migrations, rssSentLegacyMigration{
					itemKey:           entry.Key,
					destinationID:     destinationID,
					legacyDestination: legacyDestination,
				})
			}
		}

		t.mutex.RUnlock()
		t.migrateRSSSentLegacyDestinations(migrations)

		log.WithFields(log.Fields{
			"unsent_count": len(unsent),
		}).Debug("Retrieved unsent RSS items for webhook")
		return unsent
	}
}

// GetUnsentRSSRecoveryItemsForDestinationFeedType returns unsent RSS recovery
// items for one destination and feed scope without exposing the persisted JSON
// storage record to callers.
func (t *Tracker) GetUnsentRSSRecoveryItemsForDestinationFeedType(
	destinationID string,
	feedType string,
	feedURLs map[string]struct{},
	legacyDestinations ...string,
) []UnsentRSSItem {
	storedItems := t.GetUnsentRSSItemsForDestinationFeedType(destinationID, feedType, feedURLs, legacyDestinations...)
	if len(storedItems) == 0 {
		return nil
	}
	items := make([]UnsentRSSItem, 0, len(storedItems))
	for _, storedItem := range storedItems {
		items = append(items, UnsentRSSItemFromStored(storedItem))
	}
	return items
}

func (t *Tracker) parsedRSSIndexesForFeedScopeReadLocked(feedType string, feedURLs map[string]struct{}) []int {
	if feedType == "" && len(feedURLs) == 0 {
		indexes := make([]int, len(t.rssStatus.ParsedItems))
		for i := range t.rssStatus.ParsedItems {
			indexes[i] = i
		}
		return indexes
	}

	indexes := make([]int, 0)
	seen := make(map[int]struct{})
	addIndex := func(idx int) {
		if idx < 0 || idx >= len(t.rssStatus.ParsedItems) {
			return
		}
		if _, exists := seen[idx]; exists {
			return
		}
		seen[idx] = struct{}{}
		indexes = append(indexes, idx)
	}

	if feedType != "" {
		for _, idx := range t.parsedFeedTypeIndex[feedType] {
			addIndex(idx)
		}
	}
	for feedURL := range feedURLs {
		for _, idx := range t.parsedFeedIndex[feedURL] {
			entry := t.rssStatus.ParsedItems[idx]
			if entry.FeedType != "" && entry.FeedType != feedType {
				continue
			}
			addIndex(idx)
		}
	}
	if feedType != "" {
		for _, idx := range t.parsedUntypedIndex {
			entry := t.rssStatus.ParsedItems[idx]
			if _, feedURLMatched := feedURLs[entry.FeedURL]; feedURLMatched {
				continue
			}
			addIndex(idx)
		}
	}
	sort.Ints(indexes)
	return indexes
}

func (t *Tracker) markRSSParsedFullSortDirtyLocked() {
	t.parsedSortDirty = true
	t.parsedFullSortDirty = true
	t.parsedDirtyStart = 0
}

func (t *Tracker) markRSSParsedAppendDirtyLocked(appendIndex int) {
	if t.parsedFullSortDirty {
		t.parsedSortDirty = true
		return
	}
	if !t.parsedSortDirty || appendIndex < t.parsedDirtyStart {
		t.parsedDirtyStart = appendIndex
	}
	t.parsedSortDirty = true
}

// ensureSorted sorts ParsedItems if the sort-dirty flag is set, then rebuilds the index.
// NOTE: Must be called with t.mutex locked (write).
func (t *Tracker) ensureSorted() {
	if !t.parsedSortDirty {
		return
	}
	if t.parsedFullSortDirty {
		t.sortRSSParsedItems()
	} else {
		t.sortRSSParsedDirtyTail()
	}
	t.rebuildParsedIndex()
	t.parsedSortDirty = false
	t.parsedFullSortDirty = false
	t.parsedDirtyStart = 0
	t.parsedIndexDirty = false
}

func (t *Tracker) ensureParsedReadableLocked() {
	if t.parsedSortDirty {
		t.ensureSorted()
		return
	}
	if t.parsedIndexDirty {
		t.rebuildParsedIndex()
		t.parsedIndexDirty = false
	}
}

// rebuildParsedIndex rebuilds the O(1) lookup index from the ParsedItems slice.
// NOTE: Must be called with t.mutex locked.
func (t *Tracker) rebuildParsedIndex() {
	t.parsedIndex = make(map[string]int, len(t.rssStatus.ParsedItems))
	t.parsedFeedIndex = make(map[string][]int)
	t.parsedFeedTypeIndex = make(map[string][]int)
	t.parsedUntypedIndex = nil
	for i, entry := range t.rssStatus.ParsedItems {
		t.parsedIndex[parsedIndexKey(entry.FeedURL, entry.Key)] = i
		if entry.FeedURL != "" {
			t.parsedFeedIndex[entry.FeedURL] = append(t.parsedFeedIndex[entry.FeedURL], i)
		}
		if entry.FeedType != "" {
			t.parsedFeedTypeIndex[entry.FeedType] = append(t.parsedFeedTypeIndex[entry.FeedType], i)
		} else {
			t.parsedUntypedIndex = append(t.parsedUntypedIndex, i)
		}
	}
	t.parsedIndexDirty = false
}

// SavePendingChanges flushes any dirty in-memory state to disk.
// Call this after batch operations (parse cycle, send cycle) to persist changes.
func (t *Tracker) SavePendingChanges() error {
	t.saveMu.Lock()
	defer t.saveMu.Unlock()

	// Wait for any audit writes reserved-but-not-yet-flushed by a mutator
	// that already released t.mutex: without this, a state mutation could be
	// snapshotted and persisted here before its accompanying audit line is
	// durable, which is exactly the ordering guarantee the synchronous
	// under-mutex write gave for free before this change. waitForZero runs
	// outside t.mutex (so a stalled audit disk does not block every OTHER
	// tracker caller); the isZero recheck runs inside t.mutex so nothing can
	// reserve a new write between the wait returning and the lock being
	// acquired without us seeing it.
	//
	// Honest scope note: SavePendingChanges is called from many per-item and
	// per-job hot paths in internal/scheduler (one call per delivered item or
	// retry job), not once per poll cycle -- so a goroutine that mutates
	// state and immediately calls SavePendingChanges itself, in the same
	// call sequence, still blocks here for the full stall waiting on its own
	// just-made reservation, exactly as it would have blocked on t.mutex
	// before this change. This barrier does not make that sequential case
	// faster. The real win is for two other cases: any concurrent reader of
	// the tracker (no longer blocks at all), and concurrent sibling mutators
	// (e.g. several processAPIRetryJob goroutines in the same cycle) racing
	// the same stall, which collapse from fully serialized behind t.mutex to
	// running their audit writes in parallel and waiting on this shared
	// barrier together.
	for {
		t.auditFlight.waitForZero()
		t.mutex.Lock()
		if t.auditFlight.isZero() {
			break
		}
		t.mutex.Unlock()
	}

	if t.inMemory {
		if t.parsedSortDirty {
			t.ensureSorted()
		} else if t.parsedIndexDirty {
			t.rebuildParsedIndex()
		}
		t.rssDirty = false
		t.apiDirty = false
		t.retryStore.dirty = false
		t.mutex.Unlock()
		return nil
	}

	var apiSnapshot *apiStatus
	var rssSnapshot *rssStatus
	var retrySnapshot *retryStatus

	if t.rssDirty {
		t.ensureSorted()
		rssSnapshot = cloneRSSStatus(t.rssStatus)
		t.rssDirty = false
	}
	if t.apiDirty {
		if t.apiLoaded {
			apiSnapshot = cloneAPIStatus(t.apiStatus)
			t.apiDirty = false
		} else if !t.apiSaveSkipWarned {
			t.apiSaveSkipWarned = true
			log.WithField("file", filepath.Join(t.dataDir, statusAPIFileName)).
				Warn("Skipping API status save: api_status.json is not loaded; keeping the persisted dedup state")
		}
	}
	if t.retryStore.dirty {
		retrySnapshot = cloneRetryStatus(t.retryStore.status)
		t.retryStore.dirty = false
	}
	t.mutex.Unlock()

	var saveErrors []error
	if rssSnapshot != nil {
		if err := t.saveRSSStatusSnapshot(rssSnapshot); err != nil {
			log.WithError(err).Error("Failed to save pending RSS status")
			saveErrors = append(saveErrors, fmt.Errorf("save pending RSS status: %w", err))
			t.markDirtyAfterSaveFailure("rss")
		}
	}

	if apiSnapshot != nil {
		if err := t.saveAPIStatusSnapshot(apiSnapshot); err != nil {
			log.WithError(err).Error("Failed to save pending API status")
			saveErrors = append(saveErrors, fmt.Errorf("save pending API status: %w", err))
			t.markDirtyAfterSaveFailure("api")
		}
	}

	if retrySnapshot != nil {
		if err := t.saveRetryStatusSnapshot(retrySnapshot); err != nil {
			log.WithError(err).Error("Failed to save pending retry status")
			saveErrors = append(saveErrors, fmt.Errorf("save pending retry status: %w", err))
			t.markDirtyAfterSaveFailure("retry")
		}
	}
	return errors.Join(saveErrors...)
}

func (t *Tracker) markDirtyAfterSaveFailure(statusType string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	switch statusType {
	case "api":
		t.apiDirty = true
	case "rss":
		t.rssDirty = true
	case "retry":
		t.retryStore.dirty = true
	}
}

// sortRSSParsedItems sorts the parsed RSS items by published time (oldest first).
func (t *Tracker) sortRSSParsedItems() {
	sortStoredRSSParsedItems(t.rssStatus.ParsedItems)
}

// sortRSSParsedDirtyTail sorts only newly appended RSS entries and merges them
// with the already-sorted prefix. This keeps append-only RSS batches from
// paying a full-history O(n log n) sort during SavePendingChanges.
func (t *Tracker) sortRSSParsedDirtyTail() {
	dirtyStart := t.parsedDirtyStart
	if dirtyStart <= 0 {
		t.sortRSSParsedItems()
		return
	}
	if dirtyStart >= len(t.rssStatus.ParsedItems) {
		return
	}

	tail := t.rssStatus.ParsedItems[dirtyStart:]
	sortStoredRSSParsedItems(tail)
	if len(tail) == 0 || !storedRSSEntryBefore(tail[0], t.rssStatus.ParsedItems[dirtyStart-1]) {
		return
	}

	t.mergeRSSParsedSortedTail(dirtyStart)
}

func (t *Tracker) mergeRSSParsedSortedTail(dirtyStart int) {
	prefix := t.rssStatus.ParsedItems[:dirtyStart]
	tail := t.rssStatus.ParsedItems[dirtyStart:]
	merged := make([]StoredRSSEntry, 0, len(t.rssStatus.ParsedItems))

	i := 0
	j := 0
	for i < len(prefix) && j < len(tail) {
		if storedRSSEntryBefore(tail[j], prefix[i]) {
			merged = append(merged, tail[j])
			j++
			continue
		}
		merged = append(merged, prefix[i])
		i++
	}
	merged = append(merged, prefix[i:]...)
	merged = append(merged, tail[j:]...)
	copy(t.rssStatus.ParsedItems, merged)
}

func sortStoredRSSParsedItems(entries []StoredRSSEntry) {
	if len(entries) < 2 {
		return
	}

	// Pre-parse all times to avoid parsing during sort comparison.
	type itemWithTime struct {
		item StoredRSSEntry
		time time.Time
	}

	items := make([]itemWithTime, len(entries))
	for i, entry := range entries {
		items[i] = itemWithTime{entry, storedRSSSortTime(entry)}
	}

	// Sort by pre-parsed times.
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].time.Before(items[j].time)
	})

	// Reconstruct sorted slice.
	for i, item := range items {
		entries[i] = item.item
	}
}

func storedRSSEntryBefore(left, right StoredRSSEntry) bool {
	return storedRSSSortTime(left).Before(storedRSSSortTime(right))
}

func storedRSSSortTime(entry StoredRSSEntry) time.Time {
	parsedTime := timeutil.ParseFlexibleTimestampOrZero(entry.Published)
	if parsedTime.IsZero() && entry.ParsedAt != "" {
		parsedTime = parseStoredRSSPublished(entry.ParsedAt)
	}
	return parsedTime
}

func dedupeLoadedRSSParsedItems(items []StoredRSSEntry) []StoredRSSEntry {
	if len(items) < 2 {
		return items
	}

	indexByKey := make(map[string]int, len(items))
	deduped := items[:0]
	for _, item := range items {
		key := parsedIndexKey(item.FeedURL, item.Key)
		if existingIndex, exists := indexByKey[key]; exists {
			if storedRSSLoadTime(item).After(storedRSSLoadTime(deduped[existingIndex])) {
				deduped[existingIndex] = item
			}
			continue
		}
		indexByKey[key] = len(deduped)
		deduped = append(deduped, item)
	}
	return deduped
}

func storedRSSLoadTime(item StoredRSSEntry) time.Time {
	if parsed := parseStatusTimestamp(item.ParsedAt); !parsed.IsZero() {
		return parsed
	}
	return parseStoredRSSPublished(item.Published)
}

// CleanupOldEntries performs cleanup of old entries for both API and RSS status.
// This should be called once after processing a batch of items, not after each individual item.
// This approach is much more efficient than cleaning up after every single item (O(n) vs O(n*m)).
func (t *Tracker) CleanupOldEntries() {
	t.cleanupOldEntries(nil)
}

// CleanupOldEntriesForRSSWebhooks performs cleanup while preserving RSS entries
// until they have been sent to every currently enabled RSS webhook target.
func (t *Tracker) CleanupOldEntriesForRSSWebhooks(webhookURLs []string) {
	t.cleanupOldRSSEntries(webhookURLs)
}

// CleanupOldEntriesForRSSDestinations performs cleanup while preserving RSS entries
// until they have been sent to every currently enabled logical RSS destination.
func (t *Tracker) CleanupOldEntriesForRSSDestinations(destinationIDs []string) {
	t.cleanupOldRSSEntries(destinationIDs)
}

// cleanupOldEntries, cleanupOldRSSEntries and the other four
// t.mutex-holding entry points in retry_store.go establish a single deferred
// closure right after t.mutex.Lock() instead of unlocking on each return
// branch: Go runs a deferred function during a panicking function's stack
// unwind regardless of where recover() eventually happens (internal/scheduler
// recovers every poll cycle's panics, scheduler.go recoverSchedulerPanic), so
// this is the only shape under which a panic anywhere in the locked body
// still releases t.mutex instead of wedging every future Tracker caller
// forever (review Finding A, HIGH, 2026-09-03).
func (t *Tracker) cleanupOldEntries(rssWebhookURLs []string) {
	t.mutex.Lock()
	var events []DeliveryAuditEvent
	defer func() {
		t.auditFlight.reserve(len(events))
		t.mutex.Unlock()
		t.flushDeliveryAuditEvents(events)
	}()
	triggerTestAuditEntryPointPanicHook()

	// Cleanup API sent items
	t.cleanupOldAPISentItems(&events)

	t.cleanupRSSAndRetryLocked(&events, rssWebhookURLs)

	log.Debug("Completed batch cleanup of old entries")
}

func (t *Tracker) cleanupOldRSSEntries(rssWebhookURLs []string) {
	t.mutex.Lock()
	var events []DeliveryAuditEvent
	defer func() {
		t.auditFlight.reserve(len(events))
		t.mutex.Unlock()
		t.flushDeliveryAuditEvents(events)
	}()
	triggerTestAuditEntryPointPanicHook()

	t.cleanupRSSAndRetryLocked(&events, rssWebhookURLs)

	log.Debug("Completed RSS cleanup of old entries")
}

func (t *Tracker) cleanupRSSAndRetryLocked(events *[]DeliveryAuditEvent, rssWebhookURLs []string) {
	// Cleanup RSS items (synchronized to prevent orphaned entries)
	t.cleanupRSSItemsSynchronized(events, rssWebhookURLs)

	// Cleanup retry and dead-letter status so failed delivery metadata cannot grow forever.
	t.cleanupRetryStatusLocked(events)
}

// recordCleanupAudit appends a normalized event to *events instead of writing
// it -- no I/O, still requires t.mutex held (only the finalized *events value
// crosses the unlock boundary; see flushDeliveryAuditEvents).
func (t *Tracker) recordCleanupAudit(events *[]DeliveryAuditEvent, source, itemType, reason string, count int, details map[string]string) {
	if count <= 0 {
		return
	}
	*events = append(*events, normalizeDeliveryAuditEvent(DeliveryAuditEvent{
		EventType: DeliveryAuditEventCleanup,
		Source:    source,
		ItemType:  itemType,
		Outcome:   DeliveryAuditOutcomePruned,
		Reason:    reason,
		Count:     count,
		Details:   details,
	}))
}

// cleanupRSSItemsSynchronized performs synchronized cleanup of both parsed_items and sent_items
// to prevent orphaned entries that could cause duplicate sends.
// NOTE: Must be called with t.mutex locked
func (t *Tracker) cleanupRSSItemsSynchronized(events *[]DeliveryAuditEvent, webhookURLs []string) {
	t.ensureSorted()

	retention := t.retention
	initialParsedCount := len(t.rssStatus.ParsedItems)
	initialSentCount := len(t.rssStatus.SentItems)
	requiredWebhookURLs := uniqueNonEmptyStrings(webhookURLs)

	log.WithFields(log.Fields{
		"parsed_items_count": initialParsedCount,
		"sent_items_count":   initialSentCount,
		"max_items":          retention.MaxRSSParsedItems,
		"max_age":            retention.RSSParsedMaxAge,
		"rss_webhook_count":  len(requiredWebhookURLs),
	}).Debug("Starting synchronized RSS cleanup")

	protectedSentItemKeys := t.currentRSSParsedSentItemKeysLocked()
	retentionSentItemsRemoved := t.cleanupRSSSentItemsRetentionLocked(events, protectedSentItemKeys)

	countCleanupNeeded := initialParsedCount > retention.MaxRSSParsedItems
	ageCleanupEnabled := retention.RSSParsedMaxAge > 0
	if !countCleanupNeeded && !ageCleanupEnabled {
		if retentionSentItemsRemoved == 0 {
			log.Debug("RSS cleanup not needed")
		}
		return
	}

	if len(requiredWebhookURLs) == 0 {
		if countCleanupNeeded {
			log.WithFields(log.Fields{
				"parsed_items_count": initialParsedCount,
				"max_items":          retention.MaxRSSParsedItems,
			}).Warn("RSS retention cap exceeded without webhook target context; preserving parsed RSS backlog")
		} else {
			log.Debug("RSS age cleanup skipped without webhook target context")
		}
		return
	}

	// Remove only the oldest entries that have completed delivery to every
	// enabled RSS webhook target. If a webhook is in quiet hours or has been
	// failing, the parsed item remains available for recovery delivery.
	excessCount := initialParsedCount - retention.MaxRSSParsedItems
	if excessCount < 0 {
		excessCount = 0
	}
	itemsToRemove := make([]StoredRSSEntry, 0, excessCount)
	itemsToKeep := make([]StoredRSSEntry, 0, initialParsedCount)
	now := statusNow()

	for _, item := range t.rssStatus.ParsedItems {
		removeForCount := excessCount > 0
		removeForAge := ageCleanupEnabled && storedRSSEntryOlderThan(item, now, retention.RSSParsedMaxAge)
		if (removeForCount || removeForAge) && t.rssItemSentToAllWebhookURLs(item.Key, requiredWebhookURLs) {
			itemsToRemove = append(itemsToRemove, item)
			if removeForCount {
				excessCount--
			}
			continue
		}
		itemsToKeep = append(itemsToKeep, item)
	}

	if len(itemsToRemove) == 0 {
		if countCleanupNeeded {
			log.WithFields(log.Fields{
				"parsed_items_count": initialParsedCount,
				"max_items":          retention.MaxRSSParsedItems,
				"rss_webhook_count":  len(requiredWebhookURLs),
			}).Warn("RSS retention cap exceeded, but no fully delivered parsed items are safe to remove")
		} else {
			log.Debug("RSS age cleanup found no fully delivered entries to remove")
		}
		return
	}

	clippedItems := make([]StoredRSSEntry, len(itemsToKeep))
	copy(clippedItems, itemsToKeep)
	t.rssStatus.ParsedItems = clippedItems

	log.WithFields(log.Fields{
		"items_to_remove":   len(itemsToRemove),
		"oldest_item_key":   itemsToRemove[0].Key,
		"oldest_item_title": itemsToRemove[0].Title,
	}).Debug("Removing old parsed items")

	// Remove corresponding entries from sent_items to keep lists synchronized.
	removedSentItemKeys := make(map[string]struct{}, len(itemsToRemove)*2)
	for _, removedItem := range itemsToRemove {
		removedSentItemKeys[removedItem.Key] = struct{}{}
		for _, contentSig := range storedRSSContentSignatures(removedItem) {
			removedSentItemKeys[contentSig] = struct{}{}
		}
	}
	sentItemsRemoved := 0
	for compositeKey, sentInfo := range t.rssStatus.SentItems {
		if _, remove := removedSentItemKeys[sentInfo.ItemKey]; remove {
			delete(t.rssStatus.SentItems, compositeKey)
			sentItemsRemoved++
			log.WithFields(log.Fields{
				"item_key":      sentInfo.ItemKey,
				"composite_key": compositeKey,
			}).Debug("Removed sent_item entry for deleted parsed_item")
		}
	}
	retryItemsRemoved := t.moveRSSRetryRowsForRemovedParsedItemsLocked(events, removedSentItemKeys)

	// Rebuild index after slice modification
	t.rebuildParsedIndex()
	t.rssDirty = true

	log.WithFields(log.Fields{
		"parsed_items_removed": len(itemsToRemove),
		"sent_items_removed":   sentItemsRemoved,
		"retry_items_removed":  retryItemsRemoved,
		"parsed_items_kept":    len(t.rssStatus.ParsedItems),
		"sent_items_kept":      len(t.rssStatus.SentItems),
	}).Info("Synchronized RSS cleanup completed")
	t.recordCleanupAudit(events, "rss", RetryItemTypeRSS.String(), DeliveryAuditReasonRSSRetentionPruned, len(itemsToRemove), map[string]string{
		"sent_items_removed":         fmt.Sprint(sentItemsRemoved),
		"retry_items_dead_lettered":  fmt.Sprint(retryItemsRemoved),
		"parsed_items_kept":          fmt.Sprint(len(t.rssStatus.ParsedItems)),
		"sent_items_kept":            fmt.Sprint(len(t.rssStatus.SentItems)),
		"oldest_removed_item_key":    itemsToRemove[0].Key,
		"required_destination_count": fmt.Sprint(len(requiredWebhookURLs)),
	})

	if len(t.rssStatus.ParsedItems) > retention.MaxRSSParsedItems {
		log.WithFields(log.Fields{
			"parsed_items_count": len(t.rssStatus.ParsedItems),
			"max_items":          retention.MaxRSSParsedItems,
		}).Warn("RSS parsed backlog remains above retention cap because older items are not fully delivered")
	}
}

func (t *Tracker) moveRSSRetryRowsForRemovedParsedItemsLocked(events *[]DeliveryAuditEvent, removedItemKeys map[string]struct{}) int {
	if len(removedItemKeys) == 0 || len(t.retryStore.status.RetryQueue) == 0 {
		return 0
	}

	removed := 0
	for queueKey, item := range t.retryStore.status.RetryQueue {
		if item.ItemType != RetryItemTypeRSS.String() {
			continue
		}
		if _, remove := removedItemKeys[item.ItemKey]; !remove {
			continue
		}
		t.moveToDeadLetterLocked(events, queueKey, item, TerminalReasonRSSParsedPruned)
		removed++
	}
	return removed
}

func (t *Tracker) currentRSSParsedSentItemKeysLocked() map[string]struct{} {
	if len(t.rssStatus.ParsedItems) == 0 {
		return nil
	}
	keys := make(map[string]struct{}, len(t.rssStatus.ParsedItems)*2)
	for _, item := range t.rssStatus.ParsedItems {
		keys[item.Key] = struct{}{}
		for _, contentSig := range storedRSSContentSignatures(item) {
			keys[contentSig] = struct{}{}
		}
	}
	return keys
}

func (t *Tracker) cleanupRSSSentItemsRetentionLocked(events *[]DeliveryAuditEvent, protectedItemKeys map[string]struct{}) int {
	if len(t.rssStatus.SentItems) == 0 {
		return 0
	}

	retention := t.retention
	removed := 0
	now := statusNow()
	if retention.RSSParsedMaxAge > 0 {
		for compositeKey, sentInfo := range t.rssStatus.SentItems {
			if _, protected := protectedItemKeys[sentInfo.ItemKey]; protected {
				continue
			}
			if !sentInfo.SentAt.IsZero() && now.Sub(sentInfo.SentAt) > retention.RSSParsedMaxAge {
				delete(t.rssStatus.SentItems, compositeKey)
				removed++
			}
		}
	}

	if len(t.rssStatus.SentItems) > retention.MaxRSSSentItems {
		items := make([]rssSentItemWithTime, 0, len(t.rssStatus.SentItems))
		for compositeKey, sentInfo := range t.rssStatus.SentItems {
			if _, protected := protectedItemKeys[sentInfo.ItemKey]; protected {
				continue
			}
			items = append(items, rssSentItemWithTime{
				compositeKey: compositeKey,
				sentAt:       sentInfo.SentAt,
			})
		}
		sort.Slice(items, func(i, j int) bool {
			return statusTimeBefore(items[i].sentAt, items[j].sentAt)
		})

		removeCount := len(t.rssStatus.SentItems) - retention.MaxRSSSentItems
		if removeCount > len(items) {
			removeCount = len(items)
		}
		for i := 0; i < removeCount; i++ {
			delete(t.rssStatus.SentItems, items[i].compositeKey)
			removed++
		}
	}

	if removed > 0 {
		t.rssDirty = true
		log.WithFields(log.Fields{
			"removed":   removed,
			"kept":      len(t.rssStatus.SentItems),
			"max_age":   retention.RSSParsedMaxAge,
			"max_count": retention.MaxRSSSentItems,
		}).Info("Cleaned up RSS sent_items retention")
		t.recordCleanupAudit(events, "rss", RetryItemTypeRSS.String(), DeliveryAuditReasonSentItemRetention, removed, map[string]string{
			"sent_items_kept": fmt.Sprint(len(t.rssStatus.SentItems)),
		})
	}
	return removed
}

type rssSentItemWithTime struct {
	compositeKey string
	sentAt       time.Time
}

func storedRSSEntryOlderThan(item StoredRSSEntry, now time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}

	itemTime := parseStoredRSSPublished(item.Published)
	if itemTime.IsZero() && item.ParsedAt != "" {
		itemTime = parseStoredRSSPublished(item.ParsedAt)
	}
	if itemTime.IsZero() {
		return false
	}
	return now.Sub(itemTime) > maxAge
}

func parseStatusTimestamp(value string) time.Time {
	return timeutil.ParseFlexibleTimestampOrZero(value)
}

func formatStatusTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseRequiredStatusTimestamp(value string) (time.Time, error) {
	parsed := parseStatusTimestamp(value)
	if parsed.IsZero() {
		return time.Time{}, fmt.Errorf("unsupported timestamp format")
	}
	return parsed, nil
}

func statusTimeBefore(left, right time.Time) bool {
	if left.IsZero() {
		return true
	}
	if right.IsZero() {
		return false
	}
	return left.Before(right)
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func storedRSSContentSignatures(item StoredRSSEntry) []string {
	signatures := make([]string, 0, 7)
	if entry, err := RSSEntryFromStored(item); err == nil {
		signatures = append(signatures, rss.GenerateEntryContentSignatureLookupKeys(entry)...)
	}
	published := parseStoredRSSPublished(item.Published)
	signatures = append(signatures,
		storedRSSContentSignatureV3(item.Title, published, item.Description),
		storedRSSContentSignatureV3(item.Title, published, ""),
		storedRSSContentSignatureLegacyV3(item.FeedURL, item.Title, published, item.Description),
		storedRSSContentSignatureLegacyV3(item.FeedURL, item.Title, published, ""),
		storedRSSContentSignatureV2(item.FeedURL, item.Title, published),
		storedRSSContentSignatureLegacy(item.FeedURL, item.Title, published),
	)
	return uniqueNonEmptyStrings(signatures)
}

func storedRSSContentSignatureV3(title string, published time.Time, description string) string {
	return "content-sig:v3:" + hashStatusKey(
		"content-v3",
		strings.ToLower(strings.TrimSpace(title)),
		statusSignatureTimestamp(published),
		normalizeStatusDescription(description),
	)
}

func storedRSSContentSignatureLegacyV3(feedURL, title string, published time.Time, description string) string {
	return "content-sig:v3:" + hashStatusKey(
		"content-v3",
		feedURL,
		strings.ToLower(strings.TrimSpace(title)),
		statusSignatureTimestamp(published),
		normalizeStatusDescription(description),
	)
}

func storedRSSContentSignatureV2(feedURL, title string, published time.Time) string {
	return "content-sig:v2:" + hashStatusKey(
		"content",
		feedURL,
		strings.ToLower(strings.TrimSpace(title)),
		statusSignatureDate(published),
	)
}

func storedRSSContentSignatureLegacy(feedURL, title string, published time.Time) string {
	normalizedTitle := strings.ToLower(strings.TrimSpace(title))
	return "content-sig:" + feedURL + ":" + normalizedTitle + ":" + statusSignatureDate(published)
}

func statusSignatureTimestamp(published time.Time) string {
	if published.IsZero() {
		return ""
	}
	return published.UTC().Format(time.RFC3339Nano)
}

func statusSignatureDate(published time.Time) string {
	if published.IsZero() {
		return ""
	}
	return timeutil.FormatDateOnly(published)
}

func normalizeStatusDescription(description string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(description)), " ")
}

func hashStatusKey(namespace string, values ...string) string {
	h := sha256.New()
	h.Write([]byte(namespace))
	h.Write([]byte{0})
	for _, value := range values {
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func parseStoredRSSPublished(value string) time.Time {
	return timeutil.ParseFlexibleTimestampOrZero(value)
}

func (t *Tracker) rssItemSentToAllWebhookURLs(itemKey string, webhookURLs []string) bool {
	for _, webhookURL := range webhookURLs {
		if !t.rssItemSentToDestinationLocked(itemKey, webhookURL) {
			return false
		}
	}
	return true
}

func (t *Tracker) rebuildAPISentHeapLocked() {
	t.apiSentHeap = make(apiSentItemHeap, 0, len(t.apiStatus.SentItems))
	for compositeKey, sentInfo := range t.apiStatus.SentItems {
		t.apiSentHeap = append(t.apiSentHeap, apiSentHeapItem{
			compositeKey: compositeKey,
			sentAt:       sentInfo.SentAt,
		})
	}
	heap.Init(&t.apiSentHeap)
}

func (t *Tracker) indexAPISentItemLocked(compositeKey string, sentInfo webhookSentInfo) {
	heap.Push(&t.apiSentHeap, apiSentHeapItem{
		compositeKey: compositeKey,
		sentAt:       sentInfo.SentAt,
	})
}

// cleanupOldAPISentItems bounds API sent tombstones by count only. API sent
// markers are deduplication state, so age-based deletion can replay old alerts
// if the upstream API keeps or reintroduces them.
// NOTE: Must be called with t.mutex locked
func (t *Tracker) cleanupOldAPISentItems(events *[]DeliveryAuditEvent) {
	maxSentItems := t.retention.MaxAPISentItems
	removed := 0
	for len(t.apiStatus.SentItems) > maxSentItems {
		if len(t.apiSentHeap) == 0 {
			t.rebuildAPISentHeapLocked()
			if len(t.apiSentHeap) == 0 {
				break
			}
		}

		popped := heap.Pop(&t.apiSentHeap)
		oldest, ok := popped.(apiSentHeapItem)
		if !ok {
			panic(fmt.Sprintf("apiSentItemHeap: unexpected element type %T", popped))
		}
		current, exists := t.apiStatus.SentItems[oldest.compositeKey]
		if !exists || !current.SentAt.Equal(oldest.sentAt) {
			continue
		}

		delete(t.apiStatus.SentItems, oldest.compositeKey)
		removed++
	}

	if removed > 0 {
		t.apiDirty = true

		log.WithFields(log.Fields{
			"removed": removed,
			"kept":    len(t.apiStatus.SentItems),
		}).Debug("Cleaned up old API sent items by count")
		t.recordCleanupAudit(events, "api", RetryItemTypeAPI.String(), "api_sent_items_count_pruned", removed, map[string]string{
			"kept": fmt.Sprint(len(t.apiStatus.SentItems)),
		})
	}
}

// saveAPIStatus saves the API status to JSON file
func (t *Tracker) saveAPIStatus() error {
	return t.saveAPIStatusSnapshot(t.apiStatus)
}

func (t *Tracker) saveAPIStatusSnapshot(snapshot *apiStatus) error {
	filePath := filepath.Join(t.dataDir, statusAPIFileName)

	if err := writeAtomicJSONFileFunc(filePath, "API status", snapshot); err != nil {
		return err
	}

	log.WithField("file", filePath).Debug("API status saved to file with sorted entries")
	return nil
}

// saveRSSStatus saves the RSS status to JSON file
func (t *Tracker) saveRSSStatus() error {
	return t.saveRSSStatusSnapshot(t.rssStatus)
}

func (t *Tracker) saveRSSStatusSnapshot(snapshot *rssStatus) error {
	filePath := filepath.Join(t.dataDir, statusRSSFileName)

	if err := writeAtomicJSONFileFunc(filePath, "RSS status", snapshot); err != nil {
		return err
	}

	log.WithField("file", filePath).Debug("RSS status saved to file")
	return nil
}

// loadAPIStatus loads API status from JSON file if it exists
func (t *Tracker) loadAPIStatus() error {
	filePath := filepath.Join(t.dataDir, statusAPIFileName)

	var loadedStatus apiStatus
	loaded, err := loadJSONStatus(filePath, "API status", &loadedStatus, func(status *apiStatus) {
		if status.SentItems == nil {
			status.SentItems = make(map[string]webhookSentInfo)
		}
	})
	if err != nil {
		return err
	}
	if !loaded {
		t.mutex.Lock()
		t.apiLoaded = true
		t.mutex.Unlock()
		return nil
	}

	// Lock mutex before modifying shared state
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.apiStatus = &loadedStatus
	t.rebuildAPISentHeapLocked()
	t.apiLoaded = true

	log.WithFields(log.Fields{
		"file":         filePath,
		"last_updated": t.apiStatus.LastUpdated,
		"sent_items":   len(t.apiStatus.SentItems),
	}).Info("API status loaded from file")

	return nil
}

// loadRSSStatus loads RSS status from JSON file if it exists
func (t *Tracker) loadRSSStatus() error {
	filePath := filepath.Join(t.dataDir, statusRSSFileName)

	loadedStatus, loaded, err := readRSSStatusFile(filePath)
	if err != nil || !loaded {
		return err
	}
	parsedItemsDeduped := normalizeRSSStatus(&loadedStatus)
	t.installRSSStatusLocked(&loadedStatus, parsedItemsDeduped > 0)
	t.logRSSStatusLoaded(filePath, parsedItemsDeduped)
	return nil
}

func readRSSStatusFile(filePath string) (rssStatus, bool, error) {
	var loadedStatus rssStatus
	loaded, err := loadJSONStatus(filePath, "RSS status", &loadedStatus, func(status *rssStatus) {
		if status.Feeds == nil {
			status.Feeds = make(map[string]FeedInfo)
		}
		if status.ParsedItems == nil {
			status.ParsedItems = make([]StoredRSSEntry, 0)
		}
		if status.SentItems == nil {
			status.SentItems = make(map[string]rssWebhookSentInfo)
		}
	})
	return loadedStatus, loaded, err
}

func normalizeRSSStatus(status *rssStatus) int {
	if status == nil {
		return 0
	}
	migratedLegacyProcessedItems := migrateLegacyRSSProcessedItems(status)
	parsedItemsBeforeDedupe := len(status.ParsedItems)
	status.ParsedItems = dedupeLoadedRSSParsedItems(status.ParsedItems)
	backfilledContentSignatureSentItems := backfillRSSContentSignatureSentItems(status)
	return migratedLegacyProcessedItems + parsedItemsBeforeDedupe - len(status.ParsedItems) + backfilledContentSignatureSentItems
}

func backfillRSSContentSignatureSentItems(status *rssStatus) int {
	if status == nil || len(status.ParsedItems) == 0 || len(status.SentItems) == 0 {
		return 0
	}

	parsedByKey := make(map[string]StoredRSSEntry, len(status.ParsedItems))
	for _, item := range status.ParsedItems {
		if strings.TrimSpace(item.Key) == "" {
			continue
		}
		if _, exists := parsedByKey[item.Key]; !exists {
			parsedByKey[item.Key] = item
		}
	}
	if len(parsedByKey) == 0 {
		return 0
	}

	sentItems := make([]rssWebhookSentInfo, 0, len(status.SentItems))
	for _, sentInfo := range status.SentItems {
		sentItems = append(sentItems, sentInfo)
	}

	backfilled := 0
	for _, sentInfo := range sentItems {
		backfilled += backfillContentSignatureAliasesForSentItem(status, sentInfo, parsedByKey)
	}

	return backfilled
}

// backfillContentSignatureAliasesForSentItem mints the content-signature dedup
// aliases in status.SentItems for one primary sent-item record. Split out of
// backfillRSSContentSignatureSentItems (gocyclo 22) purely to bring per-function
// cyclomatic complexity under the central lint profile's threshold; same
// checks, same order, same behaviour as before the split.
func backfillContentSignatureAliasesForSentItem(status *rssStatus, sentInfo rssWebhookSentInfo, parsedByKey map[string]StoredRSSEntry) int {
	primaryItemKey := strings.TrimSpace(sentInfo.ItemKey)
	if primaryItemKey == "" || strings.HasPrefix(primaryItemKey, "content-sig:") {
		return 0
	}
	// Filter-mismatch skip markers say "do not re-evaluate this entry", not
	// "this story was delivered", and derived markers say "this story was
	// delivered under a different item key", so neither may be expanded into
	// content-signature markers: a skip marker would suppress a different,
	// allowed entry, and a derived marker would mint a second, self-renewing
	// anchor for a story whose own anchor already exists. Records written
	// before these flags existed read back as false and keep the previous
	// behaviour.
	if sentInfo.Skipped || sentInfo.Derived {
		return 0
	}

	item, exists := parsedByKey[primaryItemKey]
	if !exists {
		return 0
	}

	destinationID := strings.TrimSpace(sentInfo.DestinationID)
	if destinationID == "" {
		return 0
	}

	backfilled := 0
	for _, contentSignature := range storedRSSContentSignatures(item) {
		if contentSignature == "" || contentSignature == primaryItemKey {
			continue
		}
		compositeKey := makeCompositeKey(contentSignature, destinationID)
		if _, exists := status.SentItems[compositeKey]; exists {
			continue
		}

		aliasInfo := sentInfo
		aliasInfo.ItemKey = contentSignature
		aliasInfo.DestinationID = deliveryDestinationIDForPersistence(destinationID)
		if aliasInfo.Title == "" {
			aliasInfo.Title = item.Title
		}
		if aliasInfo.FeedTitle == "" {
			aliasInfo.FeedTitle = item.FeedTitle
		}
		status.SentItems[compositeKey] = aliasInfo
		backfilled++
	}

	return backfilled
}

func migrateLegacyRSSProcessedItems(status *rssStatus) int {
	if status == nil || len(status.Feeds) == 0 {
		return 0
	}
	if status.ParsedItems == nil {
		status.ParsedItems = make([]StoredRSSEntry, 0)
	}

	existing := make(map[string]struct{}, len(status.ParsedItems))
	for _, item := range status.ParsedItems {
		existing[parsedIndexKey(item.FeedURL, item.Key)] = struct{}{}
	}

	migrated := 0
	legacyFeedsCleared := 0
	for feedURL, feedInfo := range status.Feeds {
		if len(feedInfo.ProcessedItems) == 0 {
			continue
		}
		legacyFeedsCleared++
		for itemKey, title := range feedInfo.ProcessedItems {
			itemKey = strings.TrimSpace(itemKey)
			if itemKey == "" {
				continue
			}
			idxKey := parsedIndexKey(feedURL, itemKey)
			if _, exists := existing[idxKey]; exists {
				continue
			}

			status.ParsedItems = append(status.ParsedItems, StoredRSSEntry{
				Key:      itemKey,
				FeedURL:  feedURL,
				Title:    textutil.TruncateText(title, maxStoredRSSTitleRunes),
				ParsedAt: legacyRSSProcessedParsedAt(status.LastUpdated, feedInfo.LastCheck),
			})
			existing[idxKey] = struct{}{}
			migrated++
		}
		feedInfo.ProcessedItems = nil
		status.Feeds[feedURL] = feedInfo
	}
	return migrated + legacyFeedsCleared
}

func legacyRSSProcessedParsedAt(statusLastUpdated, feedLastCheck time.Time) string {
	if !feedLastCheck.IsZero() {
		return formatStatusTimestamp(feedLastCheck)
	}
	if !statusLastUpdated.IsZero() {
		return formatStatusTimestamp(statusLastUpdated)
	}
	return ""
}

func (t *Tracker) installRSSStatusLocked(loadedStatus *rssStatus, dirty bool) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.rssStatus = loadedStatus
	if dirty {
		t.rssDirty = true
	}
	t.rebuildParsedIndex()
	if len(t.rssStatus.ParsedItems) > 1 {
		t.parsedSortDirty = true
		t.parsedFullSortDirty = true
		t.parsedDirtyStart = 0
	}
}

func (t *Tracker) logRSSStatusLoaded(filePath string, parsedItemsDeduped int) {
	log.WithFields(log.Fields{
		"file":                 filePath,
		"last_updated":         t.rssStatus.LastUpdated,
		"feeds":                len(t.rssStatus.Feeds),
		"parsed_items":         len(t.rssStatus.ParsedItems),
		"deduped_parsed_items": parsedItemsDeduped,
		"sent_items":           len(t.rssStatus.SentItems),
	}).Info("RSS status loaded from file")
}

func cloneAPIStatus(status *apiStatus) *apiStatus {
	if status == nil {
		return nil
	}
	cloned := *status
	cloned.LastSuccess = cloneTimePtr(status.LastSuccess)
	cloned.LastError = cloneStringPtr(status.LastError)
	cloned.LastRetryable = cloneBoolPtr(status.LastRetryable)
	cloned.LastTimeout = cloneBoolPtr(status.LastTimeout)
	cloned.SentItems = make(map[string]webhookSentInfo, len(status.SentItems))
	for key, value := range status.SentItems {
		cloned.SentItems[key] = value
	}
	if len(status.FetchedItems) > 0 {
		cloned.FetchedItems = make([]json.RawMessage, 0, len(status.FetchedItems))
		for _, payload := range status.FetchedItems {
			cloned.FetchedItems = append(cloned.FetchedItems, cloneRawMessage(payload))
		}
	}
	return &cloned
}

func cloneRSSStatus(status *rssStatus) *rssStatus {
	if status == nil {
		return nil
	}
	cloned := *status
	cloned.Feeds = make(map[string]FeedInfo, len(status.Feeds))
	for key, value := range status.Feeds {
		cloned.Feeds[key] = cloneFeedInfo(value)
	}
	cloned.ParsedItems = make([]StoredRSSEntry, 0, len(status.ParsedItems))
	for _, item := range status.ParsedItems {
		cloned.ParsedItems = append(cloned.ParsedItems, cloneStoredRSSEntry(item))
	}
	cloned.SentItems = make(map[string]rssWebhookSentInfo, len(status.SentItems))
	for key, value := range status.SentItems {
		cloned.SentItems[key] = value
	}
	return &cloned
}

func cloneFeedInfo(info FeedInfo) FeedInfo {
	info.LastSuccess = cloneTimePtr(info.LastSuccess)
	info.LastError = cloneStringPtr(info.LastError)
	info.LastRetryable = cloneBoolPtr(info.LastRetryable)
	info.LastTimeout = cloneBoolPtr(info.LastTimeout)
	info.ProcessedItems = cloneStringMap(info.ProcessedItems)
	return info
}

func cloneStoredRSSEntry(item StoredRSSEntry) StoredRSSEntry {
	item.Categories = cloneStringSlice(item.Categories)
	return item
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func loadJSONStatus[T any](filePath, label string, target *T, init func(*T)) (bool, error) {
	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			log.WithFields(log.Fields{
				"file":  filePath,
				"label": label,
			}).Info("Status file does not exist, starting with empty state")
			return false, nil
		}
		return false, fmt.Errorf("failed to stat %s file %s: %w", label, filePath, err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return false, fmt.Errorf("failed to read %s file %s: %w", label, filePath, err)
	}

	if err := json.Unmarshal(data, target); err != nil {
		return false, fmt.Errorf("failed to unmarshal %s file %s: %w", label, filePath, err)
	}

	if init != nil {
		init(target)
	}
	return true, nil
}

func cleanupStatusTempFiles(dataDir string) error {
	for _, name := range []string{
		statusAPIFileName + statusTempFileSuffix,
		statusRSSFileName + statusTempFileSuffix,
		statusRetryFileName + statusTempFileSuffix,
	} {
		path := filepath.Join(dataDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove stale temp file %s: %w", path, err)
		}
	}
	return nil
}
