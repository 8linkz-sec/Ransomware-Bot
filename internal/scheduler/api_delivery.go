package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filter"
	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/quiethours"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"
)

func (s *Scheduler) deliverAPIEntriesAndProcessRetries(
	ctx context.Context,
	cfg *config.Config,
	allEntries []model.RansomwareEntry,
	checkTimeout time.Duration,
) {
	for _, target := range s.apiDeliveryTargetsForConfig(cfg) {
		s.processAPIDeliveryTarget(ctx, cfg, allEntries, target)
	}

	if !s.dryRun {
		retryCtx, retryCancel := context.WithTimeout(ctx, checkTimeout)
		s.processAPIRetryQueue(retryCtx, allEntries)
		retryCancel()
	}
}

func (s *Scheduler) processAPIDeliveryTarget(
	ctx context.Context,
	cfg *config.Config,
	allEntries []model.RansomwareEntry,
	target apiDeliveryTarget,
) {
	unsent, filteredCount := s.pendingAPIEntriesForTarget(allEntries, target)
	if target.quietHours != nil && target.quietHours.IsActive() {
		deferredCount := s.queuePendingAPIEntriesForTarget(unsent, target, "deferred by quiet hours")
		for _, entry := range unsent {
			s.recordAPIDeliveryAuditEvent(
				entry,
				target,
				status.DeliveryAuditOutcomeQuietHours,
				status.DeliveryAuditReasonQuietHours,
				map[string]string{
					"quiet_hours_start": target.quietHours.Start,
					"quiet_hours_end":   target.quietHours.End,
				},
			)
		}
		log.WithFields(log.Fields{
			"destination_id": target.destinationID,
			"messenger":      target.messenger,
			"item_type":      status.RetryItemTypeAPI.String(),
			"start":          target.quietHours.Start,
			"end":            target.quietHours.End,
			"unsent_count":   len(unsent),
			"filtered_count": filteredCount,
			"deferred_count": deferredCount,
		}).Info("Quiet hours active, skipping API send")
		return
	}

	pendingCount := s.queuePendingAPIEntriesForTarget(unsent, target, "pending API delivery")

	log.WithFields(log.Fields{
		"destination_enabled":   true,
		"destination_id":        target.destinationID,
		"messenger":             target.messenger,
		"item_type":             status.RetryItemTypeAPI.String(),
		"unsent_count":          len(unsent),
		"matching_unsent_count": len(unsent),
		"filtered_count":        filteredCount,
		"total_fetched":         len(allEntries),
		"pending_queued":        pendingCount,
	}).Debug("Checking API unsent items")

	if len(unsent) == 0 {
		fields := log.Fields{
			"destination_id":        apiTargetLogID(target),
			"messenger":             target.messenger,
			"item_type":             status.RetryItemTypeAPI.String(),
			"total_fetched":         len(allEntries),
			"unsent_count":          0,
			"matching_unsent_count": 0,
			"filtered_count":        filteredCount,
		}
		if s.dryRun {
			fields["mode"] = dryRunModeLabel
			fields["preview_count"] = 0
			fields["hint"] = "No matching ransomware entries; check sent status, webhook filters, and API data."
			log.WithFields(fields).Info("[DRY-RUN] No API entries to preview")
		} else {
			log.WithFields(fields).Debug("No unsent API items found")
		}
		return
	}

	sortAPIEntriesOldestFirst(unsent)
	sendBatch, cappedCount := limitAPIEntriesForCycle(unsent, cfg.APIMaxEntriesPerCycle)
	log.WithFields(log.Fields{
		"total_entries":         len(allEntries),
		"destination_id":        target.destinationID,
		"messenger":             target.messenger,
		"item_type":             status.RetryItemTypeAPI.String(),
		"unsent_count":          len(unsent),
		"matching_unsent_count": len(unsent),
		"cycle_limit":           cfg.APIMaxEntriesPerCycle,
		"deferred_by_cap":       cappedCount,
		"pending_queued":        pendingCount,
		"send_batch_count":      len(sendBatch),
	}).Info("Found unsent API entries")

	sendCtx, sendCancel := context.WithTimeout(ctx, apiCheckTimeout(cfg))
	s.sendAPIEntriesIndividually(sendCtx, sendBatch, target)
	sendCancel()
}

func (s *Scheduler) recordAPIDeliveryAuditEvent(
	entry model.RansomwareEntry,
	target apiDeliveryTarget,
	outcome string,
	reason string,
	details map[string]string,
) {
	s.recordDeliveryAuditEvent(status.DeliveryAuditEvent{
		EventType:     status.DeliveryAuditEventAlertCandidate,
		Source:        status.RetryItemTypeAPI.String(),
		ItemType:      status.RetryItemTypeAPI.String(),
		ItemKey:       model.GenerateRansomwareEntryKey(entry),
		Title:         model.DisplayRansomwareTitle(entry),
		Messenger:     target.messenger,
		DestinationID: target.destinationID,
		FeedType:      config.FeedTypeRansomware,
		Outcome:       outcome,
		Reason:        reason,
		Details:       details,
	})
}

