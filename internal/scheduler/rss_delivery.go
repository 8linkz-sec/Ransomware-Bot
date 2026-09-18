package scheduler

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filter"
	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
)

func (s *Scheduler) isRSSItemSentToWebhook(keys []string, destinationID, webhookURL string) bool {
	for _, key := range keys {
		if s.statusTracker.IsRSSItemSentToDestination(key, destinationID, webhookURL) {
			return true
		}
	}
	return false
}

func (s *Scheduler) sendRSSEntryToTarget(
	ctx context.Context,
	cfg *config.Config,
	feedType string,
	target webhookTarget,
	item rssDeliveryItem,
	mode rssDeliveryMode,
	index int,
	total int,
	markAttempted func(itemKey, destinationID string),
) rssDeliveryOutcome {
	destinationID := targetDestinationID(target)
	messenger := target.messenger

	select {
	case <-ctx.Done():
		log.WithField("messenger", messenger).Warn(rssDeliveryCancelMessage(mode))
		return rssDeliveryOutcomeCanceled
	default:
	}

	contentSig := rss.GenerateEntryContentSignature(item.entry)
	if s.isRSSItemSentToWebhook(item.lookupKeys, destinationID, target.url) {
		s.recordRSSDeliveryAuditEvent(
			item,
			feedType,
			target,
			mode,
			status.DeliveryAuditOutcomeDeduplicated,
			status.DeliveryAuditReasonAlreadySent,
			nil,
		)
		return rssDeliveryOutcomeAlreadySent
	}
	if s.isRSSItemSentToWebhook(rss.GenerateEntryContentSignatureLookupKeys(item.entry), destinationID, target.url) {
		if !s.dryRun {
			markRSSItemDedupedForDestination(s.statusTracker, item.key, item.title, item.feedTitle, destinationID, target.url)
			s.persistStatus("rss item already sent")
		}
		s.recordRSSDeliveryAuditEvent(
			item,
			feedType,
			target,
			mode,
			status.DeliveryAuditOutcomeDeduplicated,
			status.DeliveryAuditReasonAlreadySent,
			map[string]string{"dedupe_key_type": "content_signature"},
		)
		return rssDeliveryOutcomeAlreadySent
	}

	if !filter.MatchesRSSEntry(target.filters, item.entry) {
		log.WithFields(log.Fields{
			"item_key":  item.key,
			"feed_url":  textutil.RedactURLCredentials(item.feedURL),
			"messenger": messenger,
		}).Debug(rssDeliveryFilteredMessage(mode))
		if !s.dryRun {
			// Only the item key is marked. The content signature is a *sent*
			// concept (same story republished under a new GUID); it excludes
			// FeedURL/Link/GUID, which filters can discriminate on, so writing
			// it here would suppress a different, allowed entry that happens to
			// share title/published/description.
			markRSSItemSkippedForDestination(s.statusTracker, item.key, destinationID, target.url)
			s.statusTracker.RemoveFromRetryQueueByDestination(item.key, destinationID, target.url)
		}
		s.recordRSSDeliveryAuditEvent(
			item,
			feedType,
			target,
			mode,
			status.DeliveryAuditOutcomeFiltered,
			status.DeliveryAuditReasonFilterMismatch,
			nil,
		)
		return rssDeliveryOutcomeFiltered
	}

	// Couple the freshness gate to the dedup horizon for dated items: sending an
	// item older than the horizon guarantees a future duplicate (its sent marker
	// is pruned with the parsed item by age while it is still present upstream,
	// so the next poll re-delivers it). Undated items are exempt from the age
	// gate entirely (rssEntryExceedsMaxAge short-circuits on a zero Published);
	// this is deliberate, not an oversight (rejecting them here silently drops
	// every item a feed's date format the parser cannot read, forever). It does
	// NOT close the redelivery loop for them: ParsedAt, their retention age
	// basis, is stamped once at first sight and never refreshed by any
	// production path, so an undated item that stays upstream past
	// status_retention.rss_parsed_max_age is evicted, then re-enters as "new"
	// and is delivered again -- a real, recurring, roughly-annual redelivery
	// cycle per surviving undated item, tracked separately as a retention-layer
	// question, not fixed by this gate.
	sendMaxAge := rssSendMaxAgeForPublished(cfg, item.entry.Published)
	if rssEntryExceedsMaxAge(item.entry.Published, sendMaxAge) {
		fields := staleRSSLogFields(item.key, item.entry.Published, sendMaxAge)
		log.WithFields(fields).Info(rssDeliveryStaleMessage(mode))
		if mode == rssDeliveryModeRecovery {
			s.statusTracker.RemoveFromRetryQueueByDestination(item.key, destinationID, target.url)
		}
		s.recordRSSDeliveryAuditEvent(
			item,
			feedType,
			target,
			mode,
			status.DeliveryAuditOutcomeStale,
			status.DeliveryAuditReasonStale,
			map[string]string{
				"published":       formatLogTimestamp(item.entry.Published),
				"max_age_minutes": fmt.Sprint(int64(sendMaxAge / time.Minute)),
			},
		)
		return rssDeliveryOutcomeStale
	}

	if s.dryRun {
		fields := log.Fields{
			"item_key":       item.key,
			"destination_id": destinationID,
			"messenger":      messenger,
			"item_type":      status.RetryItemTypeRSS.String(),
			"feed_title":     item.feedTitle,
			"feed_url":       textutil.RedactURLCredentials(item.feedURL),
			"has_title":      item.title != "",
			"has_link":       item.entry.Link != "",
			"mode":           dryRunModeLabel,
		}
		addDryRunRSSPreview(fields, messenger, item.entry, config.NotificationFormatOptions(&cfg.Format), feedType)
		log.WithFields(fields).Info("[DRY-RUN] Would send RSS entry to webhook")
		return rssDeliveryOutcomeSent
	}

	if markAttempted != nil {
		markAttempted(item.key, destinationID)
	}
	delivery, ok := s.messengerDeliveryForConfig(messenger, cfg)
	if !ok || delivery.sender == nil {
		log.WithFields(log.Fields{
			"destination_id": destinationID,
			"messenger":      messenger,
			"item_key":       item.key,
			"item_type":      status.RetryItemTypeRSS.String(),
		}).Error(rssDeliveryMissingSenderMessage(mode))
		return rssDeliveryOutcomeFailed
	}
	if !waitForSendDelay(ctx, delivery.delay) {
		log.WithField("messenger", messenger).Warn(rssDeliveryDelayCancelMessage(mode))
		return rssDeliveryOutcomeCanceled
	}

	progressFields := webhookProgressFields(index, total, destinationID)
	log.WithFields(progressFields).Info(rssDeliveryProgressMessage(mode))
	err := s.sendWithWebhookLock(ctx, messenger, target.url, func() error {
		return delivery.sender.SendRSSEntry(ctx, target.url, item.entry, feedType, config.NotificationFormatOptions(&cfg.Format))
	})
	if err != nil {
		log.WithFields(webhookFailureFields(messenger, err, log.Fields{
			"item_key":       item.key,
			"destination_id": destinationID,
			"feed_type":      feedType,
			"feed_url":       textutil.RedactURLCredentials(item.feedURL),
			"messenger":      messenger,
		})).Error("Failed to send RSS entry to webhook")
		s.recordWebhookDeliveryFailure(
			item.key,
			destinationID,
			messenger,
			status.RetryItemTypeRSS.String(),
			item.title,
			err,
			cfg.RetryMaxAttempts,
			cfg.RetryWindow,
		)
		return rssDeliveryOutcomeFailed
	}

	markRSSItemSentForDestination(s.statusTracker, item.key, item.title, item.feedTitle, destinationID, target.url)
	markRSSItemSentForDestination(s.statusTracker, contentSig, item.title, item.feedTitle, destinationID, target.url)
	s.recordRSSDeliveryAuditEvent(
		item,
		feedType,
		target,
		mode,
		status.DeliveryAuditOutcomeDelivered,
		"",
		nil,
	)
	s.statusTracker.RemoveFromRetryQueueByDestination(item.key, destinationID, target.url)
	s.persistStatus("rss item delivered")
	return rssDeliveryOutcomeSent
}

