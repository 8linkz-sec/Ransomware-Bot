package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"encoding/json"

	"Ransomware-Bot/internal/api"
	"Ransomware-Bot/internal/config"
	"Ransomware-Bot/internal/discord"
	"Ransomware-Bot/internal/filter"
	"Ransomware-Bot/internal/rss"
	"Ransomware-Bot/internal/slack"
	"Ransomware-Bot/internal/status"

	log "github.com/sirupsen/logrus"
)

// Scheduler manages the execution of API polling and RSS feed checking
type Scheduler struct {
	config             *config.Config
	apiClient          *api.Client
	rssParser          *rss.Parser
	discordWebhookSender *discord.WebhookSender
	slackWebhookSender   *slack.WebhookSender
	statusTracker      *status.Tracker

	// Feed type lookup map for O(1) categorization
	feedTypeMap map[string]string

	// Channels for graceful shutdown
	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// Tickers for scheduled tasks
	apiTicker *time.Ticker
	rssTicker *time.Ticker

	// Guards against overlapping runs (only one check at a time per type)
	apiMu sync.Mutex
	rssMu sync.Mutex

	// Config hot-reload support
	configDir      string
	configMu       sync.RWMutex
	lastConfigTime time.Time

	// Dry-run mode: log messages instead of sending
	dryRun bool
}

// New creates a new scheduler instance
func New(cfg *config.Config, configDir string, dryRun bool) (*Scheduler, error) {
	// Initialize API client
	apiClient, err := api.NewClient(cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	// Initialize status tracker FIRST (uses configured data directory)
	statusTracker, err := status.NewTracker(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create status tracker: %w", err)
	}

	// Initialize RSS parser with status tracker for persistent deduplication
	rssParser := rss.NewParser(cfg.RSSRetryCount, cfg.RSSRetryDelay, cfg.RSSWorkerTimeout, statusTracker)

	// Initialize webhook senders
	discordWebhookSender, err := discord.NewWebhookSender()
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord webhook sender: %w", err)
	}
	slackWebhookSender := slack.NewWebhookSender()

	// Build feed type lookup map for O(1) categorization
	feedTypeMap := buildFeedTypeMap(cfg)

	// Get initial config file times
	lastConfigTime, _ := config.ConfigFileTimes(configDir)

	return &Scheduler{
		config:               cfg,
		apiClient:            apiClient,
		rssParser:            rssParser,
		discordWebhookSender: discordWebhookSender,
		slackWebhookSender:   slackWebhookSender,
		statusTracker:        statusTracker,
		feedTypeMap:          feedTypeMap,
		stopChan:             make(chan struct{}),
		configDir:            configDir,
		lastConfigTime:       lastConfigTime,
		dryRun:               dryRun,
	}, nil
}

// buildFeedTypeMap creates a lookup map for O(1) feed type categorization
func buildFeedTypeMap(cfg *config.Config) map[string]string {
	feedTypeMap := make(map[string]string)

	for _, url := range cfg.Feeds.GeneralFeeds {
		feedTypeMap[url] = "general"
	}
	for _, url := range cfg.Feeds.GovernmentFeeds {
		feedTypeMap[url] = "government"
	}
	for _, url := range cfg.Feeds.RansomwareFeeds {
		feedTypeMap[url] = "ransomware"
	}

	return feedTypeMap
}

// getConfig returns the current config with read-lock protection.
func (s *Scheduler) getConfig() *config.Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
}

// Start begins the scheduler operations
func (s *Scheduler) Start(ctx context.Context) error {
	log.Info("Starting scheduler")

	// Print status summary at startup
	log.Info("Starting scheduler - status tracker initialized")

	cfg := s.getConfig()

	// Create tickers for periodic tasks
	s.apiTicker = time.NewTicker(cfg.APIPollInterval)
	s.rssTicker = time.NewTicker(cfg.RSSPollInterval)

	// Start API polling goroutine
	s.wg.Add(1)
	go s.runAPIPoller(ctx)

	// Start RSS feed checking goroutine
	s.wg.Add(1)
	go s.runRSSChecker(ctx)

	// Start config watcher goroutine
	s.wg.Add(1)
	go s.runConfigWatcher(ctx)

	// Run initial checks immediately (tracked in WaitGroup for graceful shutdown)
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.checkAPIOnce(ctx)
	}()
	go func() {
		defer s.wg.Done()
		s.checkRSSOnce(ctx)
	}()

	log.Info("Scheduler started successfully")
	return nil
}

// RunOnce performs a single API check and RSS check, then returns.
// Used for dry-run/preview mode.
func (s *Scheduler) RunOnce(ctx context.Context) {
	log.Info("Running single cycle (dry-run mode)")
	s.checkAPIOnce(ctx)
	s.checkRSSOnce(ctx)
	log.Info("Single cycle completed")
}