func (s *Scheduler) recordAPIRetryAuditEvent(
	item status.RetryRecord,
	destinationID string,
	outcome string,
	reason string,
	details map[string]string,
) {
	s.recordDeliveryAuditEvent(status.DeliveryAuditEvent{
		EventType:     status.DeliveryAuditEventAlertCandidate,
		Source:        "api_retry",
		ItemType:      item.ItemType,
		ItemKey:       item.ItemKey,
		Title:         item.Title,
		Messenger:     item.Messenger,
		DestinationID: destinationID,
		FeedType:      config.FeedTypeRansomware,
		Outcome:       outcome,
		Reason:        reason,
		Details:       details,
	})
}

type apiDeliveryTarget struct {
	url           string
	destinationID string
	messenger     string
	logName       string
	filters       *filter.Rules
	quietHours    *quiethours.Policy
	delay         time.Duration
	send          func(context.Context, string, model.RansomwareEntry, *notifyfmt.FormatOptions) error
}

func apiTargetLogID(target apiDeliveryTarget) string {
	if target.destinationID != "" {
		return target.destinationID
	}
	return target.url
}

func (s *Scheduler) apiDeliveryTargetsForConfig(cfg *config.Config) []apiDeliveryTarget {
	targetConfigs := config.WebhookTargetsForType(cfg, config.WebhookTypeRansomware)
	targets := make([]apiDeliveryTarget, 0, len(targetConfigs))
	for _, targetConfig := range targetConfigs {
		if !targetConfig.Webhook.Enabled {
			continue
		}
		target, ok := s.apiDeliveryTargetForWebhookConfig(cfg, targetConfig)
		if ok {
			targets = append(targets, target)
		}
	}
	return targets
}

func (s *Scheduler) apiDeliveryTargetForWebhookConfig(
	cfg *config.Config,
	targetConfig config.WebhookTargetConfig,
) (apiDeliveryTarget, bool) {
	messenger, ok := messengerForWebhookPlatform(targetConfig.Platform)
	if !ok {
		return apiDeliveryTarget{}, false
	}
	delivery, ok := s.messengerDeliveryForConfig(messenger.String(), cfg)
	if !ok {
		return apiDeliveryTarget{}, false
	}

	target := apiDeliveryTarget{
		url:           targetConfig.Webhook.URL,
		destinationID: apiDestinationIDForTarget(messenger, targetConfig.DestinationSuffix),
		messenger:     messenger.String(),
		logName:       delivery.logName,
		filters:       config.FilterRules(targetConfig.Webhook.Filters),
		quietHours:    config.QuietHoursPolicy(targetConfig.Webhook.QuietHours),
		delay:         delivery.delay,
	}
	if delivery.sender != nil {
		target.send = delivery.sender.SendRansomwareEntry
	}

	return target, true
}

func (s *Scheduler) pendingAPIEntriesForTarget(
	allEntries []model.RansomwareEntry,
	target apiDeliveryTarget,
) ([]model.RansomwareEntry, int) {
	var unsent []model.RansomwareEntry
	filteredCount := 0

	for _, entry := range allEntries {
		if s.apiEntrySentToWebhook(entry, target.url, target.destinationID) {
			s.recordAPIDeliveryAuditEvent(
				entry,
				target,
				status.DeliveryAuditOutcomeDeduplicated,
				status.DeliveryAuditReasonAlreadySent,
				nil,
			)
			continue
		}
		if s.apiEntryDeadLettered(entry, target.destinationID, target.url, target.messenger) {
			s.recordAPIDeliveryAuditEvent(
				entry,
				target,
				status.DeliveryAuditOutcomeDeadLettered,
				status.DeliveryAuditReasonAlreadyDeadLettered,
				nil,
			)
			continue
		}
		if !filter.MatchesAPIEntry(target.filters, entry) {
			filteredCount++
			s.recordAPIDeliveryAuditEvent(
				entry,
				target,
				status.DeliveryAuditOutcomeFiltered,
				status.DeliveryAuditReasonFilterMismatch,
				nil,
			)
			log.WithFields(log.Fields{
				"group":          entry.Group,
				"country":        entry.Country,
				"entry_id":       entry.ID,
				"destination_id": target.destinationID,
				"messenger":      target.messenger,
				"item_type":      status.RetryItemTypeAPI.String(),
			}).Debug("API entry filtered out by webhook rules")
			continue
		}
		unsent = append(unsent, entry)
	}

	return unsent, filteredCount
}