func rssDeliveryCancelMessage(mode rssDeliveryMode) string {
	if mode == rssDeliveryModeRecovery {
		return "Context cancelled, stopping RSS send"
	}
	return "Context cancelled, stopping RSS entry send"
}

func rssDeliveryDelayCancelMessage(mode rssDeliveryMode) string {
	if mode == rssDeliveryModeRecovery {
		return "Context cancelled during RSS recovery send delay"
	}
	return "Context cancelled during RSS send delay"
}

func rssDeliveryFilteredMessage(mode rssDeliveryMode) string {
	if mode == rssDeliveryModeRecovery {
		return "RSS recovery entry filtered out by webhook rules"
	}
	return "RSS entry filtered out by webhook rules"
}

func rssDeliveryMissingSenderMessage(mode rssDeliveryMode) string {
	if mode == rssDeliveryModeRecovery {
		return "Missing RSS recovery webhook sender"
	}
	return "Missing RSS webhook sender"
}

func rssDeliveryProgressMessage(mode rssDeliveryMode) string {
	if mode == rssDeliveryModeRecovery {
		return "Sending RSS recovery item to webhook"
	}
	return "Sending RSS item to webhook"
}

func rssDeliveryStaleMessage(mode rssDeliveryMode) string {
	if mode == rssDeliveryModeRecovery {
		return "Skipping stale RSS entry in recovery without marking it sent"
	}
	return "Skipping stale RSS entry without marking it sent"
}