// Stop gracefully shuts down the scheduler
func (s *Scheduler) Stop() {
	log.Info("Stopping scheduler")

	// Signal all goroutines to stop (safe against double-close)
	s.stopOnce.Do(func() { close(s.stopChan) })

	// Stop tickers
	if s.apiTicker != nil {
		s.apiTicker.Stop()
	}
	if s.rssTicker != nil {
		s.rssTicker.Stop()
	}

	// Wait for all goroutines to finish with timeout to prevent deadlock
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	graceful := false
	select {
	case <-done:
		log.Info("All goroutines stopped gracefully")
		graceful = true
	case <-time.After(30 * time.Second):
		log.Warn("Timeout waiting for goroutines to stop - forcing shutdown")
	}

	// Flush any pending tracker changes before shutdown (skip in dry-run to avoid persisting state)
	if !s.dryRun {
		s.statusTracker.SavePendingChanges()
	}

	if !graceful {
		// Wait a bit more for goroutines to notice closed channels before closing clients
		<-time.After(2 * time.Second)
	}

	// Close all clients to release resources
	if s.apiClient != nil {
		if err := s.apiClient.Close(); err != nil {
			log.WithError(err).Warn("Failed to close API client")
		}
	}
	if s.discordWebhookSender != nil {
		if err := s.discordWebhookSender.Close(); err != nil {
			log.WithError(err).Warn("Failed to close Discord webhook sender")
		}
	}
	if s.slackWebhookSender != nil {
		if err := s.slackWebhookSender.Close(); err != nil {
			log.WithError(err).Warn("Failed to close Slack webhook sender")
		}
	}

	log.Info("Scheduler stopped")
}

// runAPIPoller runs the API polling loop
func (s *Scheduler) runAPIPoller(ctx context.Context) {
	defer s.wg.Done()

	log.Info("API poller started")

	for {
		select {
		case <-ctx.Done():
			log.Info("API poller stopping due to context cancellation")
			return
		case <-s.stopChan:
			log.Info("API poller stopping due to stop signal")
			return
		case <-s.apiTicker.C:
			s.checkAPIOnce(ctx)
		}
	}
}

// runRSSChecker runs the RSS feed checking loop
func (s *Scheduler) runRSSChecker(ctx context.Context) {
	defer s.wg.Done()

	log.Info("RSS checker started")

	for {
		select {
		case <-ctx.Done():
			log.Info("RSS checker stopping due to context cancellation")
			return
		case <-s.stopChan:
			log.Info("RSS checker stopping due to stop signal")
			return
		case <-s.rssTicker.C:
			s.checkRSSOnce(ctx)
		}
	}
}

// runConfigWatcher polls config files for changes every 60 seconds.
func (s *Scheduler) runConfigWatcher(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	log.Info("Config watcher started")

	for {
		select {
		case <-ctx.Done():
			log.Info("Config watcher stopping due to context cancellation")
			return
		case <-s.stopChan:
			log.Info("Config watcher stopping due to stop signal")
			return
		case <-ticker.C:
			latest, err := config.ConfigFileTimes(s.configDir)
			if err != nil {
				log.WithError(err).Warn("Failed to check config file times")
				continue
			}
			s.configMu.RLock()
			needsReload := latest.After(s.lastConfigTime)
			s.configMu.RUnlock()

			if needsReload {
				log.Info("Config file change detected, reloading...")
				changed, err := s.ReloadConfig()
				if err != nil {
					log.WithError(err).Error("Config reload failed, keeping current config")
				} else if changed {
					s.configMu.Lock()
					s.lastConfigTime = latest
					s.configMu.Unlock()
					log.Info("Config reloaded successfully")
				}
			}
		}
	}
}

// ReloadConfig loads config from disk and applies safe-to-reload fields.
func (s *Scheduler) ReloadConfig() (bool, error) {
	newCfg, err := config.LoadConfig(s.configDir)
	if err != nil {
		return false, fmt.Errorf("config reload failed validation: %w", err)
	}

	s.configMu.Lock()
	oldCfg := s.config

	// Preserve non-reloadable fields from old config
	newCfg.DataDir = oldCfg.DataDir

	s.config = newCfg

	// Rebuild feed type map (must be inside configMu to avoid race with readers)
	s.feedTypeMap = buildFeedTypeMap(newCfg)
	s.configMu.Unlock()

	// Update tickers if intervals changed (nil-safe for calls before Start())
	if oldCfg.APIPollInterval != newCfg.APIPollInterval && s.apiTicker != nil {
		s.apiTicker.Reset(newCfg.APIPollInterval)
		log.WithField("interval", newCfg.APIPollInterval).Info("API poll interval updated")
	}
	if oldCfg.RSSPollInterval != newCfg.RSSPollInterval && s.rssTicker != nil {
		s.rssTicker.Reset(newCfg.RSSPollInterval)
		log.WithField("interval", newCfg.RSSPollInterval).Info("RSS poll interval updated")
	}

	// Update log level if changed
	if !strings.EqualFold(oldCfg.LogLevel, newCfg.LogLevel) {
		level, err := log.ParseLevel(newCfg.LogLevel)
		if err == nil {
			log.SetLevel(level)
			log.WithField("level", newCfg.LogLevel).Info("Log level updated")
		}
	}

	return true, nil
}