func (s *Scheduler) pendingAPIEntriesForWebhook(
	allEntries []model.RansomwareEntry,
	webhookURL, messenger, destinationID string,
	filters *filter.Rules,
) ([]model.RansomwareEntry, int) {
	return s.pendingAPIEntriesForTarget(allEntries, apiDeliveryTarget{
		url:           webhookURL,
		destinationID: destinationID,
		messenger:     messenger,
		filters:       filters,
	})
}

func (s *Scheduler) migrateLegacyAPIFetchedItemsForConfig(cfg *config.Config) {
	payloads := s.statusTracker.LegacyAPIFetchedItemPayloads()
	if len(payloads) == 0 {
		return
	}

	entries := make([]model.RansomwareEntry, 0, len(payloads))
	malformedCount := 0
	for _, payload := range payloads {
		var entry model.RansomwareEntry
		if err := json.Unmarshal(payload, &entry); err != nil {
			malformedCount++
			log.WithError(err).Warn("Skipping malformed legacy API fetched item during migration")
			continue
		}
		entries = append(entries, entry)
	}

	migratedCount := 0
	for _, target := range s.apiDeliveryTargetsForConfig(cfg) {
		pending, _ := s.pendingAPIEntriesForTarget(entries, target)
		migratedCount += s.queuePendingAPIEntriesForTarget(pending, target, "migrated legacy API fetched item")
	}

	s.statusTracker.ClearLegacyAPIFetchedItems()
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		log.WithError(err).Warn("Failed to persist API legacy fetched item migration")
	}
	log.WithFields(log.Fields{
		"legacy_fetched_items": len(payloads),
		"decoded_items":        len(entries),
		"malformed_items":      malformedCount,
		"migrated_items":       migratedCount,
	}).Info("Migrated legacy API fetched items to pending retry queue")
}

func (s *Scheduler) apiEntrySentToWebhook(entry model.RansomwareEntry, webhookURL, destinationID string) bool {
	for _, key := range model.GenerateRansomwareEntryLookupKeys(entry) {
		if s.statusTracker.IsAPIItemSentToDestination(key, destinationID, webhookURL) {
			return true
		}
	}
	return false
}

func (s *Scheduler) apiEntryDeadLettered(entry model.RansomwareEntry, destinationID, webhookURL, messenger string) bool {
	for _, key := range model.GenerateRansomwareEntryLookupKeys(entry) {
		if s.statusTracker.IsRetryDeadLetteredForDestination(key, destinationID, messenger, status.RetryItemTypeAPI.String(), webhookURL) {
			return true
		}
	}
	return false
}

func sortAPIEntriesOldestFirst(entries []model.RansomwareEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Discovered.Before(entries[j].Discovered)
	})
}

func limitAPIEntriesForCycle(entries []model.RansomwareEntry, limit int) ([]model.RansomwareEntry, int) {
	if limit <= 0 || len(entries) <= limit {
		return entries, 0
	}
	return entries[:limit], len(entries) - limit
}

// sortAPIRetryRecordsOldestFirst orders queued API retry records by FirstFailed
// ascending, mirroring sortAPIEntriesOldestFirst. Used both as the within-
// destination order inside roundRobinAPIRetryRecords and, degenerate case,
// as the whole ordering when there is only one destination. A record with a
// missing or unparseable FirstFailed parses to the zero time.Time and sorts
// first, exactly like sortAPIEntriesOldestFirst's treatment of a zero
// Discovered time.
func sortAPIRetryRecordsOldestFirst(records []status.QueuedRetryRecord) {
	sort.Slice(records, func(i, j int) bool {
		return timeutil.ParseFlexibleTimestampOrZero(records[i].Item.FirstFailed).
			Before(timeutil.ParseFlexibleTimestampOrZero(records[j].Item.FirstFailed))
	})
}