// sendParsedRSSToWebhooks sends freshly parsed RSS results to all enabled webhook targets
func (s *Scheduler) sendParsedRSSToWebhooks(
	ctx context.Context,
	feedResults *rss.FeedResults,
	feedType string,
	targets []webhookTarget,
	attemptedRSSDeliveries rssDeliveryAttemptSet,
) {
	s.sendParsedRSSToWebhooksWithConfig(
		ctx,
		s.getConfig(),
		feedResults,
		feedType,
		targets,
		attemptedRSSDeliveries,
	)
}

//nolint:gocyclo // core workflow, kept linear on purpose; see WORKFLOW.md §5.6/§6.1 (RSS delivery)
func (s *Scheduler) sendParsedRSSToWebhooksWithConfig(
	ctx context.Context,
	cfg *config.Config,
	feedResults *rss.FeedResults,
	feedType string,
	targets []webhookTarget,
	attemptedRSSDeliveries rssDeliveryAttemptSet,
) {
	// Collect and sort all entries chronologically
	var allEntries []model.RSSEntry
	for feedURL, entries := range feedResults.Entries {
		// Update feed status: success for feeds without errors, failure for feeds with errors
		if !s.dryRun {
			if errMsg, failed := feedResults.FeedErrors[feedURL]; failed {
				operatorError := rss.OperatorErrorMessageFromString(errMsg)
				s.statusTracker.UpdateFeedStatusWithErrorInfo(feedURL, false, 0, operatorError, rssSourceErrorInfoFromString(errMsg))
			} else {
				s.statusTracker.UpdateFeedStatusWithErrorInfoAndValidators(
					feedURL,
					true,
					len(entries),
					"",
					status.SourceErrorInfo{},
					feedResults.FeedValidators[feedURL],
				)
			}
		}

		allEntries = append(allEntries, entries...)
	}
	if !s.dryRun {
		s.persistStatus("rss entries marked parsed")
	}

	if len(allEntries) == 0 {
		fields := log.Fields{
			"feed_type":       feedType,
			"feed_count":      len(feedResults.Entries),
			"target_count":    len(targets),
			"new_entry_count": 0,
		}
		if s.dryRun {
			fields["mode"] = dryRunModeLabel
			fields["hint"] = "No new RSS entries; check feed reachability, sent status, and webhook filters."
			log.WithFields(fields).Info("[DRY-RUN] No RSS entries to preview")
		} else {
			log.WithFields(fields).Debug("No new RSS entries found")
		}
		return
	}
	defer func() {
		if !s.dryRun {
			s.persistStatus("rss delivery finished")
		}
	}()

	undatedSortTime := time.Now().UTC()
	sort.Slice(allEntries, func(i, j int) bool {
		return rssEntrySortTime(allEntries[i], undatedSortTime).Before(rssEntrySortTime(allEntries[j], undatedSortTime))
	})

	log.WithFields(log.Fields{
		"feed_type":     feedType,
		"total_entries": len(allEntries),
		"targets":       len(targets),
	}).Info("Sending parsed RSS entries to webhooks")

	var attemptedMu sync.Mutex
	markAttemptedEntries := func(entries []model.RSSEntry, destinationID string) {
		if len(entries) == 0 || attemptedRSSDeliveries == nil {
			return
		}
		attemptedMu.Lock()
		defer attemptedMu.Unlock()
		markRSSDeliveryAttemptsForEntries(entries, destinationID, attemptedRSSDeliveries)
	}
	markAttemptedEntry := func(itemKey, destinationID string) {
		if attemptedRSSDeliveries == nil {
			return
		}
		attemptedMu.Lock()
		defer attemptedMu.Unlock()
		attemptedRSSDeliveries[rssDeliveryAttemptKey(itemKey, destinationID)] = struct{}{}
	}

	var targetWG sync.WaitGroup
	// Send each entry to each webhook target (per-webhook dedup). Targets are
	// independent destinations, so one slow provider should not block another.
	for _, target := range targets {
		target := target
		targetWG.Add(1)
		go func() {
			defer targetWG.Done()

			destinationID := targetDestinationID(target)
			// Skip this webhook if quiet hours are active
			if target.quietHours.IsActive() {
				log.WithFields(log.Fields{
					"messenger": target.messenger,
					"feed_type": feedType,
					"start":     target.quietHours.Start,
					"end":       target.quietHours.End,
				}).Info("Quiet hours active, skipping RSS send for this webhook")
				s.recordRSSQuietHoursDeferredEntries(allEntries, feedType, target, rssDeliveryModeFresh, cfg.RSSMaxEntriesPerCycle)
				return
			}
			successCount := 0
			filteredCount := 0
			entriesForTarget, cappedCount := limitRSSEntriesForCycle(allEntries, cfg.RSSMaxEntriesPerCycle)
			if cappedCount > 0 {
				markAttemptedEntries(allEntries[len(entriesForTarget):], destinationID)
				log.WithFields(log.Fields{
					"feed_type":        feedType,
					"messenger":        target.messenger,
					"cycle_limit":      cfg.RSSMaxEntriesPerCycle,
					"deferred_by_cap":  cappedCount,
					"send_batch_count": len(entriesForTarget),
				}).Info("Capping RSS fresh send batch for this webhook")
			}
			for i, entry := range entriesForTarget {
				switch s.sendRSSEntryToTarget(
					ctx,
					cfg,
					feedType,
					target,
					freshRSSDeliveryItem(entry),
					rssDeliveryModeFresh,
					i,
					len(entriesForTarget),
					markAttemptedEntry,
				) {
				case rssDeliveryOutcomeSent:
					successCount++
				case rssDeliveryOutcomeFiltered:
					filteredCount++
				case rssDeliveryOutcomeCanceled:
					return
				}
			}

			log.WithFields(log.Fields{
				"total_sent":      successCount,
				"filtered_count":  filteredCount,
				"total_items":     len(entriesForTarget),
				"deferred_by_cap": cappedCount,
				"feed_type":       feedType,
				"messenger":       target.messenger,
			}).Info("RSS entries send to webhook completed")
			if s.dryRun && successCount == 0 {
				log.WithFields(log.Fields{
					"feed_type":       feedType,
					"feed_count":      len(feedResults.Entries),
					"messenger":       target.messenger,
					"destination_id":  targetDestinationID(target),
					"item_type":       status.RetryItemTypeRSS.String(),
					"new_entry_count": len(allEntries),
					"preview_count":   0,
					"filtered_count":  filteredCount,
					"total_items":     len(entriesForTarget),
					"deferred_by_cap": cappedCount,
					"mode":            dryRunModeLabel,
					"hint":            "No RSS entries matched this target; check webhook filters, sent status, and freshness limits.",
				}).Info("[DRY-RUN] No RSS entries matched target preview")
			}
		}()
	}
	targetWG.Wait()

	if !s.dryRun {
		// Perform cleanup once after batch processing
		s.statusTracker.CleanupOldEntriesForRSSDestinations(destinationIDsFromTargets(targets))

		// Flush all pending changes to disk (batched instead of per-item)
		s.persistStatus("rss delivery flushed")
	}
}