// checkAPIOnce performs a single API check with individual sending
func (s *Scheduler) checkAPIOnce(ctx context.Context) {
	// Prevent overlapping API runs – skip if a previous run is still in progress
	if !s.apiMu.TryLock() {
		log.Warn("Skipping API check: previous run still in progress")
		return
	}
	defer s.apiMu.Unlock()

	cfg := s.getConfig()

	if cfg.APIKey == "" {
		log.Debug("API key not configured, skipping API check")
		return
	}

	// Create a timeout context for this check (5 minutes max)
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	log.Debug("Checking API for new ransomware data")

	// Get all ransomware entries from API
	allEntries, err := s.apiClient.GetLatestEntries(checkCtx)
	if err != nil {
		log.WithError(err).Error("Failed to get API data")
		if !s.dryRun {
			s.statusTracker.UpdateAPIStatus(false, 0, err.Error())
		}
		return
	}

	// Update status with success
	if !s.dryRun {
		s.statusTracker.UpdateAPIStatus(true, len(allEntries), "")
	}

	// Send entries individually to Discord (filter by what's already sent to this webhook)
	if cfg.DiscordWebhooks.Ransomware.Enabled {
		if cfg.DiscordWebhooks.Ransomware.QuietHours.IsActive() {
			log.WithFields(log.Fields{
				"webhook": "discord.ransomware",
				"start":   cfg.DiscordWebhooks.Ransomware.QuietHours.Start,
				"end":     cfg.DiscordWebhooks.Ransomware.QuietHours.End,
			}).Info("Quiet hours active, skipping API send")
		} else {
			webhookURL := cfg.DiscordWebhooks.Ransomware.URL
			filters := cfg.DiscordWebhooks.Ransomware.Filters
			var unsent []api.RansomwareEntry
			filteredCount := 0
			for _, entry := range allEntries {
				key := api.GenerateEntryKey(entry)
				if s.statusTracker.IsAPIItemSentToWebhook(key, webhookURL) {
					continue
				}
				if !filter.MatchesAPIEntry(filters, entry) {
					filteredCount++
					log.WithFields(log.Fields{
						"group":   entry.Group,
						"country": entry.Country,
						"victim":  entry.Victim,
						"target":  "discord.ransomware",
					}).Debug("API entry filtered out by webhook rules")
					continue
				}
				unsent = append(unsent, entry)
			}

			log.WithFields(log.Fields{
				"webhook_enabled": true,
				"unsent_count":    len(unsent),
				"filtered_count":  filteredCount,
				"total_fetched":   len(allEntries),
			}).Debug("Checking Discord unsent items")

			if len(unsent) > 0 {
				// Sort oldest-first to preserve chronological order (API returns newest-first)
				sort.Slice(unsent, func(i, j int) bool {
					return unsent[i].Discovered.Before(unsent[j].Discovered.Time)
				})
				log.WithFields(log.Fields{
					"total_entries":  len(allEntries),
					"discord_unsent": len(unsent),
				}).Info("Found unsent API entries for Discord")
				s.bulkSendAPIEntriesToDiscord(checkCtx, unsent, webhookURL)
			} else {
				log.Debug("No unsent Discord items found")
			}
		}
	}

	// Send entries individually to Slack (filter by what's already sent to this webhook)
	if cfg.SlackWebhooks.Ransomware.Enabled {
		if cfg.SlackWebhooks.Ransomware.QuietHours.IsActive() {
			log.WithFields(log.Fields{
				"webhook": "slack.ransomware",
				"start":   cfg.SlackWebhooks.Ransomware.QuietHours.Start,
				"end":     cfg.SlackWebhooks.Ransomware.QuietHours.End,
			}).Info("Quiet hours active, skipping API send")
		} else {
			webhookURL := cfg.SlackWebhooks.Ransomware.URL
			filters := cfg.SlackWebhooks.Ransomware.Filters
			var unsent []api.RansomwareEntry
			filteredCount := 0
			for _, entry := range allEntries {
				key := api.GenerateEntryKey(entry)
				if s.statusTracker.IsAPIItemSentToWebhook(key, webhookURL) {
					continue
				}
				if !filter.MatchesAPIEntry(filters, entry) {
					filteredCount++
					log.WithFields(log.Fields{
						"group":   entry.Group,
						"country": entry.Country,
						"victim":  entry.Victim,
						"target":  "slack.ransomware",
					}).Debug("API entry filtered out by webhook rules")
					continue
				}
				unsent = append(unsent, entry)
			}

			log.WithFields(log.Fields{
				"webhook_enabled": true,
				"unsent_count":    len(unsent),
				"filtered_count":  filteredCount,
				"total_fetched":   len(allEntries),
			}).Debug("Checking Slack unsent items")

			if len(unsent) > 0 {
				// Sort oldest-first to preserve chronological order (API returns newest-first)
				sort.Slice(unsent, func(i, j int) bool {
					return unsent[i].Discovered.Before(unsent[j].Discovered.Time)
				})
				log.WithFields(log.Fields{
					"total_entries": len(allEntries),
					"slack_unsent":  len(unsent),
				}).Info("Found unsent API entries for Slack")
				s.bulkSendAPIEntriesToSlack(checkCtx, unsent, webhookURL)
			} else {
				log.Debug("No unsent Slack items found")
			}
		}
	}

	// Process API retry queue: resend items that have fallen out of the API window
	if !s.dryRun {
		s.processAPIRetryQueue(checkCtx, allEntries)
	}
}