// roundRobinAPIRetryRecords orders queued API retry records for the per-cycle
// attemptLimit cap so one chronically-failing destination's backlog cannot
// consume the whole cap every cycle (operator decision: round-robin, not
// plain oldest-first).
//
// "Destination" here is the SAME identifier the rest of processAPIRetryQueue
// routes and audits on: the resolved destinationID returned by
// apiRetryRoute(item, legacyMessengerDestination) -- not the raw
// Item.DestinationID struct field, which is empty for legacy rows before
// destination IDs existed. apiRetryRoute already resolves that legacy
// fallback (through legacyMessengerDestination, keyed by Item.Messenger),
// so grouping on its result means a legacy row and a stable-destination-ID
// row for the same actual endpoint land in the same group.
//
// Within each destination's group, records are sorted oldest-first by
// Item.FirstFailed (sortAPIRetryRecordsOldestFirst) -- the original
// per-destination fairness property from the plain-sort shape is preserved
// exactly: the oldest failure for a given destination is still that
// destination's first pick every cycle.
//
// Destinations are then visited in a fixed, deterministic order: by each
// destination's own OLDEST record's FirstFailed ascending (ties broken by
// destinationID, never by map order -- reintroducing map-order dependence
// here would defeat the entire point of this change). This choice keeps the
// property that the single oldest failure across ALL destinations is always
// attempted first (round 0, first destination visited), while still
// guaranteeing every destination a turn before any destination gets a
// second attempt. The cap itself, and everything downstream of the returned
// order (attempt, failure, dead-letter, audit), is unchanged -- this
// function only reorders the full slice; processAPIRetryQueue's existing
// attemptLimit cap-and-break still decides how much of the reordered slice
// is actually attempted.
//
// Guarantee: every destination with at least one queued record gets at
// least one attempt this cycle, PROVIDED attemptLimit >= the number of
// distinct destinations with queued records AND none of those records is
// skipped for an unrelated reason (unknown destination, disabled, missing
// URL, quiet hours, already sent, still in the current API window, invalid
// payload, or filtered) -- this function only controls ORDER; it cannot
// force an attempt that the existing per-record skip logic would refuse
// regardless of order. When attemptLimit is smaller than the number of
// distinct destinations, the guarantee degrades to: exactly attemptLimit
// destinations (those with the globally oldest per-destination-oldest
// record) get one attempt each; the rest get none this cycle (see CHANGELOG
// for how this can compound across cycles).
func roundRobinAPIRetryRecords(records []status.QueuedRetryRecord, legacyMessengerDestination map[string]string) []status.QueuedRetryRecord {
	if len(records) <= 1 {
		return records
	}

	groups := make(map[string][]status.QueuedRetryRecord)
	var destOrder []string
	for _, r := range records {
		_, destinationID := apiRetryRoute(r.Item, legacyMessengerDestination)
		if _, seen := groups[destinationID]; !seen {
			destOrder = append(destOrder, destinationID)
		}
		groups[destinationID] = append(groups[destinationID], r)
	}

	if len(destOrder) == 1 {
		sortAPIRetryRecordsOldestFirst(groups[destOrder[0]])
		return groups[destOrder[0]]
	}

	for _, dest := range destOrder {
		sortAPIRetryRecordsOldestFirst(groups[dest])
	}

	// Deterministic destination-visit order: oldest-first by each
	// destination's own oldest record (now at index 0 of its group, after
	// the per-group sort above), ties broken by destinationID -- a stable,
	// content-derived order, never Go's randomized map iteration. The
	// initial `destOrder` build order above (first-encounter in `records`,
	// itself map-iteration-derived since records comes straight from
	// GetQueuedRetryItemsByType) is fully superseded by this sort -- it is
	// irrelevant to the final result.
	sort.Slice(destOrder, func(i, j int) bool {
		ti := timeutil.ParseFlexibleTimestampOrZero(groups[destOrder[i]][0].Item.FirstFailed)
		tj := timeutil.ParseFlexibleTimestampOrZero(groups[destOrder[j]][0].Item.FirstFailed)
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return destOrder[i] < destOrder[j]
	})

	ordered := make([]status.QueuedRetryRecord, 0, len(records))
	for round := 0; ; round++ {
		progressed := false
		for _, dest := range destOrder {
			group := groups[dest]
			if round < len(group) {
				ordered = append(ordered, group[round])
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return ordered
}

func (s *Scheduler) queuePendingAPIEntriesForTarget(entries []model.RansomwareEntry, target apiDeliveryTarget, reason string) int {
	if s.dryRun || len(entries) == 0 {
		return 0
	}
	if reason == "" {
		reason = "pending API delivery"
	}

	queued := 0
	for _, entry := range entries {
		key := model.GenerateRansomwareEntryKey(entry)
		title := model.DisplayRansomwareTitle(entry)
		entryJSON, err := json.Marshal(entry)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"item_key":       key,
				"destination_id": target.destinationID,
				"messenger":      target.messenger,
				"item_type":      status.RetryItemTypeAPI.String(),
			}).Error("Failed to serialize quiet-hours API payload")
			continue
		}

		if s.statusTracker.EnqueueDeferredRetryForDestination(
			key,
			target.destinationID,
			target.messenger,
			status.RetryItemTypeAPI.String(),
			title,
			reason,
			entryJSON,
		) {
			queued++
		}
	}

	if queued > 0 {
		s.persistStatus("api entries queued for retry")
	}
	return queued
}

func (s *Scheduler) deferAPIEntriesForQuietHoursForTarget(entries []model.RansomwareEntry, target apiDeliveryTarget) int {
	return s.queuePendingAPIEntriesForTarget(entries, target, "deferred by quiet hours")
}

func (s *Scheduler) deferAPIEntriesForQuietHours(
	entries []model.RansomwareEntry,
	webhookURL, messenger, destinationID string,
) int {
	return s.deferAPIEntriesForQuietHoursForTarget(entries, apiDeliveryTarget{
		url:           webhookURL,
		destinationID: destinationID,
		messenger:     messenger,
	})
}

