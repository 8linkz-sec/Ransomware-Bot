package status

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
)

// retryStatus holds the persistent retry queue and dead letter storage.
type retryStatus struct {
	LastUpdated     time.Time            `json:"last_updated"`
	RetryQueue      map[string]retryItem `json:"retry_queue"` // compositeKey â†’ item
	DeadLetterItems []deadLetterItem     `json:"dead_letter_items"`
}

type retryStore struct {
	status *retryStatus
	byType map[string]map[string]struct{}
	dirty  bool
}

func newRetryStore() *retryStore {
	return &retryStore{
		status: newRetryStatus(),
		byType: make(map[string]map[string]struct{}),
	}
}

// newRetryStatus creates a new empty retry status structure.
func newRetryStatus() *retryStatus {
	return &retryStatus{
		LastUpdated:     statusNow(),
		RetryQueue:      make(map[string]retryItem),
		DeadLetterItems: make([]deadLetterItem, 0),
	}
}

const RetryPayloadVersionRansomwareEntryV1 = "ransomware_entry_v1"

// Messenger identifies the delivery platform used for retry routing.
type Messenger string

const (
	MessengerDiscord         Messenger = "discord"
	MessengerSlack           Messenger = "slack"
	MessengerSlackCompatible Messenger = "slack_compatible"
)

func (m Messenger) String() string {
	return string(m)
}

// RetryItemType identifies the source type of a retry item.
type RetryItemType string

const (
	RetryItemTypeAPI RetryItemType = "api"
	RetryItemTypeRSS RetryItemType = "rss"
)

func (t RetryItemType) String() string {
	return string(t)
}

// retryItem represents a failed webhook send queued for retry.
//
// DestinationID is the stable persisted route. The current webhook URL is
// reconstructed from config before replay.
// Messenger and ItemType are persisted as strings. API retry items should
// include Payload so they can be replayed after the source entry leaves the
// ransomware.live recent window.
type retryItem struct {
	ItemKey        string          `json:"item_key"`
	DestinationID  string          `json:"destination_id,omitempty"`
	Messenger      string          `json:"messenger"`
	ItemType       string          `json:"item_type"`
	PayloadVersion string          `json:"payload_version,omitempty"`
	Title          string          `json:"title"`
	RetryCount     int             `json:"retry_count"`
	LastError      string          `json:"last_error"`
	ErrorCategory  string          `json:"error_category,omitempty"`
	StatusCode     int             `json:"status_code,omitempty"`
	Retryable      *bool           `json:"retryable,omitempty"`
	FirstFailed    string          `json:"first_failed"`
	LastRetried    string          `json:"last_retried"`
	Payload        json.RawMessage `json:"payload,omitempty"` // Serialized entry data for retry (API items only)
}

// RetryRecord is a read-only view of a queued retry item.
type RetryRecord struct {
	ItemKey        string
	DestinationID  string
	Messenger      string
	ItemType       string
	PayloadVersion string
	Title          string
	RetryCount     int
	LastError      string
	ErrorCategory  string
	StatusCode     int
	Retryable      *bool
	FirstFailed    string
	LastRetried    string
	Payload        []byte
}

type RetryErrorInfo struct {
	ErrorCategory string
	StatusCode    int
	Retryable     *bool
}

type SourceErrorInfo struct {
	ErrorCategory string
	StatusCode    int
	Retryable     *bool
	Timeout       *bool
}

// RetryRequest describes one retry queue create or update operation.
type RetryRequest struct {
	ItemKey       string
	DestinationID string
	Messenger     Messenger
	ItemType      RetryItemType
	Title         string
	LastError     string
	ErrorInfo     RetryErrorInfo
	// MaxAttempts is the retry budget after the first send; 0 means unlimited.
	MaxAttempts int
	RetryWindow time.Duration
	Payload     []byte
}

// QueuedRetryRecord carries the opaque durable queue key with the retry record.
type QueuedRetryRecord struct {
	QueueKey string
	Item     RetryRecord
}