// bulkSendAPIEntriesToDiscord sends API entries to Discord individually with rate limiting
func (s *Scheduler) bulkSendAPIEntriesToDiscord(ctx context.Context, entries []api.RansomwareEntry, webhookURL string) {
	if len(entries) == 0 {
		log.Debug("No entries to send")
		return
	}

	cfg := s.getConfig()
	log.WithField("total_entries", len(entries)).Info("Starting individual send to Discord")

	// Send each entry individually with configurable delay
	successCount := 0
	for _, entry := range entries {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			log.WithField("sent", successCount).Warn("Context cancelled, stopping Discord send")
			return
		default:
		}

		// Dry-run: log instead of sending
		if s.dryRun {
			log.WithFields(log.Fields{
				"group":  entry.Group,
				"victim": entry.Victim,
				"country": entry.Country,
				"mode":   "DRY-RUN",
				"target": "discord",
			}).Info("[DRY-RUN] Would send ransomware entry to Discord")
			successCount++
			continue
		}

		// Configurable delay before sending to Discord
		time.Sleep(cfg.DiscordDelay)
		// Send the entry to Discord webhook
		key := api.GenerateEntryKey(entry)
		title := entry.Group + " -> " + entry.Victim
		if err := s.discordWebhookSender.SendRansomwareEntry(ctx, webhookURL, entry, &cfg.Format); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"group":  entry.Group,
				"victim": entry.Victim,
			}).Error("Failed to send ransomware entry to webhook")
			entryJSON, _ := json.Marshal(entry)
			s.statusTracker.EnqueueRetry(key, webhookURL, "discord", "api", title, err.Error(), cfg.RetryMaxAttempts, cfg.RetryWindow, entryJSON)
		} else {
			s.statusTracker.MarkAPIItemSent(key, title, webhookURL)
			s.statusTracker.RemoveFromRetryQueue(key, webhookURL)
			successCount++
		}
	}

	log.WithFields(log.Fields{
		"total_sent":    successCount,
		"total_entries": len(entries),
	}).Info("Individual send to Discord completed")

	// Perform cleanup once after batch processing (more efficient than per-item cleanup)
	if successCount > 0 && !s.dryRun {
		s.statusTracker.CleanupOldEntries()
	}

	// Flush all pending changes to disk (batched instead of per-item)
	if !s.dryRun {
		s.statusTracker.SavePendingChanges()
	}
}

// bulkSendAPIEntriesToSlack sends API entries to Slack individually with rate limiting
func (s *Scheduler) bulkSendAPIEntriesToSlack(ctx context.Context, entries []api.RansomwareEntry, webhookURL string) {
	if len(entries) == 0 {
		log.Debug("No entries to send")
		return
	}

	cfg := s.getConfig()
	log.WithField("total_entries", len(entries)).Info("Starting individual send to Slack")

	// Send each entry individually with configurable delay
	successCount := 0
	for _, entry := range entries {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			log.WithField("sent", successCount).Warn("Context cancelled, stopping Slack send")
			return
		default:
		}

		// Dry-run: log instead of sending
		if s.dryRun {
			log.WithFields(log.Fields{
				"group":  entry.Group,
				"victim": entry.Victim,
				"country": entry.Country,
				"mode":   "DRY-RUN",
				"target": "slack",
			}).Info("[DRY-RUN] Would send ransomware entry to Slack")
			successCount++
			continue
		}

		// Configurable delay before sending to Slack
		time.Sleep(cfg.SlackDelay)
		// Send the entry to Slack webhook
		key := api.GenerateEntryKey(entry)
		title := entry.Group + " -> " + entry.Victim
		if err := s.slackWebhookSender.SendRansomwareEntry(ctx, webhookURL, entry, &cfg.Format); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"group":  entry.Group,
				"victim": entry.Victim,
			}).Error("Failed to send ransomware entry to Slack webhook")
			entryJSON, _ := json.Marshal(entry)
			s.statusTracker.EnqueueRetry(key, webhookURL, "slack", "api", title, err.Error(), cfg.RetryMaxAttempts, cfg.RetryWindow, entryJSON)
		} else {
			s.statusTracker.MarkAPIItemSent(key, title, webhookURL)
			s.statusTracker.RemoveFromRetryQueue(key, webhookURL)
			successCount++
		}
	}

	log.WithFields(log.Fields{
		"total_sent":    successCount,
		"total_entries": len(entries),
	}).Info("Individual send to Slack completed")

	// Perform cleanup once after batch processing (more efficient than per-item cleanup)
	if successCount > 0 && !s.dryRun {
		s.statusTracker.CleanupOldEntries()
	}

	// Flush all pending changes to disk (batched instead of per-item)
	if !s.dryRun {
		s.statusTracker.SavePendingChanges()
	}
}

// webhookTarget represents a single webhook destination for RSS items
type webhookTarget struct {
	url        string
	messenger  string // "discord" or "slack"
	filters    *config.WebhookFilters
	quietHours *config.QuietHours
}