func (s *Scheduler) sendAPIEntriesIndividually(ctx context.Context, entries []model.RansomwareEntry, target apiDeliveryTarget) {
	if len(entries) == 0 {
		log.Debug("No entries to send")
		return
	}
	defer func() {
		if !s.dryRun {
			s.persistStatus("api individual delivery finished")
		}
	}()

	cfg := s.getConfig()
	log.WithFields(log.Fields{
		"messenger":     target.messenger,
		"total_entries": len(entries),
	}).Info("Starting individual API send")

	successCount := 0
	for i, entry := range entries {
		select {
		case <-ctx.Done():
			log.WithFields(log.Fields{
				"messenger": target.messenger,
				"sent":      successCount,
			}).Warn("Context cancelled, stopping API send")
			return
		default:
		}

		if s.dryRun {
			fields := log.Fields{
				"entry_id":       entry.ID,
				"group":          entry.Group,
				"country":        entry.Country,
				"has_victim":     entry.Victim != "",
				"mode":           dryRunModeLabel,
				"messenger":      target.messenger,
				"destination_id": target.destinationID,
				"item_type":      status.RetryItemTypeAPI.String(),
			}
			addDryRunRansomwarePreview(fields, target.messenger, entry, config.NotificationFormatOptions(&cfg.Format))
			log.WithFields(fields).Info("[DRY-RUN] Would send ransomware entry")
			successCount++
			continue
		}

		if !waitForSendDelay(ctx, target.delay) {
			log.WithFields(log.Fields{
				"messenger": target.messenger,
				"sent":      successCount,
			}).Warn("Context cancelled during API send delay")
			return
		}
		key := model.GenerateRansomwareEntryKey(entry)
		title := model.DisplayRansomwareTitle(entry)
		log.WithFields(webhookProgressFields(i, len(entries), target.destinationID)).Info("Sending API item to webhook")
		if target.send == nil {
			log.WithFields(log.Fields{
				"destination_id": target.destinationID,
				"messenger":      target.messenger,
				"group":          entry.Group,
				"item_key":       key,
				"item_type":      status.RetryItemTypeAPI.String(),
				"entry_id":       entry.ID,
				"has_victim":     entry.Victim != "",
			}).Error("Missing API webhook sender")
			continue
		}
		if err := s.sendWithWebhookLock(ctx, target.messenger, target.url, func() error {
			return target.send(ctx, target.url, entry, config.NotificationFormatOptions(&cfg.Format))
		}); err != nil {
			log.WithFields(webhookFailureFields(target.messenger, err, log.Fields{
				"destination_id": target.destinationID,
				"group":          entry.Group,
				"item_key":       key,
				"item_type":      status.RetryItemTypeAPI.String(),
				"entry_id":       entry.ID,
				"has_victim":     entry.Victim != "",
			})).Error("Failed to send ransomware entry to webhook")
			entryJSON, _ := json.Marshal(entry)
			s.recordWebhookDeliveryFailure(
				key,
				target.destinationID,
				target.messenger,
				status.RetryItemTypeAPI.String(),
				title,
				err,
				cfg.RetryMaxAttempts,
				cfg.RetryWindow,
				entryJSON,
			)
		} else {
			markAPIItemSentForDestination(s.statusTracker, key, title, target.destinationID, target.url)
			s.recordAPIDeliveryAuditEvent(
				entry,
				target,
				status.DeliveryAuditOutcomeDelivered,
				"",
				nil,
			)
			s.statusTracker.RemoveFromRetryQueueByDestination(key, target.destinationID, target.url)
			s.persistStatus("api item delivered")
			successCount++
		}
	}

	log.WithFields(log.Fields{
		"messenger":     target.messenger,
		"total_sent":    successCount,
		"total_entries": len(entries),
	}).Info("Individual API send completed")

	if successCount > 0 && !s.dryRun {
		s.statusTracker.CleanupOldEntries()
	}

	if !s.dryRun {
		s.persistStatus("api individual delivery")
	}
}

// sendAPIEntriesIndividuallyToDiscord sends API entries to Discord one at a time with rate limiting.
func (s *Scheduler) sendAPIEntriesIndividuallyToDiscord(
	ctx context.Context,
	entries []model.RansomwareEntry,
	webhookURL string,
	destinationIDs ...string,
) {
	cfg := s.getConfig()
	delivery, _ := s.messengerDeliveryForConfig(status.MessengerDiscord.String(), cfg)
	target := apiDeliveryTarget{
		url:           webhookURL,
		destinationID: optionalDestinationID(apiDestinationID(status.MessengerDiscord), destinationIDs...),
		messenger:     status.MessengerDiscord.String(),
		logName:       delivery.logName,
		delay:         delivery.delay,
	}
	if delivery.sender != nil {
		target.send = delivery.sender.SendRansomwareEntry
	}
	s.sendAPIEntriesIndividually(ctx, entries, target)
}