// sendUnsentRSSItems sends all RSS items that were parsed but not yet sent (per-webhook recovery)
func (s *Scheduler) sendUnsentRSSItems(ctx context.Context, attemptedRSSDeliveries rssDeliveryAttemptSet) {
	s.sendUnsentRSSItemsWithConfig(ctx, s.getConfig(), attemptedRSSDeliveries)
}

func (s *Scheduler) sendUnsentRSSItemsWithConfig(
	ctx context.Context,
	cfg *config.Config,
	attemptedRSSDeliveries rssDeliveryAttemptSet,
) {
	// Check if context is cancelled before starting
	select {
	case <-ctx.Done():
		log.Debug("RSS unsent items sending cancelled due to context")
		return
	default:
	}

	feedTypeMap := buildFeedTypeMap(cfg)

	for _, route := range rssFeedRoutes(cfg) {
		feedType := route.feedType
		feedURLScope := stringSetFromSlice(route.feedURLs)
		targets := webhookTargetsForRoute(cfg, route)
		var targetWG sync.WaitGroup
		for _, target := range targets {
			target := target
			targetWG.Add(1)
			go func() {
				defer targetWG.Done()

				destinationID := targetDestinationID(target)
				unsentItems := s.statusTracker.GetUnsentRSSRecoveryItemsForDestinationFeedType(
					destinationID,
					feedType,
					feedURLScope,
					target.url,
				)
				if len(unsentItems) == 0 {
					return
				}

				// Skip this webhook if quiet hours are active
				if target.quietHours.IsActive() {
					log.WithFields(log.Fields{
						"messenger":    target.messenger,
						"feed_type":    feedType,
						"start":        target.quietHours.Start,
						"end":          target.quietHours.End,
						"unsent_count": len(unsentItems),
					}).Info("Quiet hours active, skipping unsent RSS recovery for this webhook")
					s.recordRSSQuietHoursDeferredItems(unsentItems, feedType, target, rssDeliveryModeRecovery, cfg.RSSMaxEntriesPerCycle)
					return
				}

				// Prefer persisted feed type. The feed URL map is only a legacy
				// fallback for status files written before feed_type existed.
				var filteredItems []status.UnsentRSSItem
				for _, item := range unsentItems {
					itemFeedType := storedRSSItemFeedType(item, feedTypeMap)
					if itemFeedType == "" {
						log.WithFields(log.Fields{
							"item_key":       item.Key,
							"feed_url":       textutil.RedactURLCredentials(item.FeedURL),
							"feed_type":      feedType,
							"destination_id": destinationID,
							"messenger":      target.messenger,
							"skip_reason":    "unmapped_feed_type",
						}).Warn("Skipping RSS recovery item whose feed is no longer mapped to a feed type")
						continue
					}
					if itemFeedType == feedType {
						filteredItems = append(filteredItems, item)
					}
				}
				filteredItems = s.filterAttemptedRSSItems(filteredItems, destinationID, attemptedRSSDeliveries)
				filteredItems = s.filterDeadLetteredRSSItems(filteredItems, target.messenger, destinationID, target.url)

				if len(filteredItems) == 0 {
					return
				}
				sendBatch, cappedCount := limitUnsentRSSItemsForCycle(filteredItems, cfg.RSSMaxEntriesPerCycle, cfg)

				log.WithFields(log.Fields{
					"unsent_count":     len(filteredItems),
					"feed_type":        feedType,
					"messenger":        target.messenger,
					"cycle_limit":      cfg.RSSMaxEntriesPerCycle,
					"deferred_by_cap":  cappedCount,
					"send_batch_count": len(sendBatch),
				}).Info("Found unsent RSS items for webhook, sending")

				s.sendRSSItemsToWebhookWithConfig(
					ctx,
					cfg,
					sendBatch,
					feedType,
					target,
				)
			}()
		}
		targetWG.Wait()
	}
}