// getWebhookTargets returns all enabled webhook targets for a given feed type
func (s *Scheduler) getWebhookTargets(feedType string) []webhookTarget {
	cfg := s.getConfig()
	var targets []webhookTarget

	switch feedType {
	case "general":
		if cfg.DiscordWebhooks.RSS.Enabled {
			targets = append(targets, webhookTarget{url: cfg.DiscordWebhooks.RSS.URL, messenger: "discord", filters: cfg.DiscordWebhooks.RSS.Filters, quietHours: cfg.DiscordWebhooks.RSS.QuietHours})
		}
		if cfg.SlackWebhooks.RSS.Enabled {
			targets = append(targets, webhookTarget{url: cfg.SlackWebhooks.RSS.URL, messenger: "slack", filters: cfg.SlackWebhooks.RSS.Filters, quietHours: cfg.SlackWebhooks.RSS.QuietHours})
		}
	case "government":
		if cfg.DiscordWebhooks.Government.Enabled {
			targets = append(targets, webhookTarget{url: cfg.DiscordWebhooks.Government.URL, messenger: "discord", filters: cfg.DiscordWebhooks.Government.Filters, quietHours: cfg.DiscordWebhooks.Government.QuietHours})
		}
		if cfg.SlackWebhooks.Government.Enabled {
			targets = append(targets, webhookTarget{url: cfg.SlackWebhooks.Government.URL, messenger: "slack", filters: cfg.SlackWebhooks.Government.Filters, quietHours: cfg.SlackWebhooks.Government.QuietHours})
		}
	case "ransomware":
		if cfg.DiscordWebhooks.Ransomware.Enabled {
			targets = append(targets, webhookTarget{url: cfg.DiscordWebhooks.Ransomware.URL, messenger: "discord", filters: cfg.DiscordWebhooks.Ransomware.Filters, quietHours: cfg.DiscordWebhooks.Ransomware.QuietHours})
		}
		if cfg.SlackWebhooks.Ransomware.Enabled {
			targets = append(targets, webhookTarget{url: cfg.SlackWebhooks.Ransomware.URL, messenger: "slack", filters: cfg.SlackWebhooks.Ransomware.Filters, quietHours: cfg.SlackWebhooks.Ransomware.QuietHours})
		}
	}

	return targets
}

// checkRSSOnce performs a single RSS feed check with parse-once pattern
// Each feed type is parsed exactly once, then results are sent to all enabled webhooks
func (s *Scheduler) checkRSSOnce(ctx context.Context) {
	// Prevent overlapping RSS runs – skip if a previous run is still in progress
	if !s.rssMu.TryLock() {
		log.Warn("Skipping RSS check: previous run still in progress")
		return
	}
	defer s.rssMu.Unlock()

	cfg := s.getConfig()

	// Create a timeout context for this check (10 minutes max for RSS due to multiple feeds)
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	log.Debug("Checking RSS feeds for new entries")

	// Phase 1: Parse each feed type exactly once (3 calls instead of 6)
	type feedBatch struct {
		urls     []string
		feedType string
	}
	batches := []feedBatch{
		{cfg.Feeds.GeneralFeeds, "general"},
		{cfg.Feeds.GovernmentFeeds, "government"},
		{cfg.Feeds.RansomwareFeeds, "ransomware"},
	}

	for _, batch := range batches {
		if len(batch.urls) == 0 {
			continue
		}

		// Check if any webhook is enabled for this feed type
		targets := s.getWebhookTargets(batch.feedType)
		if len(targets) == 0 {
			continue
		}

		feedResults, err := s.rssParser.ParseMultipleFeeds(checkCtx, batch.urls, cfg.MaxRSSWorkers)
		if err != nil {
			log.WithError(err).WithField("feed_type", batch.feedType).Error("Failed to parse RSS feeds")
		}
		if feedResults == nil {
			continue
		}

		// Persist newly parsed items before sending (crash safety)
		if !s.dryRun {
			s.statusTracker.SavePendingChanges()
		}

		// Phase 2: Send parsed results to all enabled webhooks for this feed type
		s.sendParsedRSSToWebhooks(checkCtx, feedResults, batch.feedType, targets)
	}

	// Phase 3: Recovery — send any items that were parsed but not yet sent (per-webhook)
	if !s.dryRun {
		s.sendUnsentRSSItems(checkCtx)
	}
}