// sendAPIEntriesIndividuallyToSlack sends API entries to Slack one at a time with rate limiting.
func (s *Scheduler) sendAPIEntriesIndividuallyToSlack(
	ctx context.Context,
	entries []model.RansomwareEntry,
	webhookURL string,
	destinationIDs ...string,
) {
	cfg := s.getConfig()
	delivery, _ := s.messengerDeliveryForConfig(status.MessengerSlack.String(), cfg)
	target := apiDeliveryTarget{
		url:           webhookURL,
		destinationID: optionalDestinationID(apiDestinationID(status.MessengerSlack), destinationIDs...),
		messenger:     status.MessengerSlack.String(),
		logName:       delivery.logName,
		delay:         delivery.delay,
	}
	if delivery.sender != nil {
		target.send = delivery.sender.SendRansomwareEntry
	}
	s.sendAPIEntriesIndividually(ctx, entries, target)
}

// processAPIRetryQueue consumes API retry items that are no longer in the current
// API response. Items still in allEntries are retried naturally by the normal send
// loop; this method handles items that have fallen out of the API window. Retry
// records persist stable destination IDs, so webhook URLs are reconstructed from
// the current config before replay.
type apiRetryWebhookInfo struct {
	url              string
	destinationID    string
	enabled          bool
	quietHours       *quiethours.Policy
	quietHoursActive bool
	filters          *filter.Rules
}

type apiRetryJob struct {
	item          status.RetryRecord
	queueKey      string
	destinationID string
	webhook       apiRetryWebhookInfo
	entry         model.RansomwareEntry
	primaryKey    string
	title         string
}

func apiRetryCurrentEntryMap(entries []model.RansomwareEntry) map[string]model.RansomwareEntry {
	entryMap := make(map[string]model.RansomwareEntry, len(entries))
	for _, entry := range entries {
		for _, key := range model.GenerateRansomwareEntryLookupKeys(entry) {
			entryMap[key] = entry
		}
	}
	return entryMap
}

func apiRetryWebhookMaps(cfg *config.Config) (map[string]apiRetryWebhookInfo, map[string]string) {
	messengerWebhooks := make(map[string]apiRetryWebhookInfo)
	legacyMessengerDestination := make(map[string]string)
	for _, targetConfig := range config.WebhookTargetsForType(cfg, config.WebhookTypeRansomware) {
		messenger, ok := messengerForWebhookPlatform(targetConfig.Platform)
		if !ok {
			continue
		}
		destinationID := apiDestinationIDForTarget(messenger, targetConfig.DestinationSuffix)
		quietHours := config.QuietHoursPolicy(targetConfig.Webhook.QuietHours)
		messengerWebhooks[destinationID] = apiRetryWebhookInfo{
			url:              targetConfig.Webhook.URL,
			destinationID:    destinationID,
			enabled:          targetConfig.Webhook.Enabled,
			quietHours:       quietHours,
			quietHoursActive: quietHours.IsActive(),
			filters:          config.FilterRules(targetConfig.Webhook.Filters),
		}
		if targetConfig.DestinationSuffix == "" {
			legacyMessengerDestination[messenger.String()] = destinationID
		}
	}
	return messengerWebhooks, legacyMessengerDestination
}

func apiRetryRoute(item status.RetryRecord, legacyMessengerDestination map[string]string) (string, string) {
	routeID := item.DestinationID
	if routeID == "" {
		routeID = legacyMessengerDestination[item.Messenger]
	}
	destinationID := item.DestinationID
	if destinationID == "" {
		destinationID = routeID
	}
	return routeID, destinationID
}

func retryEntryFromPayload(item status.RetryRecord) (model.RansomwareEntry, string, error) {
	if item.Payload == nil {
		return model.RansomwareEntry{}, "missing API retry payload", errors.New("missing API retry payload")
	}
	if item.PayloadVersion != "" && item.PayloadVersion != status.RetryPayloadVersionRansomwareEntryV1 {
		return model.RansomwareEntry{},
			"unsupported API retry payload version",
			fmt.Errorf("unsupported API retry payload version %q", item.PayloadVersion)
	}

	var entry model.RansomwareEntry
	if err := json.Unmarshal(item.Payload, &entry); err != nil {
		return model.RansomwareEntry{}, "malformed API retry payload", err
	}
	return entry, "", nil
}