func stringSetFromSlice(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	return set
}

func storedRSSItemFeedType(item status.UnsentRSSItem, feedTypeMap map[string]string) string {
	if item.FeedType != "" {
		return item.FeedType
	}
	return feedTypeMap[item.FeedURL]
}

func (s *Scheduler) filterAttemptedRSSItems(
	items []status.UnsentRSSItem,
	destinationID string,
	attemptedRSSDeliveries rssDeliveryAttemptSet,
) []status.UnsentRSSItem {
	if len(items) == 0 || len(attemptedRSSDeliveries) == 0 {
		return items
	}

	filtered := items[:0]
	for _, item := range items {
		if _, attempted := attemptedRSSDeliveries[rssDeliveryAttemptKey(item.Key, destinationID)]; attempted {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func limitRSSEntriesForCycle(entries []model.RSSEntry, limit int) ([]model.RSSEntry, int) {
	if limit <= 0 || len(entries) <= limit {
		return entries, 0
	}
	return entries[:limit], len(entries) - limit
}

// rssEntrySortTime orders a fresh batch chronologically. A zero Published
// (undated) sorts as "now" rather than as the zero value's year-1 instant, so
// undated entries fall to the END of the batch instead of consuming the front
// of max_rss_entries_per_cycle ahead of entries with a real, older date.
// model.RSSEntry carries no ParsedAt (that is only stamped once the item is
// recorded, moments before this sort runs, and is ~identical for every entry
// in one cycle's fresh batch), so "now" is the only value already available
// to this batch that has the desired effect -- unlike storedRSSSortTime
// (status/tracker.go), which sorts entries accumulated across many past
// cycles and falls back to the row's real ParsedAt, so an old undated row
// sorts early there. The two rules coincide only because this batch's
// entries were all parsed moments earlier; this is a correct specialisation
// for a fresh batch, not the same rule, and the equivalence would break if
// this helper were ever reused for stored entries.
func rssEntrySortTime(entry model.RSSEntry, undatedSortTime time.Time) time.Time {
	if entry.Published.IsZero() {
		return undatedSortTime
	}
	return entry.Published
}

// rssSendMaxAgeForPublished: dated entries are clamped to the dedup horizon.
// The undated branch still returns the plain rss_max_item_age, but the value is
// only used for the log field max_age_minutes: rssEntryExceedsMaxAge
// short-circuits on a zero Published, so undated entries are exempt from the age
// gate. The branch is kept so both call sites stay symmetric. Two callers: the
// send path in sendRSSEntryToTarget and the recovery-batch predicate below; they
// must not diverge.
func rssSendMaxAgeForPublished(cfg *config.Config, published time.Time) time.Duration {
	if published.IsZero() {
		return cfg.RSSMaxItemAge
	}
	return effectiveRSSSendMaxAge(cfg.RSSMaxItemAge, cfg.StatusRetention.RSSParsedMaxAge)
}

func rssRecoveryItemIsStale(cfg *config.Config, item status.UnsentRSSItem) bool {
	return rssEntryExceedsMaxAge(item.Published, rssSendMaxAgeForPublished(cfg, item.Published))
}

// limitUnsentRSSItemsForCycle splits the per-cycle budget. Stale items are
// skipped without a webhook call and are deliberately not marked sent
// (readme.md: raising rss_max_item_age must still recover them), so they stay at
// the head of the oldest-first recovery list; counting them against the send
// budget starves every retry and quiet-hours resume until retention prunes them.
// Own, equally sized budget instead. The second return value keeps meaning
// "items in the unsent set this cycle did not look at", so
// unsent_count == send_batch_count + deferred_by_cap still holds.
func limitUnsentRSSItemsForCycle(
	entries []status.UnsentRSSItem,
	limit int,
	cfg *config.Config,
) ([]status.UnsentRSSItem, int) {
	if limit <= 0 || len(entries) <= limit {
		return entries, 0
	}
	if cfg == nil {
		return entries[:limit], len(entries) - limit
	}
	batch := make([]status.UnsentRSSItem, 0, 2*limit)
	sendable, stale, deferred := 0, 0, 0
	for _, entry := range entries {
		if rssRecoveryItemIsStale(cfg, entry) {
			if stale < limit {
				stale++
				batch = append(batch, entry)
				continue
			}
			deferred++
			continue
		}
		if sendable < limit {
			sendable++
			batch = append(batch, entry)
			continue
		}
		deferred++
	}
	return batch, deferred
}

func markRSSDeliveryAttemptsForEntries(
	entries []model.RSSEntry,
	destinationID string,
	attemptedRSSDeliveries rssDeliveryAttemptSet,
) {
	if len(entries) == 0 || attemptedRSSDeliveries == nil {
		return
	}
	for _, entry := range entries {
		attemptedRSSDeliveries[rssDeliveryAttemptKey(rss.GenerateEntryKeyForEntry(entry), destinationID)] = struct{}{}
	}
}

func (s *Scheduler) filterDeadLetteredRSSItems(
	items []status.UnsentRSSItem,
	messenger, destinationID, webhookURL string,
) []status.UnsentRSSItem {
	if len(items) == 0 {
		return items
	}

	filtered := items[:0]
	for _, item := range items {
		if s.statusTracker.IsRetryDeadLetteredForDestination(item.Key, destinationID, messenger, status.RetryItemTypeRSS.String(), webhookURL) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// sendRSSItemsToWebhook sends RSS items to a webhook (Discord or Slack) with Phase 2 tracking
func (s *Scheduler) sendRSSItemsToWebhook(
	ctx context.Context,
	items []status.UnsentRSSItem,
	webhookURL string,
	feedType string,
	messenger string,
	filters *filter.Rules,
	destinationIDs ...string,
) {
	target := webhookTarget{
		url:           webhookURL,
		destinationID: optionalDestinationID(webhookURL, destinationIDs...),
		messenger:     messenger,
		filters:       filters,
	}
	s.sendRSSItemsToWebhookWithConfig(
		ctx,
		s.getConfig(),
		items,
		feedType,
		target,
	)
}

func (s *Scheduler) sendRSSItemsToWebhookWithConfig(
	ctx context.Context,
	cfg *config.Config,
	items []status.UnsentRSSItem,
	feedType string,
	target webhookTarget,
) {
	if len(items) == 0 {
		log.Debug("No RSS items to send")
		return
	}
	webhookURL := target.url
	messenger := target.messenger
	destinationID := targetDestinationID(target)
	defer s.persistStatus("rss webhook delivery finished")

	log.WithFields(log.Fields{
		"total_items": len(items),
		"feed_type":   feedType,
		"messenger":   messenger,
	}).Info("Starting RSS items send to webhook")

	successCount := 0
	deadLetterCount := 0
	alreadySentCount := 0
	staleCount := 0
	filteredCount := 0
	failedCount := 0
	for i, storedItem := range items {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			log.WithFields(log.Fields{
				"sent":      successCount,
				"messenger": messenger,
			}).Warn("Context cancelled, stopping RSS send")
			return
		default:
		}

		if s.statusTracker.IsRetryDeadLetteredForDestination(storedItem.Key, destinationID, messenger, status.RetryItemTypeRSS.String(), webhookURL) {
			deadLetterCount++
			continue
		}

		rssEntry, err := storedItem.RSSEntry()
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"item_key":  storedItem.Key,
				"feed_url":  textutil.RedactURLCredentials(storedItem.FeedURL),
				"messenger": messenger,
			}).Error("Invalid RSS recovery item, moving recovery item to dead letter")
			s.statusTracker.MarkRetryDeadLetterForDestination(
				storedItem.Key,
				destinationID,
				messenger,
				status.RetryItemTypeRSS.String(),
				storedItem.Title,
				err.Error(),
			)
			s.statusTracker.RemoveFromRetryQueueByDestination(storedItem.Key, destinationID, webhookURL)
			s.persistStatus("rss recovery item dead-lettered")
			deadLetterCount++
			continue
		}

		switch s.sendRSSEntryToTarget(
			ctx,
			cfg,
			feedType,
			target,
			recoveryRSSDeliveryItem(storedItem, rssEntry),
			rssDeliveryModeRecovery,
			i,
			len(items),
			nil,
		) {
		case rssDeliveryOutcomeSent:
			successCount++
		case rssDeliveryOutcomeAlreadySent:
			alreadySentCount++
		case rssDeliveryOutcomeFiltered:
			filteredCount++
		case rssDeliveryOutcomeStale:
			staleCount++
		case rssDeliveryOutcomeFailed:
			failedCount++
		case rssDeliveryOutcomeCanceled:
			return
		}
	}

	log.WithFields(log.Fields{
		"total_sent":         successCount,
		"total_items":        len(items),
		"feed_type":          feedType,
		"messenger":          messenger,
		"dead_letter_count":  deadLetterCount,
		"already_sent_count": alreadySentCount,
		"stale_count":        staleCount,
		"filtered_count":     filteredCount,
		"failed_count":       failedCount,
	}).Info("RSS items send to webhook completed")
	if successCount == 0 {
		log.WithFields(log.Fields{
			"total_items":        len(items),
			"feed_type":          feedType,
			"messenger":          messenger,
			"dead_letter_count":  deadLetterCount,
			"already_sent_count": alreadySentCount,
			"stale_count":        staleCount,
			"filtered_count":     filteredCount,
			"failed_count":       failedCount,
		}).Info("No recoverable RSS items remained after recovery skips")
	}

	// Perform cleanup once after batch processing (more efficient than per-item cleanup)
	if successCount > 0 {
		s.statusTracker.CleanupOldEntriesForRSSDestinations(destinationIDsFromTargets(webhookTargetsForConfig(cfg, feedType)))
	}

	// Flush all pending changes to disk (batched instead of per-item)
	s.persistStatus("rss webhook delivery flushed")
}