// sendParsedRSSToWebhooks sends freshly parsed RSS results to all enabled webhook targets
func (s *Scheduler) sendParsedRSSToWebhooks(ctx context.Context, feedResults *rss.FeedResults, feedType string, targets []webhookTarget) {
	cfg := s.getConfig()

	// Collect and sort all entries chronologically
	var allEntries []rss.Entry
	for feedURL, entries := range feedResults.Entries {
		// Update feed status: success for feeds without errors, failure for feeds with errors
		if !s.dryRun {
			if errMsg, failed := feedResults.FeedErrors[feedURL]; failed {
				s.statusTracker.UpdateFeedStatus(feedURL, false, 0, errMsg)
			} else {
				s.statusTracker.UpdateFeedStatus(feedURL, true, len(entries), "")
			}
		}

		allEntries = append(allEntries, entries...)
	}

	if len(allEntries) == 0 {
		log.WithField("feed_type", feedType).Debug("No new RSS entries found")
		return
	}

	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].Published.Before(allEntries[j].Published)
	})

	log.WithFields(log.Fields{
		"feed_type":     feedType,
		"total_entries": len(allEntries),
		"targets":       len(targets),
	}).Info("Sending parsed RSS entries to webhooks")

	// Send each entry to each webhook target (per-webhook dedup)
	for _, target := range targets {
		// Skip this webhook if quiet hours are active
		if target.quietHours.IsActive() {
			log.WithFields(log.Fields{
				"messenger": target.messenger,
				"feed_type": feedType,
				"start":     target.quietHours.Start,
				"end":       target.quietHours.End,
			}).Info("Quiet hours active, skipping RSS send for this webhook")
			continue
		}
		successCount := 0
		filteredCount := 0
		for _, entry := range allEntries {
			// Check if context is cancelled
			select {
			case <-ctx.Done():
				log.WithField("messenger", target.messenger).Warn("Context cancelled, stopping RSS entry send")
				return
			default:
			}

			// Per-webhook dedup: skip if already sent to this specific webhook
			key := rss.GenerateEntryKey(entry.FeedURL, entry.GUID, entry.Link, entry.Title)
			contentSig := rss.GenerateContentSignature(entry.FeedURL, entry.Title, entry.Published)
			if s.statusTracker.IsRSSItemSentToWebhook(key, target.url) || s.statusTracker.IsRSSItemSentToWebhook(contentSig, target.url) {
				continue
			}

			// Per-webhook content filter: skip if entry doesn't match filter rules
			if !filter.MatchesRSSEntry(target.filters, entry) {
				filteredCount++
				log.WithFields(log.Fields{
					"title":     entry.Title,
					"feed_url":  entry.FeedURL,
					"messenger": target.messenger,
				}).Debug("RSS entry filtered out by webhook rules")
				continue
			}

			// Freshness filter: skip items older than configured max age (if enabled)
			if cfg.RSSMaxItemAge > 0 && !entry.Published.IsZero() && time.Since(entry.Published) > cfg.RSSMaxItemAge {
				log.WithFields(log.Fields{
					"title":     entry.Title,
					"published": entry.Published.Format("2006-01-02 15:04:05"),
					"age":       time.Since(entry.Published).Round(time.Minute),
					"max_age":   cfg.RSSMaxItemAge,
				}).Info("Skipping stale RSS entry, marking as sent")
				if !s.dryRun {
					// Mark as sent so it doesn't reappear in future polls
					s.statusTracker.MarkRSSItemSentToWebhook(key, entry.Title, entry.FeedTitle, target.url)
					s.statusTracker.MarkRSSItemSentToWebhook(contentSig, entry.Title, entry.FeedTitle, target.url)
				}
				continue
			}

			// Dry-run: log instead of sending
			if s.dryRun {
				log.WithFields(log.Fields{
					"title":      entry.Title,
					"link":       entry.Link,
					"feed_title": entry.FeedTitle,
					"mode":       "DRY-RUN",
					"target":     target.messenger,
				}).Info("[DRY-RUN] Would send RSS entry to webhook")
				successCount++
				continue
			}

			var err error
			switch target.messenger {
			case "discord":
				time.Sleep(cfg.DiscordDelay)
				err = s.discordWebhookSender.SendRSSEntry(ctx, target.url, entry)
			case "slack":
				time.Sleep(cfg.SlackDelay)
				err = s.slackWebhookSender.SendRSSEntry(ctx, target.url, entry, &cfg.Format)
			}

			if err != nil {
				log.WithError(err).WithFields(log.Fields{
					"feed_url":  entry.FeedURL,
					"messenger": target.messenger,
				}).Error("Failed to send RSS entry to webhook")
				s.statusTracker.EnqueueRetry(key, target.url, target.messenger, "rss", entry.Title, err.Error(), cfg.RetryMaxAttempts, cfg.RetryWindow)
			} else {
				s.statusTracker.MarkRSSItemSentToWebhook(key, entry.Title, entry.FeedTitle, target.url)
				s.statusTracker.MarkRSSItemSentToWebhook(contentSig, entry.Title, entry.FeedTitle, target.url)
				s.statusTracker.RemoveFromRetryQueue(key, target.url)
				successCount++
			}
		}

		log.WithFields(log.Fields{
			"total_sent":     successCount,
			"filtered_count": filteredCount,
			"total_items":    len(allEntries),
			"feed_type":      feedType,
			"messenger":      target.messenger,
		}).Info("RSS entries send to webhook completed")
	}

	if !s.dryRun {
		// Perform cleanup once after batch processing
		s.statusTracker.CleanupOldEntries()

		// Flush all pending changes to disk (batched instead of per-item)
		s.statusTracker.SavePendingChanges()
	}
}

// sendUnsentRSSItems sends all RSS items that were parsed but not yet sent (per-webhook recovery)
func (s *Scheduler) sendUnsentRSSItems(ctx context.Context) {
	// Check if context is cancelled before starting
	select {
	case <-ctx.Done():
		log.Debug("RSS unsent items sending cancelled due to context")
		return
	default:
	}

	// Iterate over all feed types and their webhook targets
	feedTypes := []string{"general", "government", "ransomware"}

	for _, feedType := range feedTypes {
		targets := s.getWebhookTargets(feedType)
		for _, target := range targets {
			// Skip this webhook if quiet hours are active
			if target.quietHours.IsActive() {
				log.WithFields(log.Fields{
					"messenger": target.messenger,
					"feed_type": feedType,
					"start":     target.quietHours.Start,
					"end":       target.quietHours.End,
				}).Info("Quiet hours active, skipping unsent RSS recovery for this webhook")
				continue
			}

			// Get unsent items specifically for this webhook
			unsentItems := s.statusTracker.GetUnsentRSSItemsForWebhook(target.url)
			if len(unsentItems) == 0 {
				continue
			}

			// Filter items by feed type using feedTypeMap (read under configMu)
			s.configMu.RLock()
			var filteredItems []status.StoredRSSEntry
			for _, item := range unsentItems {
				if s.feedTypeMap[item.FeedURL] == feedType {
					filteredItems = append(filteredItems, item)
				}
			}
			s.configMu.RUnlock()

			if len(filteredItems) == 0 {
				continue
			}

			log.WithFields(log.Fields{
				"unsent_count": len(filteredItems),
				"feed_type":    feedType,
				"messenger":    target.messenger,
			}).Info("Found unsent RSS items for webhook, sending")

			s.sendRSSItemsToWebhook(ctx, filteredItems, target.url, feedType, target.messenger, target.filters)
		}
	}
}