func (s *Scheduler) processAPIRetryQueue(ctx context.Context, allEntries []model.RansomwareEntry) {
	cfg := s.getConfig()

	retryRecords := s.statusTracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String())
	if len(retryRecords) == 0 {
		return
	}

	entryMap := apiRetryCurrentEntryMap(allEntries)
	messengerWebhooks, legacyMessengerDestination := apiRetryWebhookMaps(cfg)
	retryRecords = roundRobinAPIRetryRecords(retryRecords, legacyMessengerDestination)

	var changedMu sync.Mutex
	changed := false
	markChanged := func() {
		changedMu.Lock()
		changed = true
		changedMu.Unlock()
	}
	hasChanged := func() bool {
		changedMu.Lock()
		defer changedMu.Unlock()
		return changed
	}
	defer func() {
		if hasChanged() {
			s.persistStatus("api retry queue processed")
		}
	}()
	attemptLimit := cfg.APIMaxEntriesPerCycle
	attemptedSends := 0
	retryGroups := make(map[string][]apiRetryJob)
	for _, retryRecord := range retryRecords {
		item := retryRecord.Item
		select {
		case <-ctx.Done():
			log.WithFields(apiRetrySkipFields(item, retryRecord.QueueKey, item.DestinationID, "context_cancelled")).Warn("Context cancelled, stopping API retry queue processing")
			return
		default:
		}

		routeID, destinationID := apiRetryRoute(item, legacyMessengerDestination)
		wh, ok := messengerWebhooks[routeID]
		if !ok {
			fields := apiRetrySkipFields(item, retryRecord.QueueKey, destinationID, "unknown_destination")
			fields["route_id"] = routeID
			log.WithFields(fields).Warn("Skipping API retry item with unknown destination")
			continue
		}
		if !wh.enabled {
			log.WithFields(apiRetrySkipFields(item, retryRecord.QueueKey, destinationID, "destination_disabled")).Warn("Skipping API retry item because destination is disabled")
			continue
		}
		if wh.url == "" {
			log.WithFields(apiRetrySkipFields(item, retryRecord.QueueKey, destinationID, "destination_missing_url")).Warn("Skipping API retry item because destination URL is missing")
			continue
		}
		if wh.quietHoursActive {
			fields := apiRetrySkipFields(item, retryRecord.QueueKey, destinationID, "quiet_hours")
			if wh.quietHours != nil {
				fields["quiet_hours_start"] = wh.quietHours.Start
				fields["quiet_hours_end"] = wh.quietHours.End
			}
			log.WithFields(fields).Info("Deferring API retry item because quiet hours are active")
			details := map[string]string{"queue_key": retryRecord.QueueKey}
			if wh.quietHours != nil {
				details["quiet_hours_start"] = wh.quietHours.Start
				details["quiet_hours_end"] = wh.quietHours.End
			}
			s.recordAPIRetryAuditEvent(
				item,
				destinationID,
				status.DeliveryAuditOutcomeQuietHours,
				status.DeliveryAuditReasonQuietHours,
				details,
			)
			continue
		}

		// If already sent (e.g. by the normal send loop this cycle), just clean up
		if s.statusTracker.IsAPIItemSentToDestination(item.ItemKey, destinationID, wh.url) {
			log.WithFields(apiRetrySkipFields(item, retryRecord.QueueKey, destinationID, "already_sent")).Info("Removing API retry item already marked sent")
			s.statusTracker.RemoveRetryQueueEntry(retryRecord.QueueKey)
			markChanged()
			continue
		}

		// If entry is still in API window, the normal send loop handles it — skip
		if entry, inWindow := entryMap[item.ItemKey]; inWindow {
			if !filter.MatchesAPIEntry(wh.filters, entry) {
				log.WithFields(apiRetrySkipFields(item, retryRecord.QueueKey, destinationID, "filtered_in_window")).Info("In-window API retry item filtered out by current webhook rules, keeping queued")
				s.recordAPIRetryAuditEvent(
					item,
					destinationID,
					status.DeliveryAuditOutcomeFiltered,
					status.DeliveryAuditReasonFilterMismatch,
					map[string]string{
						"queue_key":   retryRecord.QueueKey,
						"retry_scope": "in_api_window",
					},
				)
			} else {
				log.WithFields(apiRetrySkipFields(item, retryRecord.QueueKey, destinationID, "in_api_window")).Info("Skipping API retry item still present in current API response")
			}
			continue
		}

		entry, deadLetterReason, err := retryEntryFromPayload(item)
		if err != nil {
			fields := apiRetrySkipFields(item, retryRecord.QueueKey, destinationID, "invalid_payload")
			if item.PayloadVersion != "" {
				fields["payload_version"] = item.PayloadVersion
			}
			log.WithError(err).WithFields(fields).Error("Failed to reconstruct API retry item from payload")
			s.deadLetterMalformedAPIRetryItem(retryRecord.QueueKey, deadLetterReason)
			markChanged()
			continue
		}

		// Re-check filter (config may have changed since first attempt)
		if !filter.MatchesAPIEntry(wh.filters, entry) {
			log.WithFields(apiRetrySkipFields(item, retryRecord.QueueKey, destinationID, "filtered_reconstructed")).Info("API retry item filtered out by current webhook rules, keeping queued")
			s.recordAPIRetryAuditEvent(
				item,
				destinationID,
				status.DeliveryAuditOutcomeFiltered,
				status.DeliveryAuditReasonFilterMismatch,
				map[string]string{
					"queue_key":   retryRecord.QueueKey,
					"retry_scope": "reconstructed_payload",
				},
			)
			continue
		}

		// Attempt re-send
		if attemptLimit > 0 && attemptedSends >= attemptLimit {
			log.WithFields(log.Fields{
				"cycle_limit":       attemptLimit,
				"attempted_sends":   attemptedSends,
				"remaining_retries": len(retryRecords) - attemptedSends,
			}).Info("Capping API retry queue processing for this cycle")
			break
		}
		attemptedSends++
		primaryKey := model.GenerateRansomwareEntryKey(entry)
		groupKey := item.Messenger + "\x00" + destinationID + "\x00" + wh.url
		retryGroups[groupKey] = append(retryGroups[groupKey], apiRetryJob{
			item:          item,
			queueKey:      retryRecord.QueueKey,
			destinationID: destinationID,
			webhook:       wh,
			entry:         entry,
			primaryKey:    primaryKey,
			title:         model.DisplayRansomwareTitle(entry),
		})
	}

	s.processAPIRetryGroups(ctx, cfg, retryGroups, markChanged)

	if hasChanged() {
		s.persistStatus("api retry queue processed")
	}
}