// deadLetterItem is a send that permanently failed after exhausting all retries.
type deadLetterItem struct {
	ItemKey        string          `json:"item_key"`
	DestinationID  string          `json:"destination_id,omitempty"`
	ItemType       string          `json:"item_type"`
	Messenger      string          `json:"messenger"`
	Title          string          `json:"title"`
	RetryCount     int             `json:"retry_count"`
	LastError      string          `json:"last_error"`
	ErrorCategory  string          `json:"error_category,omitempty"`
	StatusCode     int             `json:"status_code,omitempty"`
	Retryable      *bool           `json:"retryable,omitempty"`
	TerminalReason string          `json:"terminal_reason,omitempty"`
	FirstFailed    string          `json:"first_failed"`
	DeadAt         string          `json:"dead_at"`
	PayloadVersion string          `json:"payload_version,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

// DeadLetterEntry is a read-only view of a terminal retry failure.
type DeadLetterEntry struct {
	ItemKey        string
	DestinationID  string
	ItemType       string
	Messenger      string
	Title          string
	RetryCount     int
	LastError      string
	ErrorCategory  string
	StatusCode     int
	Retryable      *bool
	TerminalReason string
	FirstFailed    string
	DeadAt         string
	PayloadVersion string
	Payload        []byte
}

const (
	TerminalReasonMaxAttempts        = "max_attempts_exhausted"
	TerminalReasonRetryWindow        = "retry_window_exhausted"
	TerminalReasonInvalidFirstFailed = "invalid_first_failed_timestamp"
	TerminalReasonExplicit           = "explicit_dead_letter"
	TerminalReasonTerminalFailure    = "terminal_failure"
	TerminalReasonRetentionPruned    = "retention_pruned"
	TerminalReasonRSSParsedPruned    = "rss_parsed_item_pruned"
)

func (t *Tracker) cleanupRetryStatusLocked(events *[]DeliveryAuditEvent) {
	t.cleanupRetryQueueLocked(events)
	t.cleanupDeadLettersLocked()
}

func (t *Tracker) cleanupRetryQueueLocked(events *[]DeliveryAuditEvent) {
	retention := t.retention
	now := statusNow()
	removedByAge := 0
	for queueKey, item := range t.retryStore.status.RetryQueue {
		itemTime := retryItemRetentionTime(item)
		if itemTime.IsZero() || now.Sub(itemTime) <= retention.RetryQueueMaxAge {
			continue
		}
		t.moveToDeadLetterLocked(events, queueKey, item, TerminalReasonRetentionPruned)
		removedByAge++
	}
	if removedByAge > 0 {
		log.WithField("removed", removedByAge).Info("Moved aged retry queue items to dead letter")
	}

	if len(t.retryStore.status.RetryQueue) <= retention.MaxRetryQueueItems {
		return
	}

	items := make([]queuedRetryWithTime, 0, len(t.retryStore.status.RetryQueue))
	for queueKey, item := range t.retryStore.status.RetryQueue {
		items = append(items, queuedRetryWithTime{
			queueKey: queueKey,
			item:     item,
			at:       retryItemRetentionTime(item),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return statusTimeBefore(items[i].at, items[j].at)
	})

	removeCount := len(t.retryStore.status.RetryQueue) - retention.MaxRetryQueueItems
	for i := 0; i < removeCount; i++ {
		t.moveToDeadLetterLocked(events, items[i].queueKey, items[i].item, TerminalReasonRetentionPruned)
	}

	log.WithFields(log.Fields{
		"removed": removeCount,
		"kept":    retention.MaxRetryQueueItems,
	}).Info("Moved excess retry queue items to dead letter")
}

type queuedRetryWithTime struct {
	queueKey string
	item     retryItem
	at       time.Time
}

func retryItemRetentionTime(item retryItem) time.Time {
	if parsed := parseStatusTimestamp(item.LastRetried); !parsed.IsZero() {
		return parsed
	}
	return parseStatusTimestamp(item.FirstFailed)
}

func (t *Tracker) cleanupDeadLettersLocked() {
	if len(t.retryStore.status.DeadLetterItems) == 0 {
		return
	}

	retention := t.retention
	now := statusNow()
	kept := make([]deadLetterItem, 0, len(t.retryStore.status.DeadLetterItems))
	removedByAge := 0
	for _, item := range t.retryStore.status.DeadLetterItems {
		deadAt := parseStatusTimestamp(item.DeadAt)
		if !deadAt.IsZero() && now.Sub(deadAt) > retention.DeadLetterMaxAge {
			removedByAge++
			continue
		}
		kept = append(kept, item)
	}
	t.retryStore.status.DeadLetterItems = kept

	removedByCount := 0
	if len(t.retryStore.status.DeadLetterItems) > retention.MaxDeadLetterItems {
		items := append([]deadLetterItem(nil), t.retryStore.status.DeadLetterItems...)
		sort.Slice(items, func(i, j int) bool {
			return statusTimeBefore(parseStatusTimestamp(items[i].DeadAt), parseStatusTimestamp(items[j].DeadAt))
		})
		removedByCount = len(items) - retention.MaxDeadLetterItems
		t.retryStore.status.DeadLetterItems = items[removedByCount:]
	}

	if removedByAge > 0 || removedByCount > 0 {
		t.retryStore.status.LastUpdated = now
		t.retryStore.dirty = true
		log.WithFields(log.Fields{
			"removed_by_age":   removedByAge,
			"removed_by_count": removedByCount,
			"kept":             len(t.retryStore.status.DeadLetterItems),
		}).Info("Pruned dead-letter retry status")
	}
}

// --- Retry Queue & Dead Letter Methods ---

// EnqueueRetry adds or updates a failed item in the retry queue. DestinationID
// may be a stable route such as "discord.ransomware" or a legacy raw webhook
// URL. Raw URLs are used only for the opaque queue key and are not persisted as
// destination_id.
func (t *Tracker) EnqueueRetry(req RetryRequest) bool {
	return t.EnqueueRetryForDestination(req)
}

// EnqueueRetryForDestination adds or updates a failed item in the retry queue
// for a stable destination.
//
// DestinationID should be a stable logical route such as "discord.ransomware"
// or "slack.rss.general", not a raw webhook URL. The current webhook URL is
// intentionally reconstructed by scheduler code from config during replay.
// API callers should pass a serialized RansomwareEntry payload in Payload so
// retry recovery can send the item after it leaves the upstream recent-results window.
func (t *Tracker) EnqueueRetryForDestination(req RetryRequest) bool {
	t.mutex.Lock()
	var events []DeliveryAuditEvent
	defer func() {
		t.auditFlight.reserve(len(events))
		t.mutex.Unlock()
		t.flushDeliveryAuditEvents(events)
	}()
	triggerTestAuditEntryPointPanicHook()

	req.LastError = textutil.RedactWebhookSecrets(req.LastError)

	compositeKey := makeCompositeKey(req.ItemKey, req.DestinationID)
	if t.isDeadLetteredForDestinationLocked(req.ItemKey, req.DestinationID, req.Messenger.String(), req.ItemType.String()) {
		return false
	}
	now := statusNow()
	nowStr := formatStatusTimestamp(now)

	existing, exists := t.retryStore.status.RetryQueue[compositeKey]
	if exists {
		if !t.updateExistingRetryLocked(&events, compositeKey, existing, req, now, nowStr) {
			return false
		}
	} else {
		item := newRetryItem(req, nowStr)
		t.retryStore.status.RetryQueue[compositeKey] = item
		t.indexRetryQueueItemLocked(compositeKey, item)
	}
	t.retryStore.status.LastUpdated = now
	t.retryStore.dirty = true
	queued := t.retryStore.status.RetryQueue[compositeKey]
	events = append(events, normalizeDeliveryAuditEvent(DeliveryAuditEvent{
		EventType:     DeliveryAuditEventDeliveryState,
		Source:        req.ItemType.String(),
		ItemType:      req.ItemType.String(),
		ItemKey:       req.ItemKey,
		Title:         req.Title,
		Messenger:     req.Messenger.String(),
		DestinationID: req.DestinationID,
		Outcome:       DeliveryAuditOutcomeRetryQueued,
		Reason:        DeliveryAuditReasonWebhookFailure,
		Details: map[string]string{
			"retry_count":    fmt.Sprint(queued.RetryCount),
			"error_category": queued.ErrorCategory,
			"status_code":    fmt.Sprint(queued.StatusCode),
		},
	}))
	return true
}

func (t *Tracker) updateExistingRetryLocked(
	events *[]DeliveryAuditEvent,
	compositeKey string,
	existing retryItem,
	req RetryRequest,
	now time.Time,
	nowStr string,
) bool {
	t.deindexRetryQueueItemLocked(compositeKey, existing)
	existing.RetryCount++
	if existing.DestinationID == "" {
		existing.DestinationID = retryDestinationIDForPersistence(req.DestinationID)
	}
	existing.LastError = req.LastError
	applyRetryErrorInfoToRetryItem(&existing, req.ErrorInfo)
	existing.LastRetried = nowStr
	if existing.PayloadVersion == "" && existing.ItemType == RetryItemTypeAPI.String() && len(existing.Payload) > 0 {
		existing.PayloadVersion = RetryPayloadVersionRansomwareEntryV1
	}

	if updated, terminalReason, shouldMove := retryDeadLetterDecision(
		existing,
		req.LastError,
		req.MaxAttempts,
		req.RetryWindow,
		now,
	); shouldMove {
		t.moveToDeadLetterLocked(events, compositeKey, updated, terminalReason)
		return false
	}

	t.retryStore.status.RetryQueue[compositeKey] = existing
	t.indexRetryQueueItemLocked(compositeKey, existing)
	return true
}

func newRetryItem(req RetryRequest, nowStr string) retryItem {
	item := retryItem{
		ItemKey:       req.ItemKey,
		DestinationID: retryDestinationIDForPersistence(req.DestinationID),
		Messenger:     req.Messenger.String(),
		ItemType:      req.ItemType.String(),
		Title:         req.Title,
		RetryCount:    1,
		LastError:     req.LastError,
		FirstFailed:   nowStr,
		LastRetried:   nowStr,
	}
	applyRetryErrorInfoToRetryItem(&item, req.ErrorInfo)
	if req.Payload != nil {
		item.Payload = cloneRawMessage(req.Payload)
		if req.ItemType == RetryItemTypeAPI {
			item.PayloadVersion = RetryPayloadVersionRansomwareEntryV1
		}
	}
	return item
}

func applyRetryErrorInfoToRetryItem(item *retryItem, info RetryErrorInfo) {
	item.ErrorCategory = info.ErrorCategory
	item.StatusCode = info.StatusCode
	item.Retryable = cloneBoolPtr(info.Retryable)
}

func applyRetryErrorInfoToDeadLetterItem(item *deadLetterItem, info RetryErrorInfo) {
	item.ErrorCategory = info.ErrorCategory
	item.StatusCode = info.StatusCode
	item.Retryable = cloneBoolPtr(info.Retryable)
}

func retryErrorInfoFromItem(item retryItem) RetryErrorInfo {
	return RetryErrorInfo{
		ErrorCategory: item.ErrorCategory,
		StatusCode:    item.StatusCode,
		Retryable:     cloneBoolPtr(item.Retryable),
	}
}

func (t *Tracker) retryIndexCurrentLocked() bool {
	indexed := 0
	for _, queueKeys := range t.retryStore.byType {
		indexed += len(queueKeys)
	}
	return indexed == len(t.retryStore.status.RetryQueue)
}

func (t *Tracker) ensureRetryIndexLocked() {
	if t.retryStore.byType == nil || !t.retryIndexCurrentLocked() {
		t.rebuildRetryIndexLocked()
	}
}

func (t *Tracker) rebuildRetryIndexLocked() {
	t.retryStore.byType = make(map[string]map[string]struct{})
	for queueKey, item := range t.retryStore.status.RetryQueue {
		t.indexRetryQueueItemLocked(queueKey, item)
	}
}

func (t *Tracker) indexRetryQueueItemLocked(queueKey string, item retryItem) {
	if queueKey == "" || item.ItemType == "" {
		return
	}
	if t.retryStore.byType == nil {
		t.retryStore.byType = make(map[string]map[string]struct{})
	}
	queueKeys := t.retryStore.byType[item.ItemType]
	if queueKeys == nil {
		queueKeys = make(map[string]struct{})
		t.retryStore.byType[item.ItemType] = queueKeys
	}
	queueKeys[queueKey] = struct{}{}
}

func (t *Tracker) deindexRetryQueueItemLocked(queueKey string, item retryItem) {
	if queueKey == "" || item.ItemType == "" || t.retryStore.byType == nil {
		return
	}
	queueKeys := t.retryStore.byType[item.ItemType]
	delete(queueKeys, queueKey)
	if len(queueKeys) == 0 {
		delete(t.retryStore.byType, item.ItemType)
	}
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// EnqueueDeferredRetry stores an item for later delivery without counting it as
// a failed send attempt. It returns true only when a new queue item was added.
func (t *Tracker) EnqueueDeferredRetry(
	itemKey, webhookURL, messenger, itemType, title, reason string,
	payload []byte,
) bool {
	return t.EnqueueDeferredRetryForDestination(itemKey, webhookURL, messenger, itemType, title, reason, payload)
}

// EnqueueDeferredRetryForDestination stores an item for later delivery without
// counting it as a failed send attempt for a stable destination.
func (t *Tracker) EnqueueDeferredRetryForDestination(
	itemKey, destinationID, messenger, itemType, title, reason string,
	payload []byte,
) bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	compositeKey := makeCompositeKey(itemKey, destinationID)
	if t.isDeadLetteredForDestinationLocked(itemKey, destinationID, messenger, itemType) {
		return false
	}
	if _, exists := t.retryStore.status.RetryQueue[compositeKey]; exists {
		return false
	}

	now := statusNow()
	nowStr := formatStatusTimestamp(now)
	item := retryItem{
		ItemKey:       itemKey,
		DestinationID: retryDestinationIDForPersistence(destinationID),
		Messenger:     messenger,
		ItemType:      itemType,
		Title:         title,
		RetryCount:    0,
		LastError:     textutil.RedactWebhookSecrets(reason),
		FirstFailed:   nowStr,
		LastRetried:   nowStr,
	}
	if payload != nil {
		item.Payload = cloneRawMessage(payload)
		if itemType == RetryItemTypeAPI.String() {
			item.PayloadVersion = RetryPayloadVersionRansomwareEntryV1
		}
	}

	t.retryStore.status.RetryQueue[compositeKey] = item
	t.indexRetryQueueItemLocked(compositeKey, item)
	t.retryStore.status.LastUpdated = now
	t.retryStore.dirty = true
	return true
}

func retryDestinationIDForPersistence(destinationID string) string {
	return deliveryDestinationIDForPersistence(destinationID)
}

func deliveryDestinationIDForPersistence(destinationID string) string {
	destinationID = strings.TrimSpace(destinationID)
	lowerDestinationID := strings.ToLower(destinationID)
	if strings.HasPrefix(lowerDestinationID, "http://") || strings.HasPrefix(lowerDestinationID, "https://") {
		return ""
	}
	return destinationID
}

// RemoveFromRetryQueue removes an item from the retry queue (after successful send).
func (t *Tracker) RemoveFromRetryQueue(itemKey, webhookURL string) {
	t.RemoveFromRetryQueueByDestination(itemKey, webhookURL)
}

// RemoveFromRetryQueueByDestination removes an item from the retry queue for a stable destination.
func (t *Tracker) RemoveFromRetryQueueByDestination(itemKey, destinationID string, legacyDestinations ...string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.removeRetryQueueEntryLocked(makeCompositeKey(itemKey, destinationID))
	for _, legacyDestination := range legacyDestinations {
		if legacyDestination == "" || legacyDestination == destinationID {
			continue
		}
		t.removeRetryQueueEntryLocked(makeCompositeKey(itemKey, legacyDestination))
	}
}

// RemoveRetryQueueEntry removes an item from the retry queue by its opaque queue key.
func (t *Tracker) RemoveRetryQueueEntry(queueKey string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.removeRetryQueueEntryLocked(queueKey)
}

func (t *Tracker) removeRetryQueueEntryLocked(queueKey string) {
	if item, exists := t.retryStore.status.RetryQueue[queueKey]; exists {
		t.deindexRetryQueueItemLocked(queueKey, item)
		delete(t.retryStore.status.RetryQueue, queueKey)
		t.retryStore.status.LastUpdated = statusNow()
		t.retryStore.dirty = true
	}
}

// RecordRetryQueueFailure updates an existing retry queue entry by opaque queue key.
func (t *Tracker) RecordRetryQueueFailure(queueKey, lastError string, maxAttempts int, retryWindow time.Duration) bool {
	return t.RecordRetryQueueFailureWithErrorInfo(queueKey, lastError, RetryErrorInfo{}, maxAttempts, retryWindow)
}

// RecordRetryQueueFailureWithErrorInfo updates an existing retry queue entry by opaque queue key.
func (t *Tracker) RecordRetryQueueFailureWithErrorInfo(
	queueKey, lastError string,
	errorInfo RetryErrorInfo,
	maxAttempts int,
	retryWindow time.Duration,
) bool {
	t.mutex.Lock()
	var events []DeliveryAuditEvent
	defer func() {
		t.auditFlight.reserve(len(events))
		t.mutex.Unlock()
		t.flushDeliveryAuditEvents(events)
	}()
	triggerTestAuditEntryPointPanicHook()

	item, exists := t.retryStore.status.RetryQueue[queueKey]
	if !exists {
		return false
	}

	now := statusNow()
	nowStr := formatStatusTimestamp(now)
	item.RetryCount++
	item.LastError = textutil.RedactWebhookSecrets(lastError)
	applyRetryErrorInfoToRetryItem(&item, errorInfo)
	item.LastRetried = nowStr
	if item.PayloadVersion == "" && item.ItemType == RetryItemTypeAPI.String() && len(item.Payload) > 0 {
		item.PayloadVersion = RetryPayloadVersionRansomwareEntryV1
	}

	if updated, terminalReason, shouldMove := retryDeadLetterDecision(
		item,
		item.LastError,
		maxAttempts,
		retryWindow,
		now,
	); shouldMove {
		t.moveToDeadLetterLocked(&events, queueKey, updated, terminalReason)
		return false
	} else {
		item = updated
	}

	t.retryStore.status.RetryQueue[queueKey] = item
	t.indexRetryQueueItemLocked(queueKey, item)
	t.retryStore.status.LastUpdated = now
	t.retryStore.dirty = true
	return true
}

// DeadLetterRetryQueueEntry moves an existing retry queue entry to dead letter by opaque queue key.
func (t *Tracker) DeadLetterRetryQueueEntry(queueKey, lastError string) bool {
	return t.DeadLetterRetryQueueEntryWithErrorInfo(queueKey, lastError, RetryErrorInfo{})
}

// DeadLetterRetryQueueEntryWithErrorInfo moves an existing retry queue entry to dead letter by opaque queue key.
func (t *Tracker) DeadLetterRetryQueueEntryWithErrorInfo(queueKey, lastError string, errorInfo RetryErrorInfo) bool {
	t.mutex.Lock()
	var events []DeliveryAuditEvent
	defer func() {
		t.auditFlight.reserve(len(events))
		t.mutex.Unlock()
		t.flushDeliveryAuditEvents(events)
	}()
	triggerTestAuditEntryPointPanicHook()

	item, exists := t.retryStore.status.RetryQueue[queueKey]
	if !exists {
		return false
	}
	item.LastError = textutil.RedactWebhookSecrets(lastError)
	applyRetryErrorInfoToRetryItem(&item, errorInfo)
	t.moveToDeadLetterLocked(&events, queueKey, item, TerminalReasonExplicit)
	return true
}

// MarkRetryDeadLetter records a terminal retry/recovery failure even when the
// item is not currently present in the retry queue.
func (t *Tracker) MarkRetryDeadLetter(itemKey, messenger, itemType, title, lastError string) {
	t.MarkRetryDeadLetterForDestination(itemKey, "", messenger, itemType, title, lastError)
}

// MarkRetryDeadLetterForDestination records a terminal retry/recovery failure
// for one delivery destination even when the item is not currently in the queue.
func (t *Tracker) MarkRetryDeadLetterForDestination(
	itemKey, destinationID, messenger, itemType, title, lastError string,
) {
	t.MarkRetryDeadLetterForDestinationWithErrorInfo(
		itemKey,
		destinationID,
		messenger,
		itemType,
		title,
		lastError,
		RetryErrorInfo{},
	)
}

// MarkRetryDeadLetterForDestinationWithErrorInfo records a terminal retry/recovery failure
// for one delivery destination with machine-readable error metadata.
func (t *Tracker) MarkRetryDeadLetterForDestinationWithErrorInfo(
	itemKey, destinationID, messenger, itemType, title, lastError string,
	errorInfo RetryErrorInfo,
) {
	t.mutex.Lock()
	var events []DeliveryAuditEvent
	defer func() {
		t.auditFlight.reserve(len(events))
		t.mutex.Unlock()
		t.flushDeliveryAuditEvents(events)
	}()
	triggerTestAuditEntryPointPanicHook()

	destinationID = retryDestinationIDForPersistence(destinationID)
	if destinationID == "" && t.isDeadLetteredLocked(itemKey, messenger, itemType) {
		t.removeQueuedRetryItemsForDeadLetterLocked(itemKey, destinationID, messenger, itemType)
		return
	}
	if destinationID != "" && t.isDeadLetteredForDestinationLocked(itemKey, destinationID, messenger, itemType) {
		t.removeQueuedRetryItemsForDeadLetterLocked(itemKey, destinationID, messenger, itemType)
		return
	}

	now := statusNow()
	nowStr := formatStatusTimestamp(now)
	deadLetter := deadLetterItem{
		ItemKey:        itemKey,
		DestinationID:  destinationID,
		ItemType:       itemType,
		Messenger:      messenger,
		Title:          title,
		RetryCount:     0,
		LastError:      textutil.RedactWebhookSecrets(lastError),
		TerminalReason: TerminalReasonTerminalFailure,
		FirstFailed:    nowStr,
		DeadAt:         nowStr,
	}
	if queued, found := t.queuedRetryItemForDeadLetterLocked(itemKey, destinationID, messenger, itemType); found {
		deadLetter = deadLetterItemFromRetryItem(queued, TerminalReasonTerminalFailure)
		deadLetter.DestinationID = destinationID // keep legacy blank-destination scope
		deadLetter.Title = title
		deadLetter.LastError = textutil.RedactWebhookSecrets(lastError)
		deadLetter.DeadAt = nowStr
	}
	applyRetryErrorInfoToDeadLetterItem(&deadLetter, errorInfo)
	t.retryStore.status.DeadLetterItems = append(t.retryStore.status.DeadLetterItems, deadLetter)
	t.removeQueuedRetryItemsForDeadLetterLocked(itemKey, destinationID, messenger, itemType)
	t.retryStore.status.LastUpdated = now
	t.retryStore.dirty = true
	events = append(events, normalizeDeliveryAuditEvent(DeliveryAuditEvent{
		EventType:     DeliveryAuditEventDeliveryState,
		Source:        itemType,
		ItemType:      itemType,
		ItemKey:       itemKey,
		Title:         title,
		Messenger:     messenger,
		DestinationID: destinationID,
		Outcome:       DeliveryAuditOutcomeDeadLettered,
		Reason:        TerminalReasonTerminalFailure,
		Details: map[string]string{
			"retry_count":    fmt.Sprint(deadLetter.RetryCount),
			"error_category": errorInfo.ErrorCategory,
			"status_code":    fmt.Sprint(errorInfo.StatusCode),
		},
	}))
}

func (t *Tracker) removeQueuedRetryItemsForDeadLetterLocked(itemKey, destinationID, messenger, itemType string) int {
	removed := 0
	for queueKey, item := range t.retryStore.status.RetryQueue {
		if item.ItemKey != itemKey || item.Messenger != messenger || item.ItemType != itemType {
			continue
		}
		itemDestinationID := retryDestinationIDForPersistence(item.DestinationID)
		if destinationID != "" && itemDestinationID != destinationID {
			continue
		}
		t.deindexRetryQueueItemLocked(queueKey, item)
		delete(t.retryStore.status.RetryQueue, queueKey)
		removed++
	}
	if removed > 0 {
		t.retryStore.status.LastUpdated = statusNow()
		t.retryStore.dirty = true
	}
	return removed
}

// GetRetryItemsByType returns all items in the retry queue for a given item type,
// regardless of webhook URL (which is not persisted for security).
func (t *Tracker) GetRetryItemsByType(itemType string) []RetryRecord {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.ensureRetryIndexLocked()
	queueKeys := t.retryStore.byType[itemType]
	items := make([]RetryRecord, 0, len(queueKeys))
	for queueKey := range queueKeys {
		if item, exists := t.retryStore.status.RetryQueue[queueKey]; exists {
			items = append(items, retryRecordFromItem(item))
		}
	}
	return items
}

// GetQueuedRetryItemsByType returns retry items with their opaque queue keys.
func (t *Tracker) GetQueuedRetryItemsByType(itemType string) []QueuedRetryRecord {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.ensureRetryIndexLocked()
	queueKeys := t.retryStore.byType[itemType]
	items := make([]QueuedRetryRecord, 0, len(queueKeys))
	for queueKey := range queueKeys {
		if item, exists := t.retryStore.status.RetryQueue[queueKey]; exists {
			items = append(items, QueuedRetryRecord{QueueKey: queueKey, Item: retryRecordFromItem(item)})
		}
	}
	return items
}

func (t *Tracker) setQueuedRetryTimes(itemKey, itemType, firstFailed, lastRetried string) bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.ensureRetryIndexLocked()
	queueKeys := t.retryStore.byType[itemType]
	for queueKey := range queueKeys {
		item, exists := t.retryStore.status.RetryQueue[queueKey]
		if !exists || item.ItemKey != itemKey {
			continue
		}
		item.FirstFailed = firstFailed
		item.LastRetried = lastRetried
		t.retryStore.status.RetryQueue[queueKey] = item
		t.retryStore.status.LastUpdated = statusNow()
		t.retryStore.dirty = true
		return true
	}
	return false
}

func (t *Tracker) setDeadLetterTimes(times map[string]string) int {
	if len(times) == 0 {
		return 0
	}

	t.mutex.Lock()
	defer t.mutex.Unlock()

	updated := 0
	for i, item := range t.retryStore.status.DeadLetterItems {
		deadAt, ok := times[item.ItemKey]
		if !ok {
			continue
		}
		t.retryStore.status.DeadLetterItems[i].DeadAt = deadAt
		updated++
	}
	if updated > 0 {
		t.retryStore.status.LastUpdated = statusNow()
		t.retryStore.dirty = true
	}
	return updated
}

func retryRecordFromItem(item retryItem) RetryRecord {
	return RetryRecord{
		ItemKey:        item.ItemKey,
		DestinationID:  item.DestinationID,
		Messenger:      item.Messenger,
		ItemType:       item.ItemType,
		PayloadVersion: item.PayloadVersion,
		Title:          item.Title,
		RetryCount:     item.RetryCount,
		LastError:      item.LastError,
		ErrorCategory:  item.ErrorCategory,
		StatusCode:     item.StatusCode,
		Retryable:      cloneBoolPtr(item.Retryable),
		FirstFailed:    item.FirstFailed,
		LastRetried:    item.LastRetried,
		Payload:        cloneBytes(item.Payload),
	}
}

func cloneRetryItem(item retryItem) retryItem {
	item.Payload = cloneRawMessage(item.Payload)
	item.Retryable = cloneBoolPtr(item.Retryable)
	return item
}

func cloneRetryStatus(status *retryStatus) *retryStatus {
	if status == nil {
		return nil
	}
	cloned := *status
	cloned.RetryQueue = make(map[string]retryItem, len(status.RetryQueue))
	for key, value := range status.RetryQueue {
		cloned.RetryQueue[key] = cloneRetryItem(value)
	}
	cloned.DeadLetterItems = make([]deadLetterItem, 0, len(status.DeadLetterItems))
	for _, item := range status.DeadLetterItems {
		cloned.DeadLetterItems = append(cloned.DeadLetterItems, cloneDeadLetterItem(item))
	}
	return &cloned
}

// GetDeadLetterItems returns a cloned snapshot of terminal retry failures.
func (t *Tracker) GetDeadLetterItems() []DeadLetterEntry {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	items := make([]DeadLetterEntry, 0, len(t.retryStore.status.DeadLetterItems))
	for _, item := range t.retryStore.status.DeadLetterItems {
		items = append(items, deadLetterEntryFromItem(item))
	}
	return items
}

// RetryStatusSnapshot returns retry-store timestamps and counts without
// exposing the persistent queue or dead-letter slices.
func (t *Tracker) RetryStatusSnapshot() RetryStatusSnapshot {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	return RetryStatusSnapshot{
		LastUpdated:     t.retryStore.status.LastUpdated,
		RetryQueueItems: len(t.retryStore.status.RetryQueue),
		DeadLetterItems: len(t.retryStore.status.DeadLetterItems),
	}
}

func deadLetterEntryFromItem(item deadLetterItem) DeadLetterEntry {
	return DeadLetterEntry{
		ItemKey:        item.ItemKey,
		DestinationID:  item.DestinationID,
		ItemType:       item.ItemType,
		Messenger:      item.Messenger,
		Title:          item.Title,
		RetryCount:     item.RetryCount,
		LastError:      item.LastError,
		ErrorCategory:  item.ErrorCategory,
		StatusCode:     item.StatusCode,
		Retryable:      cloneBoolPtr(item.Retryable),
		TerminalReason: item.TerminalReason,
		FirstFailed:    item.FirstFailed,
		DeadAt:         item.DeadAt,
		PayloadVersion: item.PayloadVersion,
		Payload:        cloneBytes(item.Payload),
	}
}

func cloneDeadLetterItem(item deadLetterItem) deadLetterItem {
	item.Payload = cloneRawMessage(item.Payload)
	item.Retryable = cloneBoolPtr(item.Retryable)
	return item
}

func cloneRawMessage(payload []byte) json.RawMessage {
	return json.RawMessage(cloneBytes(payload))
}

func cloneBytes(payload []byte) []byte {
	if payload == nil {
		return nil
	}
	return append([]byte(nil), payload...)
}

// IsRetryDeadLettered reports whether retry processing has reached a terminal
// dead-letter state for an item/messenger/type tuple.
func (t *Tracker) IsRetryDeadLettered(itemKey, messenger, itemType string) bool {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	return t.isDeadLetteredLocked(itemKey, messenger, itemType)
}

func (t *Tracker) isDeadLetteredLocked(itemKey, messenger, itemType string) bool {
	for _, item := range t.retryStore.status.DeadLetterItems {
		if item.ItemKey == itemKey && item.Messenger == messenger && item.ItemType == itemType {
			return true
		}
	}
	return false
}

// IsRetryDeadLetteredForDestination reports whether retry processing reached a
// terminal state for one item, messenger, item type, and delivery destination.
func (t *Tracker) IsRetryDeadLetteredForDestination(
	itemKey, destinationID, messenger, itemType string,
	legacyDestinations ...string,
) bool {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	return t.isDeadLetteredForDestinationLocked(itemKey, destinationID, messenger, itemType, legacyDestinations...)
}

func (t *Tracker) isDeadLetteredForDestinationLocked(
	itemKey, destinationID, messenger, itemType string,
	legacyDestinations ...string,
) bool {
	destinationIDs := map[string]struct{}{
		retryDestinationIDForPersistence(destinationID): {},
	}
	for _, legacyDestination := range legacyDestinations {
		destinationIDs[retryDestinationIDForPersistence(legacyDestination)] = struct{}{}
	}

	for _, item := range t.retryStore.status.DeadLetterItems {
		if item.ItemKey != itemKey || item.Messenger != messenger || item.ItemType != itemType {
			continue
		}
		itemDestinationID := retryDestinationIDForPersistence(item.DestinationID)
		if itemDestinationID == "" {
			return true
		}
		if _, ok := destinationIDs[itemDestinationID]; ok {
			return true
		}
	}
	return false
}

func deadLetterKey(itemKey, destinationID, messenger, itemType string) string {
	return itemKey + "\x00" + destinationID + "\x00" + messenger + "\x00" + itemType
}

// deadLetterItemFromRetryItem builds a terminal record from a queued retry row,
// carrying its attempt history and replay payload.
func deadLetterItemFromRetryItem(item retryItem, terminalReason string) deadLetterItem {
	deadLetter := deadLetterItem{
		ItemKey:        item.ItemKey,
		DestinationID:  retryDestinationIDForPersistence(item.DestinationID),
		ItemType:       item.ItemType,
		Messenger:      item.Messenger,
		Title:          item.Title,
		RetryCount:     item.RetryCount,
		LastError:      textutil.RedactWebhookSecrets(item.LastError),
		TerminalReason: terminalReason,
		FirstFailed:    item.FirstFailed,
		DeadAt:         formatStatusTimestamp(statusNow()),
		PayloadVersion: item.PayloadVersion,
		Payload:        cloneRawMessage(item.Payload),
	}
	applyRetryErrorInfoToDeadLetterItem(&deadLetter, retryErrorInfoFromItem(item))
	return deadLetter
}

// queuedRetryItemForDeadLetterLocked returns the queued row whose history a new dead letter inherits.
// A legacy blank destinationID matches several rows, so the choice is pinned to the furthest-attempted
// row and, on a tie, to the smallest queue key.
// NOTE: Must be called with t.mutex locked.
func (t *Tracker) queuedRetryItemForDeadLetterLocked(itemKey, destinationID, messenger, itemType string) (retryItem, bool) {
	var (
		best     retryItem
		bestKey  string
		hasMatch bool
	)
	for queueKey, item := range t.retryStore.status.RetryQueue {
		if item.ItemKey != itemKey || item.Messenger != messenger || item.ItemType != itemType {
			continue
		}
		if destinationID != "" && retryDestinationIDForPersistence(item.DestinationID) != destinationID {
			continue
		}
		if hasMatch && (item.RetryCount < best.RetryCount ||
			(item.RetryCount == best.RetryCount && queueKey >= bestKey)) {
			continue
		}
		best, bestKey, hasMatch = item, queueKey, true
	}
	return best, hasMatch
}

// moveToDeadLetterLocked moves an item from retry queue to dead letter.
// NOTE: Must be called with t.mutex locked.
func (t *Tracker) moveToDeadLetterLocked(events *[]DeliveryAuditEvent, compositeKey string, item retryItem, terminalReason string) {
	lastError := textutil.RedactWebhookSecrets(item.LastError)
	recordedDeadLetter := false
	if !t.isDeadLetteredForDestinationLocked(item.ItemKey, item.DestinationID, item.Messenger, item.ItemType) {
		deadLetter := deadLetterItemFromRetryItem(item, terminalReason)
		t.retryStore.status.DeadLetterItems = append(t.retryStore.status.DeadLetterItems, deadLetter)
		recordedDeadLetter = true
	}
	t.deindexRetryQueueItemLocked(compositeKey, item)
	delete(t.retryStore.status.RetryQueue, compositeKey)
	t.retryStore.status.LastUpdated = statusNow()
	t.retryStore.dirty = true
	if recordedDeadLetter {
		*events = append(*events, normalizeDeliveryAuditEvent(DeliveryAuditEvent{
			EventType:     DeliveryAuditEventDeliveryState,
			Source:        item.ItemType,
			ItemType:      item.ItemType,
			ItemKey:       item.ItemKey,
			Title:         item.Title,
			Messenger:     item.Messenger,
			DestinationID: retryDestinationIDForPersistence(item.DestinationID),
			Outcome:       DeliveryAuditOutcomeDeadLettered,
			Reason:        terminalReason,
			Details: map[string]string{
				"retry_count":    fmt.Sprint(item.RetryCount),
				"error_category": item.ErrorCategory,
				"status_code":    fmt.Sprint(item.StatusCode),
			},
		}))
	}

	log.WithFields(log.Fields{
		"item_key":        item.ItemKey,
		"destination_id":  retryDestinationIDForPersistence(item.DestinationID),
		"title":           item.Title,
		"item_type":       item.ItemType,
		"messenger":       item.Messenger,
		"retry_count":     item.RetryCount,
		"terminal_reason": terminalReason,
		"last_error":      lastError,
	}).Warn("Item moved to dead letter " + deadLetterWarnReasonPhrase(terminalReason))
}

// deadLetterWarnReasonPhrase names the actual cause behind a dead-lettering in
// the moveToDeadLetterLocked WARN, instead of always claiming exhausted
// retries: a quiet-hours-deferred item can dead-letter on its very first real
// send once its retry window elapses (TerminalReasonRetryWindow), and
// retention/RSS-pruning paths never attempted a send at all. Log/diagnostic
// text only.
//
// The default case is deliberately neutral, not a repeat of the
// TerminalReasonMaxAttempts wording: every TerminalReason* constant this
// package defines has its own case above, so default is reached only for a
// value this function does not yet recognize -- returning "after exhausting
// retries" there would silently reintroduce exactly the misreporting this fix
// removed the moment a future terminal reason is added without a matching
// case here.
func deadLetterWarnReasonPhrase(terminalReason string) string {
	switch terminalReason {
	case TerminalReasonMaxAttempts:
		return "after exhausting retries"
	case TerminalReasonRetryWindow:
		return "after its retry window elapsed"
	case TerminalReasonInvalidFirstFailed:
		return "due to an invalid first_failed timestamp"
	case TerminalReasonExplicit:
		return "on an explicit dead-letter request"
	case TerminalReasonRetentionPruned:
		return "by retention pruning"
	case TerminalReasonRSSParsedPruned:
		return "because its parsed RSS item was pruned"
	case TerminalReasonTerminalFailure:
		return "after a non-retryable failure"
	default:
		return "for an unrecognized terminal reason"
	}
}

// --- Retry Persistence ---

// loadRetryStatus loads retry status from JSON file if it exists.
func (t *Tracker) loadRetryStatus() error {
	filePath := filepath.Join(t.dataDir, statusRetryFileName)

	var loadedStatus retryStatus
	loaded, err := loadJSONStatus(filePath, "retry status", &loadedStatus, func(status *retryStatus) {
		if status.RetryQueue == nil {
			status.RetryQueue = make(map[string]retryItem)
		}
		if status.DeadLetterItems == nil {
			status.DeadLetterItems = make([]deadLetterItem, 0)
		}
	})
	if err != nil || !loaded {
		return err
	}

	sanitized := false
	for key, item := range loadedStatus.RetryQueue {
		changedItem := false
		redacted := textutil.RedactWebhookSecrets(item.LastError)
		if redacted != item.LastError {
			item.LastError = redacted
			sanitized = true
			changedItem = true
		}
		if item.PayloadVersion == "" && item.ItemType == RetryItemTypeAPI.String() && len(item.Payload) > 0 {
			item.PayloadVersion = RetryPayloadVersionRansomwareEntryV1
			sanitized = true
			changedItem = true
		}
		if changedItem {
			loadedStatus.RetryQueue[key] = item
		}
	}
	seenDeadLetters := make(map[string]bool, len(loadedStatus.DeadLetterItems))
	dedupedDeadLetters := loadedStatus.DeadLetterItems[:0]
	for _, item := range loadedStatus.DeadLetterItems {
		redacted := textutil.RedactWebhookSecrets(item.LastError)
		if redacted != item.LastError {
			item.LastError = redacted
			sanitized = true
		}
		key := deadLetterKey(
			item.ItemKey,
			retryDestinationIDForPersistence(item.DestinationID),
			item.Messenger,
			item.ItemType,
		)
		if seenDeadLetters[key] {
			sanitized = true
			continue
		}
		seenDeadLetters[key] = true
		dedupedDeadLetters = append(dedupedDeadLetters, item)
	}
	loadedStatus.DeadLetterItems = dedupedDeadLetters

	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.retryStore.status = &loadedStatus
	if removed := t.removeQueuedDeadLetteredRetryItemsLocked(); removed > 0 {
		sanitized = true
	}
	t.cleanupDeadLettersLocked()
	t.rebuildRetryIndexLocked()
	if sanitized {
		t.retryStore.dirty = true
	}

	log.WithFields(log.Fields{
		"file":              filePath,
		"retry_queue_size":  len(t.retryStore.status.RetryQueue),
		"dead_letter_count": len(t.retryStore.status.DeadLetterItems),
	}).Info("Retry status loaded from file")

	return nil
}

func (t *Tracker) removeQueuedDeadLetteredRetryItemsLocked() int {
	removed := 0
	for queueKey, item := range t.retryStore.status.RetryQueue {
		if !t.isDeadLetteredForDestinationLocked(item.ItemKey, item.DestinationID, item.Messenger, item.ItemType) {
			continue
		}
		t.deindexRetryQueueItemLocked(queueKey, item)
		delete(t.retryStore.status.RetryQueue, queueKey)
		removed++
	}
	if removed > 0 {
		t.retryStore.status.LastUpdated = statusNow()
		t.retryStore.dirty = true
		log.WithField("removed", removed).Info("Removed retry queue items already present in dead letter")
	}
	return removed
}

// saveRetryStatus saves retry status to JSON file.
func (t *Tracker) saveRetryStatus() error {
	return t.saveRetryStatusSnapshot(t.retryStore.status)
}

func (t *Tracker) saveRetryStatusSnapshot(snapshot *retryStatus) error {
	filePath := filepath.Join(t.dataDir, statusRetryFileName)

	if err := writeAtomicJSONFileFunc(filePath, "retry status", snapshot); err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"file":              filePath,
		"retry_queue_size":  len(snapshot.RetryQueue),
		"dead_letter_count": len(snapshot.DeadLetterItems),
	}).Debug("Retry status saved to file")
	return nil
}