// sendRSSItemsToWebhook sends RSS items to a webhook (Discord or Slack) with Phase 2 tracking
func (s *Scheduler) sendRSSItemsToWebhook(ctx context.Context, items []status.StoredRSSEntry, webhookURL string, feedType string, messenger string, filters *config.WebhookFilters) {
	if len(items) == 0 {
		log.Debug("No RSS items to send")
		return
	}

	cfg := s.getConfig()

	// Sort oldest-first to preserve chronological order
	sort.Slice(items, func(i, j int) bool {
		ti, _ := time.Parse("2006-01-02 15:04:05.999999", items[i].Published)
		tj, _ := time.Parse("2006-01-02 15:04:05.999999", items[j].Published)
		return ti.Before(tj)
	})

	log.WithFields(log.Fields{
		"total_items": len(items),
		"feed_type":   feedType,
		"messenger":   messenger,
	}).Info("Starting RSS items send to webhook")

	successCount := 0
	for _, storedItem := range items {
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

		// Convert stored RSS entry back to rss.Entry for sending
		rssEntry := s.convertStoredToRSSEntry(storedItem)

		// Per-webhook dedup: skip if already sent via primary key or content signature
		contentSig := rss.GenerateContentSignature(storedItem.FeedURL, storedItem.Title, rssEntry.Published)
		if s.statusTracker.IsRSSItemSentToWebhook(storedItem.Key, webhookURL) || s.statusTracker.IsRSSItemSentToWebhook(contentSig, webhookURL) {
			continue
		}

		// Freshness filter: skip items older than configured max age (if enabled)
		if cfg.RSSMaxItemAge > 0 && !rssEntry.Published.IsZero() && time.Since(rssEntry.Published) > cfg.RSSMaxItemAge {
			log.WithFields(log.Fields{
				"title":     storedItem.Title,
				"published": rssEntry.Published.Format("2006-01-02 15:04:05"),
				"age":       time.Since(rssEntry.Published).Round(time.Minute),
				"max_age":   cfg.RSSMaxItemAge,
			}).Info("Skipping stale RSS entry in recovery, marking as sent")
			s.statusTracker.MarkRSSItemSentToWebhook(storedItem.Key, storedItem.Title, storedItem.FeedTitle, webhookURL)
			contentSig := rss.GenerateContentSignature(storedItem.FeedURL, storedItem.Title, rssEntry.Published)
			s.statusTracker.MarkRSSItemSentToWebhook(contentSig, storedItem.Title, storedItem.FeedTitle, webhookURL)
			continue
		}

		// Per-webhook content filter: skip if entry doesn't match filter rules
		if !filter.MatchesRSSEntry(filters, rssEntry) {
			log.WithFields(log.Fields{
				"title":     storedItem.Title,
				"feed_url":  storedItem.FeedURL,
				"messenger": messenger,
			}).Debug("RSS recovery entry filtered out by webhook rules")
			continue
		}

		var err error

		switch messenger {
		case "discord":
			// Configurable delay before sending to Discord
			time.Sleep(cfg.DiscordDelay)
			err = s.discordWebhookSender.SendRSSEntry(ctx, webhookURL, rssEntry)
		case "slack":
			// Configurable delay before sending to Slack
			time.Sleep(cfg.SlackDelay)
			err = s.slackWebhookSender.SendRSSEntry(ctx, webhookURL, rssEntry, &cfg.Format)
		}

		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"title":     storedItem.Title,
				"feed_url":  storedItem.FeedURL,
				"messenger": messenger,
			}).Error("Failed to send RSS entry to webhook")
			s.statusTracker.EnqueueRetry(storedItem.Key, webhookURL, messenger, "rss", storedItem.Title, err.Error(), cfg.RetryMaxAttempts, cfg.RetryWindow)
		} else {
			// Mark as sent after successful transmission (Phase 2)
			s.statusTracker.MarkRSSItemSentToWebhook(storedItem.Key, storedItem.Title, storedItem.FeedTitle, webhookURL)
			// Also mark content signature to prevent link-variant duplicates
			contentSig := rss.GenerateContentSignature(storedItem.FeedURL, storedItem.Title, rssEntry.Published)
			s.statusTracker.MarkRSSItemSentToWebhook(contentSig, storedItem.Title, storedItem.FeedTitle, webhookURL)
			s.statusTracker.RemoveFromRetryQueue(storedItem.Key, webhookURL)
			successCount++
		}
	}

	log.WithFields(log.Fields{
		"total_sent":  successCount,
		"total_items": len(items),
		"feed_type":   feedType,
		"messenger":   messenger,
	}).Info("RSS items send to webhook completed")

	// Perform cleanup once after batch processing (more efficient than per-item cleanup)
	if successCount > 0 {
		s.statusTracker.CleanupOldEntries()
	}

	// Flush all pending changes to disk (batched instead of per-item)
	s.statusTracker.SavePendingChanges()
}