func (s *Scheduler) processAPIRetryGroups(
	ctx context.Context,
	cfg *config.Config,
	retryGroups map[string][]apiRetryJob,
	markChanged func(),
) {
	if len(retryGroups) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, group := range retryGroups {
		group := group
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, job := range group {
				if !s.processAPIRetryJob(ctx, cfg, job, markChanged) {
					return
				}
			}
		}()
	}
	wg.Wait()
}

func (s *Scheduler) processAPIRetryJob(
	ctx context.Context,
	cfg *config.Config,
	job apiRetryJob,
	markChanged func(),
) bool {
	item := job.item
	select {
	case <-ctx.Done():
		log.WithFields(apiRetrySkipFields(item, job.queueKey, job.destinationID, "context_cancelled")).Warn("Context cancelled, stopping API retry queue processing")
		return false
	default:
	}

	delivery, ok := s.messengerDeliveryForConfig(item.Messenger, cfg)
	if !ok {
		log.WithFields(apiRetrySkipFields(item, job.queueKey, job.destinationID, "unknown_messenger")).Warn("Skipping API retry item with unknown messenger")
		return true
	}
	if delivery.sender == nil {
		log.WithFields(apiRetrySkipFields(item, job.queueKey, job.destinationID, "missing_sender")).Error("Skipping API retry item with missing messenger sender")
		return true
	}
	if !waitForSendDelay(ctx, delivery.delay) {
		log.WithFields(apiRetrySkipFields(item, job.queueKey, job.destinationID, "context_cancelled")).Warn("Context cancelled during API retry send delay")
		return false
	}
	err := s.sendWithWebhookLock(ctx, item.Messenger, job.webhook.url, func() error {
		return delivery.sender.SendRansomwareEntry(ctx, job.webhook.url, job.entry, config.NotificationFormatOptions(&cfg.Format))
	})

	if err != nil {
		log.WithFields(webhookFailureFields(item.Messenger, err, log.Fields{
			"item_key":    item.ItemKey,
			"queue_key":   job.queueKey,
			"messenger":   item.Messenger,
			"retry_count": item.RetryCount,
		})).Warn("API retry send failed")
		s.recordRetryQueueDeliveryFailure(job.queueKey, item, err, cfg.RetryMaxAttempts, cfg.RetryWindow)
		markChanged()
		return true
	}

	markAPIItemSentForDestination(s.statusTracker, job.primaryKey, job.title, job.destinationID, job.webhook.url)
	s.recordAPIRetryAuditEvent(
		item,
		job.destinationID,
		status.DeliveryAuditOutcomeDelivered,
		status.DeliveryAuditReasonRetrySuccess,
		nil,
	)
	s.statusTracker.RemoveRetryQueueEntry(job.queueKey)
	s.persistStatus("api retry job delivered")
	markChanged()
	log.WithFields(log.Fields{
		"item_key":  job.primaryKey,
		"messenger": item.Messenger,
	}).Info("API retry send succeeded")
	return true
}

func apiRetrySkipFields(item status.RetryRecord, queueKey, destinationID, skipReason string) log.Fields {
	fields := log.Fields{
		"item_key":    item.ItemKey,
		"messenger":   item.Messenger,
		"retry_count": item.RetryCount,
		"queue_key":   queueKey,
		"skip_reason": skipReason,
	}
	if destinationID != "" {
		fields["destination_id"] = destinationID
	}
	return fields
}

func (s *Scheduler) deadLetterMalformedAPIRetryItem(queueKey, reason string) {
	s.statusTracker.DeadLetterRetryQueueEntryWithErrorInfo(queueKey, reason, status.RetryErrorInfo{
		ErrorCategory: "unreplayable_payload",
		Retryable:     boolPtr(false),
	})
}