// convertStoredToRSSEntry converts a StoredRSSEntry back to rss.Entry for Discord sending
func (s *Scheduler) convertStoredToRSSEntry(stored status.StoredRSSEntry) rss.Entry {
	// Parse published time from stored string
	published, err := time.Parse("2006-01-02 15:04:05.999999", stored.Published)
	if err != nil {
		// Fallback to simpler format if parsing fails
		published, _ = time.Parse("2006-01-02 15:04:05", stored.Published)
	}

	return rss.Entry{
		Title:       stored.Title,
		Link:        stored.Link,
		Description: stored.Description,
		Published:   published,
		Author:      stored.Author,
		Categories:  stored.Categories,
		GUID:        stored.GUID,
		FeedTitle:   stored.FeedTitle,
		FeedURL:     stored.FeedURL,
	}
}

// processAPIRetryQueue consumes API retry items that are no longer in the current
// API response. Items still in allEntries are retried naturally by the normal send
// loop; this method handles items that have fallen out of the API window.
func (s *Scheduler) processAPIRetryQueue(ctx context.Context, allEntries []api.RansomwareEntry) {
	cfg := s.getConfig()

	retryItems := s.statusTracker.GetRetryItemsByType("api")
	if len(retryItems) == 0 {
		return
	}

	// Build lookup map: itemKey → entry from current API response
	entryMap := make(map[string]api.RansomwareEntry, len(allEntries))
	for _, entry := range allEntries {
		entryMap[api.GenerateEntryKey(entry)] = entry
	}

	// Determine webhook URLs from config (WebhookURL is not persisted for security)
	type webhookInfo struct {
		url        string
		enabled    bool
		quietHours *config.QuietHours
		filters    *config.WebhookFilters
	}
	messengerWebhooks := map[string]webhookInfo{
		"discord": {
			url:        cfg.DiscordWebhooks.Ransomware.URL,
			enabled:    cfg.DiscordWebhooks.Ransomware.Enabled,
			quietHours: cfg.DiscordWebhooks.Ransomware.QuietHours,
			filters:    cfg.DiscordWebhooks.Ransomware.Filters,
		},
		"slack": {
			url:        cfg.SlackWebhooks.Ransomware.URL,
			enabled:    cfg.SlackWebhooks.Ransomware.Enabled,
			quietHours: cfg.SlackWebhooks.Ransomware.QuietHours,
			filters:    cfg.SlackWebhooks.Ransomware.Filters,
		},
	}

	retried := 0
	for _, item := range retryItems {
		select {
		case <-ctx.Done():
			return
		default:
		}

		wh, ok := messengerWebhooks[item.Messenger]
		if !ok || !wh.enabled || wh.url == "" {
			continue
		}
		if wh.quietHours.IsActive() {
			continue
		}

		// If already sent (e.g. by the normal send loop this cycle), just clean up
		if s.statusTracker.IsAPIItemSentToWebhook(item.ItemKey, wh.url) {
			s.statusTracker.RemoveFromRetryQueue(item.ItemKey, wh.url)
			continue
		}

		// If entry is still in API window, the normal send loop handles it — skip
		if _, inWindow := entryMap[item.ItemKey]; inWindow {
			continue
		}

		// Entry has fallen out of API window — try to reconstruct from stored payload
		if item.Payload == nil {
			log.WithFields(log.Fields{
				"item_key":    item.ItemKey,
				"title":       item.Title,
				"messenger":   item.Messenger,
				"retry_count": item.RetryCount,
			}).Warn("API retry item has no payload and is no longer in API response, will dead-letter when limits exceeded")
			continue
		}

		var entry api.RansomwareEntry
		if err := json.Unmarshal(item.Payload, &entry); err != nil {
			log.WithError(err).WithField("item_key", item.ItemKey).Error("Failed to deserialize retry payload")
			continue
		}

		// Re-check filter (config may have changed since first attempt)
		if !filter.MatchesAPIEntry(wh.filters, entry) {
			log.WithFields(log.Fields{
				"item_key":  item.ItemKey,
				"title":     item.Title,
				"messenger": item.Messenger,
			}).Debug("API retry item filtered out by current webhook rules, removing from queue")
			s.statusTracker.RemoveFromRetryQueue(item.ItemKey, wh.url)
			continue
		}

		// Attempt re-send
		key := api.GenerateEntryKey(entry)
		title := entry.Group + " -> " + entry.Victim
		var err error
		switch item.Messenger {
		case "discord":
			time.Sleep(cfg.DiscordDelay)
			err = s.discordWebhookSender.SendRansomwareEntry(ctx, wh.url, entry, &cfg.Format)
		case "slack":
			time.Sleep(cfg.SlackDelay)
			err = s.slackWebhookSender.SendRansomwareEntry(ctx, wh.url, entry, &cfg.Format)
		}

		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"item_key":    key,
				"title":       title,
				"messenger":   item.Messenger,
				"retry_count": item.RetryCount,
			}).Warn("API retry send failed")
			// Re-enqueue bumps retry count; payload is preserved from first enqueue
			s.statusTracker.EnqueueRetry(key, wh.url, item.Messenger, "api", title, err.Error(), cfg.RetryMaxAttempts, cfg.RetryWindow)
		} else {
			s.statusTracker.MarkAPIItemSent(key, title, wh.url)
			s.statusTracker.RemoveFromRetryQueue(key, wh.url)
			retried++
			log.WithFields(log.Fields{
				"item_key":  key,
				"title":     title,
				"messenger": item.Messenger,
			}).Info("API retry send succeeded")
		}
	}

	if retried > 0 {
		s.statusTracker.SavePendingChanges()
	}
}
