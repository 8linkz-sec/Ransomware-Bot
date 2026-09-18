// Package config provides configuration management for Discord and Slack webhook
// delivery with validation, defaults, and multi-file JSON support.
//
// Configuration Structure:
// - config_general.json: Core settings, API keys, webhooks, logging
// - config_feeds.json: RSS feed URLs organized by category
// - config_format.json: Message formatting and display options
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/discordurl"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/feedurl"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/keywordmode"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
	"golang.org/x/text/language"
)

const (
	configGeneralFile       = "config_general.json"
	configFeedsFile         = "config_feeds.json"
	configFormatFile        = "config_format.json"
	maxWebhookFilterValues  = 50
	maxFilterFieldNameRunes = 64
	maxFilterValueRunes     = 128
	maxKeywordValueRunes    = 256
	maxRegexPatternRunes    = 512
	minDescriptionMaxChars  = 50
	maxDescriptionMaxChars  = 3000
	maxFormatLabels         = 100
	maxFormatLabelRunes     = 120

	minLogRotationSizeMB       = 1
	maxLogRotationSizeMB       = 1000
	minLogRotationBackups      = 0
	maxLogRotationBackups      = 50
	minLogRotationAgeDays      = 0
	maxLogRotationAgeDays      = 365
	logRotationDiskWarningMB   = 5000
	minAPIMaxRetries           = 1
	maxAPIMaxRetries           = 10
	minWebhookMaxRetries       = 1
	maxWebhookMaxRetries       = 10
	minRSSRetryCount           = 0
	maxRSSRetryCount           = 10
	minRSSWorkers              = 1
	maxRSSWorkers              = 10
	minEntriesPerCycle         = 1
	maxEntriesPerCycle         = 1000
	minRetryMaxAttempts        = 0
	maxRetryMaxAttempts        = 100
	discordFieldOrderMaxItems  = 25
	slackFieldOrderMaxItems    = 50
	discordEmbedDescriptionMax = 1024
	isoCountryCodeLength       = 2
	minStatusRetentionItems    = 1
	maxStatusRetentionItems    = 1000000
)

const (
	WebhookPlatformDiscord         = "discord"
	WebhookPlatformSlack           = "slack"
	WebhookPlatformSlackCompatible = "slack_compatible"

	WebhookTypeRansomware = "ransomware"
	WebhookTypeRSS        = "rss"
	WebhookTypeGovernment = "government"
)

const (
	minPollInterval       = time.Minute
	minCheckTimeout       = time.Minute
	minAPIRequestTimeout  = time.Second
	maxAPIRequestTimeout  = 5 * time.Minute
	minAPIRetryDelay      = 100 * time.Millisecond
	maxAPIRetryDelay      = time.Minute
	minWebhookTimeout     = time.Second
	maxWebhookTimeout     = 5 * time.Minute
	minWebhookRetryDelay  = 100 * time.Millisecond
	maxWebhookRetryDelay  = time.Minute
	minRSSRetryDelay      = time.Second
	minDiscordDelay       = 400 * time.Millisecond
	minSlackDelay         = time.Second
	maxWebhookDelay       = 30 * time.Second
	minRSSWorkerTimeout   = 5 * time.Second
	maxRSSWorkerTimeout   = 5 * time.Minute
	minStatusRetentionAge = time.Hour
	maxStatusRetentionAge = 5 * 365 * 24 * time.Hour
)

type configFileSpec struct {
	name     string
	label    string
	required bool
	load     func(*Config, string, bool) error
}

var configFileRegistry = []configFileSpec{
	{
		name:     configGeneralFile,
		label:    "general",
		required: true,
		load: func(cfg *Config, configDir string, _ bool) error {
			return loadGeneralConfig(cfg, configDir)
		},
	},
	{
		name:     configFeedsFile,
		label:    "feeds",
		required: false,
		load:     loadFeedsConfigWithMode,
	},
	{
		name:     configFormatFile,
		label:    "format",
		required: false,
		load:     loadFormatConfigWithMode,
	},
}

// LoadConfig loads and validates all configuration files from the specified directory
//
// Loading strategy:
// 1. Start with sensible defaults from DefaultConfig()
// 2. Override with values from config_general.json (required)
// 3. Merge optional config_feeds.json and config_format.json
// 4. Validate all settings for security and operational requirements
//
// This approach ensures the bot can start even with minimal configuration
// while providing extensive customization options for advanced users.
func LoadConfig(configDir string) (*Config, error) {
	return loadConfig(configDir, false)
}

func LoadConfigStrict(configDir string) (*Config, error) {
	return loadConfig(configDir, true)
}

func loadConfig(configDir string, strictOptional bool) (*Config, error) {
	// Start with default configuration
	cfg := DefaultConfig()

	for _, spec := range configFileRegistry {
		if err := spec.load(cfg, configDir, strictOptional); err != nil {
			return nil, fmt.Errorf("failed to load %s config: %w", spec.label, err)
		}
	}

	// Validate configuration
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	log.Info("Configuration loaded successfully")
	return cfg, nil
}

// loadGeneralConfig loads the main configuration file
func loadGeneralConfig(cfg *Config, configDir string) error {
	configPath, data, _, err := readConfigFile(configDir, configGeneralFile, true)
	if err != nil {
		return err
	}

	var generalCfg generalConfig
	if err := decodeStrictJSON(data, &generalCfg); err != nil {
		return fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	applyGeneralScalarOverrides(cfg, generalCfg)
	applyGeneralWebhookOverrides(cfg, generalCfg)
	applyGeneralLogRotationOverrides(cfg, generalCfg.LogRotation)
	if err := applyGeneralDurationOverrides(cfg, generalCfg); err != nil {
		return err
	}
	if err := applyGeneralRetryOverrides(cfg, generalCfg); err != nil {
		return err
	}
	applyStatusRetentionOverrides(&cfg.StatusRetention, generalCfg.StatusRetention)
	return applyGeneralDataDirOverride(cfg, generalCfg.DataDir)
}

func applyGeneralScalarOverrides(cfg *Config, generalCfg generalConfig) {
	if generalCfg.LogLevel != "" {
		cfg.LogLevel = generalCfg.LogLevel
	}
	if generalCfg.LogFilePath != "" {
		cfg.LogFilePath = generalCfg.LogFilePath
	}
	if generalCfg.MaxRSSWorkers != nil {
		cfg.MaxRSSWorkers = *generalCfg.MaxRSSWorkers
	}
	if generalCfg.APIMaxEntriesPerCycle != nil {
		cfg.APIMaxEntriesPerCycle = *generalCfg.APIMaxEntriesPerCycle
	}
	if generalCfg.RSSMaxEntriesPerCycle != nil {
		cfg.RSSMaxEntriesPerCycle = *generalCfg.RSSMaxEntriesPerCycle
	}
	cfg.APIKey = generalCfg.APIKey
	if generalCfg.APIBaseURL != "" {
		cfg.APIBaseURL = generalCfg.APIBaseURL
	}
	if generalCfg.APIMaxRetries != nil {
		cfg.APIMaxRetries = *generalCfg.APIMaxRetries
	}
	if generalCfg.RSSRetryCount != nil {
		cfg.RSSRetryCount = *generalCfg.RSSRetryCount
	}
	if generalCfg.WebhookMaxRetries != nil {
		cfg.WebhookMaxRetries = *generalCfg.WebhookMaxRetries
	}
}

func applyGeneralWebhookOverrides(cfg *Config, generalCfg generalConfig) {
	cfg.DiscordWebhooks = generalCfg.DiscordWebhooks
	cfg.SlackWebhooks = generalCfg.SlackWebhooks
	cfg.SlackCompatibleWebhookHosts = normalizeAllowedWebhookHosts(generalCfg.SlackCompatibleWebhookHosts)
	cfg.SlackCompatibleWebhooks = generalCfg.SlackCompatibleWebhooks
}

func applyGeneralLogRotationOverrides(cfg *Config, raw generalLogRotation) {
	if raw.MaxSizeMB != nil {
		cfg.LogRotation.MaxSizeMB = *raw.MaxSizeMB
	}
	if raw.MaxBackups != nil {
		cfg.LogRotation.MaxBackups = *raw.MaxBackups
	}
	if raw.MaxAgeDays != nil {
		cfg.LogRotation.MaxAgeDays = *raw.MaxAgeDays
	}
	if raw.Compress != nil {
		cfg.LogRotation.Compress = *raw.Compress
	}
}

func applyGeneralDurationOverrides(cfg *Config, generalCfg generalConfig) error {
	durationOverrides := []durationOverride{
		{name: "api_request_timeout", raw: generalCfg.APIRequestTimeout, target: &cfg.APIRequestTimeout},
		{name: "api_retry_delay", raw: generalCfg.APIRetryDelay, target: &cfg.APIRetryDelay},
		{name: "webhook_request_timeout", raw: generalCfg.WebhookRequestTimeout, target: &cfg.WebhookRequestTimeout},
		{name: "webhook_retry_delay", raw: generalCfg.WebhookRetryDelay, target: &cfg.WebhookRetryDelay},
		{name: "api_poll_interval", raw: generalCfg.APIPollInterval, target: &cfg.APIPollInterval},
		{name: "api_check_timeout", raw: generalCfg.APICheckTimeout, target: &cfg.APICheckTimeout},
		{name: "rss_poll_interval", raw: generalCfg.RSSPollInterval, target: &cfg.RSSPollInterval},
		{name: "rss_check_timeout", raw: generalCfg.RSSCheckTimeout, target: &cfg.RSSCheckTimeout},
		{name: "rss_retry_delay", raw: generalCfg.RSSRetryDelay, target: &cfg.RSSRetryDelay},
		{name: "discord_delay", raw: generalCfg.DiscordDelay, target: &cfg.DiscordDelay},
		{name: "slack_delay", raw: generalCfg.SlackDelay, target: &cfg.SlackDelay},
		{name: "rss_worker_timeout", raw: generalCfg.RSSWorkerTimeout, target: &cfg.RSSWorkerTimeout},
		{name: "rss_max_item_age", raw: generalCfg.RSSMaxItemAge, target: &cfg.RSSMaxItemAge},
		{name: "status_retention.rss_parsed_max_age", raw: generalCfg.StatusRetention.RSSParsedMaxAge, target: &cfg.StatusRetention.RSSParsedMaxAge},
		{name: "status_retention.retry_queue_max_age", raw: generalCfg.StatusRetention.RetryQueueMaxAge, target: &cfg.StatusRetention.RetryQueueMaxAge},
		{name: "status_retention.dead_letter_max_age", raw: generalCfg.StatusRetention.DeadLetterMaxAge, target: &cfg.StatusRetention.DeadLetterMaxAge},
	}
	for _, override := range durationOverrides {
		if err := applyDurationOverride(override); err != nil {
			return err
		}
	}
	return nil
}

func applyGeneralRetryOverrides(cfg *Config, generalCfg generalConfig) error {
	if generalCfg.RetryMaxAttempts != nil {
		cfg.RetryMaxAttempts = *generalCfg.RetryMaxAttempts
	}
	retryWindowOverride := durationOverride{
		name:   "retry_window",
		raw:    generalCfg.RetryWindow,
		target: &cfg.RetryWindow,
	}
	if err := applyDurationOverride(retryWindowOverride); err != nil {
		return err
	}
	return nil
}

func applyGeneralDataDirOverride(cfg *Config, rawDataDir string) error {
	if rawDataDir != "" {
		cfg.DataDir = rawDataDir
	}

	absDataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("failed to resolve data_dir to absolute path: %w", err)
	}
	cfg.DataDir = absDataDir

	return nil
}

func applyStatusRetentionOverrides(cfg *StatusRetentionConfig, raw generalStatusRetention) {
	if raw.MaxAPISentItems != nil {
		cfg.MaxAPISentItems = *raw.MaxAPISentItems
	}
	if raw.MaxRSSParsedItems != nil {
		cfg.MaxRSSParsedItems = *raw.MaxRSSParsedItems
	}
	if raw.MaxRSSSentItems != nil {
		cfg.MaxRSSSentItems = *raw.MaxRSSSentItems
	}
	if raw.MaxRetryQueueItems != nil {
		cfg.MaxRetryQueueItems = *raw.MaxRetryQueueItems
	}
	if raw.MaxDeadLetterItems != nil {
		cfg.MaxDeadLetterItems = *raw.MaxDeadLetterItems
	}
	if raw.AuditLogRotation.MaxSizeMB != nil {
		cfg.AuditLogRotation.MaxSizeMB = *raw.AuditLogRotation.MaxSizeMB
	}
	if raw.AuditLogRotation.MaxBackups != nil {
		cfg.AuditLogRotation.MaxBackups = *raw.AuditLogRotation.MaxBackups
	}
	if raw.AuditLogRotation.MaxAgeDays != nil {
		cfg.AuditLogRotation.MaxAgeDays = *raw.AuditLogRotation.MaxAgeDays
	}
	if raw.AuditLogRotation.Compress != nil {
		cfg.AuditLogRotation.Compress = *raw.AuditLogRotation.Compress
	}
}

type durationOverride struct {
	name   string
	raw    string
	target *time.Duration
}

func applyDurationOverride(override durationOverride) error {
	if override.raw == "" {
		return nil
	}
	duration, err := time.ParseDuration(override.raw)
	if err != nil {
		return fmt.Errorf("invalid %s format: %w", override.name, err)
	}
	*override.target = duration
	return nil
}

// loadFeedsConfig loads the RSS feeds configuration
func loadFeedsConfig(cfg *Config, configDir string) error {
	return loadFeedsConfigWithMode(cfg, configDir, true)
}

func loadFeedsConfigWithMode(cfg *Config, configDir string, strict bool) error {
	configPath, data, exists, err := readConfigFile(configDir, configFeedsFile, false)
	if err != nil {
		return err
	}
	if !exists {
		log.WithField("path", configPath).Warn("Feeds config file not found, using defaults")
		return nil
	}

	if err := decodeStrictJSON(data, &cfg.Feeds); err != nil {
		if !strict {
			log.WithError(err).WithField("path", configPath).Warn("Feeds config invalid, disabling RSS feeds")
			cfg.Feeds = emptyFeedConfig()
			return nil
		}
		return fmt.Errorf("failed to parse feeds config file %s: %w", configPath, err)
	}

	// Ensure nil slices from JSON are replaced with empty slices
	if cfg.Feeds.RansomwareFeeds == nil {
		cfg.Feeds.RansomwareFeeds = []string{}
	}
	if cfg.Feeds.GovernmentFeeds == nil {
		cfg.Feeds.GovernmentFeeds = []string{}
	}
	if cfg.Feeds.GeneralFeeds == nil {
		cfg.Feeds.GeneralFeeds = []string{}
	}

	// Validate RSS feed URLs
	if err := validateFeedURLs(cfg); err != nil {
		if !strict {
			log.WithError(err).WithField("path", configPath).Warn("Feeds config failed validation, disabling RSS feeds")
			cfg.Feeds = emptyFeedConfig()
			return nil
		}
		return err
	}

	return nil
}

func emptyFeedConfig() FeedConfig {
	return FeedConfig{
		RansomwareFeeds: []string{},
		GovernmentFeeds: []string{},
		GeneralFeeds:    []string{},
	}
}

// loadFormatConfig loads the message formatting configuration
func loadFormatConfig(cfg *Config, configDir string) error {
	return loadFormatConfigWithMode(cfg, configDir, true)
}

func loadFormatConfigWithMode(cfg *Config, configDir string, strict bool) error {
	configPath, data, exists, err := readConfigFile(configDir, configFormatFile, false)
	if err != nil {
		return err
	}
	if !exists {
		log.WithField("path", configPath).Warn("Format config file not found, using defaults")
		return nil
	}

	// Preserve defaults: unmarshal into a copy, then merge non-zero fields
	formatOverrides := cfg.Format
	if err := decodeStrictJSON(data, &formatOverrides); err != nil {
		if !strict {
			log.WithError(err).WithField("path", configPath).Warn("Format config invalid, using default formatting")
			return nil
		}
		return fmt.Errorf("failed to parse format config file %s: %w", configPath, err)
	}
	cfg.Format = formatOverrides

	return nil
}

func readConfigFile(configDir, filename string, required bool) (string, []byte, bool, error) {
	configPath := filepath.Join(configDir, filename)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return configPath, nil, false, nil
		}
		return configPath, nil, false, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}
	return configPath, data, true, nil
}

func decodeStrictJSON(data []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("config JSON must contain a single object")
	}
	return nil
}

// validateConfig validates the loaded configuration
//
// Security validations:
// - Webhook URLs must use the expected Discord or Slack webhook format
// - API keys are checked if webhooks are enabled
// - Intervals have minimum limits to prevent API abuse
// - Log rotation limits prevent disk space exhaustion
//
// Operational validations:
// - Resource limits (workers, retry counts) within reasonable bounds
// - Time intervals long enough to avoid rate limiting
func validateConfig(cfg *Config) error {
	validators := []func(*Config) error{
		validateLogConfig,
		validateAPIConfig,
		validateWebhookConfig,
		validateTimingConfig,
		validateRetryConfig,
		validateStatusRetentionConfig,
		validateFormatConfig,
		validateWebhookFiltersAndQuietHours,
	}
	for _, validator := range validators {
		if err := validator(cfg); err != nil {
			return err
		}
	}
	warnSharedWebhookURLs(cfg)
	warnTargetInheritance(cfg)
	return nil
}

// warnSharedWebhookURLs logs one WARN per webhook kind whose enabled endpoints
// repeat a URL. Sharing is allowed and delivers correctly; the risk it flags is
// duplicate posts where the endpoints' filters overlap. Never a load failure,
// and never a URL in the log.
func warnSharedWebhookURLs(cfg *Config) {
	for _, group := range SharedWebhookURLGroups(cfg) {
		log.WithFields(log.Fields{
			"endpoint_ids":   strings.Join(group.EndpointIDs, ", "),
			"endpoint_count": len(group.EndpointIDs),
			"webhook_kind":   group.Kind,
			"webhook_host":   group.Host,
		}).Warn("Webhook endpoints share one webhook URL; where their filters overlap the same alert is posted to that channel once per endpoint")
	}
}

func validateLogConfig(cfg *Config) error {
	switch strings.ToUpper(cfg.LogLevel) {
	case "TRACE", "DEBUG", "INFO", "WARNING", "WARN", "ERROR":
	default:
		return fmt.Errorf("invalid log level: %s (must be TRACE, DEBUG, INFO, WARNING, WARN, or ERROR)", cfg.LogLevel)
	}
	if strings.TrimSpace(cfg.LogFilePath) == "" {
		return fmt.Errorf("log_file_path cannot be empty")
	}

	// Validate log rotation settings
	if cfg.LogRotation.MaxSizeMB < minLogRotationSizeMB || cfg.LogRotation.MaxSizeMB > maxLogRotationSizeMB {
		return fmt.Errorf("invalid log rotation max_size_mb: %d (must be %d-%d)", cfg.LogRotation.MaxSizeMB, minLogRotationSizeMB, maxLogRotationSizeMB)
	}
	if cfg.LogRotation.MaxBackups < minLogRotationBackups || cfg.LogRotation.MaxBackups > maxLogRotationBackups {
		return fmt.Errorf("invalid log rotation max_backups: %d (must be %d-%d)", cfg.LogRotation.MaxBackups, minLogRotationBackups, maxLogRotationBackups)
	}
	if cfg.LogRotation.MaxAgeDays < minLogRotationAgeDays || cfg.LogRotation.MaxAgeDays > maxLogRotationAgeDays {
		return fmt.Errorf("invalid log rotation max_age_days: %d (must be %d-%d)", cfg.LogRotation.MaxAgeDays, minLogRotationAgeDays, maxLogRotationAgeDays)
	}

	if cfg.LogRotation.MaxBackups == 0 && cfg.LogRotation.MaxAgeDays == 0 {
		return fmt.Errorf("invalid log rotation retention: max_backups and max_age_days cannot both be 0")
	}

	// Warn about high disk usage potential
	maxPotentialDiskUsageMB := cfg.LogRotation.MaxSizeMB * (cfg.LogRotation.MaxBackups + 1)
	if maxPotentialDiskUsageMB > logRotationDiskWarningMB {
		log.WithFields(log.Fields{
			"max_size_mb":             cfg.LogRotation.MaxSizeMB,
			"max_backups":             cfg.LogRotation.MaxBackups,
			"potential_disk_usage_mb": maxPotentialDiskUsageMB,
		}).Warn("Log rotation configuration may use significant disk space")
	}
	return nil
}

func validateAPIConfig(cfg *Config) error {
	normalizedAPIBaseURL, err := api.NormalizeBaseURL(cfg.APIBaseURL)
	if err != nil {
		return fmt.Errorf("api_base_url is invalid: %w", err)
	}
	cfg.APIBaseURL = normalizedAPIBaseURL

	hasEnabledWebhook := false
	hasEnabledRansomwareWebhook := false
	for _, target := range WebhookTargets(cfg) {
		if !target.Webhook.Enabled {
			continue
		}
		hasEnabledWebhook = true
		if target.Name == WebhookTypeRansomware {
			hasEnabledRansomwareWebhook = true
		}
	}
	if hasEnabledRansomwareWebhook {
		if cfg.APIKey == "" {
			return fmt.Errorf("api_key is required when ransomware webhooks are enabled")
		}
		if isPlaceholderAPIKey(cfg.APIKey) {
			return fmt.Errorf("api_key appears to be a placeholder value when ransomware webhooks are enabled")
		}
	}
	if hasEnabledWebhook {
		if cfg.APIKey == "" {
			log.Warn("API key is empty but webhooks are enabled")
		} else if isPlaceholderAPIKey(cfg.APIKey) {
			log.Warn("API key appears to be a placeholder value - please set a real API key")
		}
	}
	return nil
}

func validateWebhookConfig(cfg *Config) error {
	for _, target := range WebhookTargets(cfg) {
		var err error
		switch target.Platform {
		case WebhookPlatformDiscord:
			err = validateDiscordWebhook(target.Name, target.Webhook)
		case WebhookPlatformSlack:
			err = validateSlackWebhook(target.Name, target.Webhook)
		case WebhookPlatformSlackCompatible:
			err = validateSlackCompatibleWebhook(target.Name, target.Webhook, cfg.SlackCompatibleWebhookHosts)
		default:
			err = fmt.Errorf("unsupported webhook platform %q", target.Platform)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// validateTimingConfig runs the timing-related validators in the same order
// the checks used to appear in a single function, so the first failing check
// is unchanged. Split out of one function (gocyclo 35) into topic-sized
// helpers purely to bring per-function cyclomatic complexity under the
// central lint profile's threshold; no check, message or ordering changed.
func validateTimingConfig(cfg *Config) error {
	validators := []func(*Config) error{
		validateAPITimingConfig,
		validateWebhookTimingConfig,
		validateRSSPollTimingConfig,
		validateWebhookDelayConfig,
		validateRSSLimitsConfig,
	}
	for _, validator := range validators {
		if err := validator(cfg); err != nil {
			return err
		}
	}
	return nil
}

func validateAPITimingConfig(cfg *Config) error {
	if cfg.APIPollInterval < minPollInterval {
		return fmt.Errorf("api_poll_interval too short: %v (minimum %s)", cfg.APIPollInterval, minPollInterval)
	}
	if cfg.APICheckTimeout < minCheckTimeout {
		return fmt.Errorf("api_check_timeout too short: %v (minimum %s)", cfg.APICheckTimeout, minCheckTimeout)
	}
	if cfg.APIRequestTimeout < minAPIRequestTimeout || cfg.APIRequestTimeout > maxAPIRequestTimeout {
		return fmt.Errorf("api_request_timeout out of range: %v (must be %s-%s)", cfg.APIRequestTimeout, minAPIRequestTimeout, maxAPIRequestTimeout)
	}
	if cfg.APIMaxRetries < minAPIMaxRetries || cfg.APIMaxRetries > maxAPIMaxRetries {
		return fmt.Errorf("api_max_retries out of range: %d (must be %d-%d)", cfg.APIMaxRetries, minAPIMaxRetries, maxAPIMaxRetries)
	}
	if cfg.APIRetryDelay < minAPIRetryDelay || cfg.APIRetryDelay > maxAPIRetryDelay {
		return fmt.Errorf("api_retry_delay out of range: %v (must be %s-%s)", cfg.APIRetryDelay, minAPIRetryDelay, maxAPIRetryDelay)
	}
	return nil
}

func validateWebhookTimingConfig(cfg *Config) error {
	if cfg.WebhookRequestTimeout < minWebhookTimeout || cfg.WebhookRequestTimeout > maxWebhookTimeout {
		return fmt.Errorf("webhook_request_timeout out of range: %v (must be %s-%s)", cfg.WebhookRequestTimeout, minWebhookTimeout, maxWebhookTimeout)
	}
	if cfg.WebhookMaxRetries < minWebhookMaxRetries || cfg.WebhookMaxRetries > maxWebhookMaxRetries {
		return fmt.Errorf("webhook_max_retries out of range: %d (must be %d-%d)", cfg.WebhookMaxRetries, minWebhookMaxRetries, maxWebhookMaxRetries)
	}
	if cfg.WebhookRetryDelay < minWebhookRetryDelay || cfg.WebhookRetryDelay > maxWebhookRetryDelay {
		return fmt.Errorf("webhook_retry_delay out of range: %v (must be %s-%s)", cfg.WebhookRetryDelay, minWebhookRetryDelay, maxWebhookRetryDelay)
	}
	return nil
}

func validateRSSPollTimingConfig(cfg *Config) error {
	if cfg.RSSPollInterval < minPollInterval {
		return fmt.Errorf("rss_poll_interval too short: %v (minimum %s)", cfg.RSSPollInterval, minPollInterval)
	}
	if cfg.RSSCheckTimeout < minCheckTimeout {
		return fmt.Errorf("rss_check_timeout too short: %v (minimum %s)", cfg.RSSCheckTimeout, minCheckTimeout)
	}
	if cfg.RSSRetryDelay < minRSSRetryDelay {
		return fmt.Errorf("rss_retry_delay too short: %v (minimum %s)", cfg.RSSRetryDelay, minRSSRetryDelay)
	}
	return nil
}

func validateWebhookDelayConfig(cfg *Config) error {
	if cfg.DiscordDelay < 0 {
		return fmt.Errorf("discord_delay cannot be negative: %v", cfg.DiscordDelay)
	}
	if cfg.DiscordDelay < minDiscordDelay {
		return fmt.Errorf("discord_delay too short: %v (minimum %s to avoid rate limits)", cfg.DiscordDelay, minDiscordDelay)
	}
	if cfg.DiscordDelay > maxWebhookDelay {
		return fmt.Errorf("discord_delay too long: %v (maximum %s)", cfg.DiscordDelay, maxWebhookDelay)
	}

	if cfg.SlackDelay < 0 {
		return fmt.Errorf("slack_delay cannot be negative: %v", cfg.SlackDelay)
	}
	if cfg.SlackDelay < minSlackDelay {
		return fmt.Errorf("slack_delay too short: %v (minimum %s to avoid rate limits)", cfg.SlackDelay, minSlackDelay)
	}
	if cfg.SlackDelay > maxWebhookDelay {
		return fmt.Errorf("slack_delay too long: %v (maximum %s)", cfg.SlackDelay, maxWebhookDelay)
	}
	return nil
}

func validateRSSLimitsConfig(cfg *Config) error {
	if cfg.RSSRetryCount < minRSSRetryCount || cfg.RSSRetryCount > maxRSSRetryCount {
		return fmt.Errorf("rss_retry_count out of range: %d (must be %d-%d)", cfg.RSSRetryCount, minRSSRetryCount, maxRSSRetryCount)
	}
	if cfg.MaxRSSWorkers < minRSSWorkers || cfg.MaxRSSWorkers > maxRSSWorkers {
		return fmt.Errorf("max_rss_workers out of range: %d (must be %d-%d)", cfg.MaxRSSWorkers, minRSSWorkers, maxRSSWorkers)
	}
	if cfg.RSSWorkerTimeout < minRSSWorkerTimeout || cfg.RSSWorkerTimeout > maxRSSWorkerTimeout {
		return fmt.Errorf("rss_worker_timeout out of range: %v (must be %s-%s)", cfg.RSSWorkerTimeout, minRSSWorkerTimeout, maxRSSWorkerTimeout)
	}
	if cfg.RSSMaxItemAge < 0 {
		return fmt.Errorf("rss_max_item_age cannot be negative: %v", cfg.RSSMaxItemAge)
	}
	if cfg.APIMaxEntriesPerCycle < minEntriesPerCycle || cfg.APIMaxEntriesPerCycle > maxEntriesPerCycle {
		return fmt.Errorf("max_api_entries_per_cycle out of range: %d (must be %d-%d)", cfg.APIMaxEntriesPerCycle, minEntriesPerCycle, maxEntriesPerCycle)
	}
	if cfg.RSSMaxEntriesPerCycle < minEntriesPerCycle || cfg.RSSMaxEntriesPerCycle > maxEntriesPerCycle {
		return fmt.Errorf("max_rss_entries_per_cycle out of range: %d (must be %d-%d)", cfg.RSSMaxEntriesPerCycle, minEntriesPerCycle, maxEntriesPerCycle)
	}
	return nil
}

func validateRetryConfig(cfg *Config) error {
	if cfg.RetryMaxAttempts < minRetryMaxAttempts || cfg.RetryMaxAttempts > maxRetryMaxAttempts {
		return fmt.Errorf("retry_max_attempts out of range: %d (must be %d-%d, 0=unlimited)", cfg.RetryMaxAttempts, minRetryMaxAttempts, maxRetryMaxAttempts)
	}
	if cfg.RetryWindow < 0 {
		return fmt.Errorf("retry_window cannot be negative: %v", cfg.RetryWindow)
	}
	if cfg.RetryMaxAttempts == 0 && cfg.RetryWindow == 0 {
		return fmt.Errorf("retry_max_attempts and retry_window cannot both be unlimited")
	}
	return nil
}

func validateStatusRetentionConfig(cfg *Config) error {
	retention := cfg.StatusRetention
	counts := []struct {
		name  string
		value int
	}{
		{name: "status_retention.max_api_sent_items", value: retention.MaxAPISentItems},
		{name: "status_retention.max_rss_parsed_items", value: retention.MaxRSSParsedItems},
		{name: "status_retention.max_rss_sent_items", value: retention.MaxRSSSentItems},
		{name: "status_retention.max_retry_queue_items", value: retention.MaxRetryQueueItems},
		{name: "status_retention.max_dead_letter_items", value: retention.MaxDeadLetterItems},
	}
	for _, count := range counts {
		if count.value < minStatusRetentionItems || count.value > maxStatusRetentionItems {
			return fmt.Errorf("%s out of range: %d (must be %d-%d)", count.name, count.value, minStatusRetentionItems, maxStatusRetentionItems)
		}
	}

	ages := []struct {
		name  string
		value time.Duration
	}{
		{name: "status_retention.rss_parsed_max_age", value: retention.RSSParsedMaxAge},
		{name: "status_retention.retry_queue_max_age", value: retention.RetryQueueMaxAge},
		{name: "status_retention.dead_letter_max_age", value: retention.DeadLetterMaxAge},
	}
	for _, age := range ages {
		if age.value < minStatusRetentionAge || age.value > maxStatusRetentionAge {
			return fmt.Errorf("%s out of range: %v (must be %s-%s)", age.name, age.value, minStatusRetentionAge, maxStatusRetentionAge)
		}
	}

	// audit_log_rotation reuses LogRotation's bounds constants deliberately:
	// both express the same generic "how big / how many / how old may a
	// rotated file's backups be" question, and there is no reason for the
	// two files to be allowed different ranges.
	rot := cfg.StatusRetention.AuditLogRotation
	if rot.MaxSizeMB < minLogRotationSizeMB || rot.MaxSizeMB > maxLogRotationSizeMB {
		return fmt.Errorf("invalid status_retention.audit_log_rotation.max_size_mb: %d (must be %d-%d)", rot.MaxSizeMB, minLogRotationSizeMB, maxLogRotationSizeMB)
	}
	if rot.MaxBackups < minLogRotationBackups || rot.MaxBackups > maxLogRotationBackups {
		return fmt.Errorf("invalid status_retention.audit_log_rotation.max_backups: %d (must be %d-%d)", rot.MaxBackups, minLogRotationBackups, maxLogRotationBackups)
	}
	if rot.MaxAgeDays < minLogRotationAgeDays || rot.MaxAgeDays > maxLogRotationAgeDays {
		return fmt.Errorf("invalid status_retention.audit_log_rotation.max_age_days: %d (must be %d-%d)", rot.MaxAgeDays, minLogRotationAgeDays, maxLogRotationAgeDays)
	}
	if rot.MaxBackups == 0 && rot.MaxAgeDays == 0 {
		return fmt.Errorf("invalid status_retention.audit_log_rotation retention: max_backups and max_age_days cannot both be 0")
	}
	if maxPotential := rot.MaxSizeMB * (rot.MaxBackups + 1); maxPotential > logRotationDiskWarningMB {
		log.WithFields(log.Fields{
			"max_size_mb":             rot.MaxSizeMB,
			"max_backups":             rot.MaxBackups,
			"potential_disk_usage_mb": maxPotential,
		}).Warn("Audit log rotation configuration may use significant disk space")
	}
	return nil
}

func validateFormatConfig(cfg *Config) error {
	if err := validateDisplayLocale(cfg.Format.DisplayLocale); err != nil {
		return err
	}
	if err := validateTimestampFormat(cfg.Format.TimestampFormat); err != nil {
		return err
	}
	if err := validateDisplayTimezone(cfg.Format.DisplayTimezone); err != nil {
		return err
	}
	if err := validateFieldOrder("field_order", cfg.Format.FieldOrder, discordFieldOrderMaxItems); err != nil {
		return err
	}
	if err := validateFieldOrder("slack field_order", cfg.Format.Slack.FieldOrder, slackFieldOrderMaxItems); err != nil {
		return err
	}
	if err := validateRSSFieldOrder("rss.field_order", cfg.Format.RSS.FieldOrder); err != nil {
		return err
	}
	if err := validateFormatLabelMap("field_labels", cfg.Format.FieldLabels); err != nil {
		return err
	}
	if err := validateFormatLabelMap("rss.field_labels", cfg.Format.RSS.FieldLabels); err != nil {
		return err
	}
	if err := validateFormatLabelMap("discord.field_labels", cfg.Format.Discord.FieldLabels); err != nil {
		return err
	}
	if err := validateFormatLabelMap("slack.field_labels", cfg.Format.Slack.FieldLabels); err != nil {
		return err
	}
	if err := validateHexColor("discord.ransomware_color", cfg.Format.Discord.RansomwareColor); err != nil {
		return err
	}
	if err := validateHexColor("discord.rss_color", cfg.Format.Discord.RSSColor); err != nil {
		return err
	}
	if err := validateHexColor("discord.government_color", cfg.Format.Discord.GovernmentColor); err != nil {
		return err
	}
	if err := validateDescriptionMaxChars(
		"discord.description_max_chars",
		cfg.Format.Discord.DescriptionMaxChars,
		discordEmbedDescriptionMax,
	); err != nil {
		return err
	}
	if err := validateDescriptionMaxChars(
		"slack.description_max_chars",
		cfg.Format.Slack.DescriptionMaxChars,
		maxDescriptionMaxChars,
	); err != nil {
		return err
	}
	if cfg.Format.RSS.DescriptionMaxChars > 0 {
		if err := validateDescriptionMaxChars(
			"rss.description_max_chars",
			cfg.Format.RSS.DescriptionMaxChars,
			maxDescriptionMaxChars,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateTimestampFormat(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("timestamp_format must be a non-empty Go time layout")
	}
	return nil
}

func validateDisplayTimezone(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("display_timezone must be an IANA timezone such as %q", "UTC")
	}
	if _, err := time.LoadLocation(value); err != nil {
		return fmt.Errorf("display_timezone must be a valid IANA timezone: %w", err)
	}
	return nil
}

func validateDisplayLocale(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("display_locale must be a BCP 47 language tag such as %q", "en")
	}
	if _, err := language.Parse(value); err != nil {
		return fmt.Errorf("display_locale must be a valid BCP 47 language tag: %w", err)
	}
	return nil
}

func validateHexColor(name, value string) error {
	if len(value) != 7 || value[0] != '#' {
		return fmt.Errorf("%s must be a #RRGGBB hex color", name)
	}
	for _, r := range value[1:] {
		if !isHexDigit(r) {
			return fmt.Errorf("%s must be a #RRGGBB hex color", name)
		}
	}
	return nil
}

func validateDescriptionMaxChars(name string, value, maxValue int) error {
	if value < minDescriptionMaxChars || value > maxValue {
		return fmt.Errorf("%s out of range: %d (must be %d-%d)", name, value, minDescriptionMaxChars, maxValue)
	}
	return nil
}

func validateFormatLabelMap(name string, labels map[string]string) error {
	if len(labels) > maxFormatLabels {
		return fmt.Errorf("%s has too many entries: %d (maximum %d)", name, len(labels), maxFormatLabels)
	}
	for key, value := range labels {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%s contains an empty label key", name)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s[%q] cannot be empty", name, key)
		}
		if len([]rune(value)) > maxFormatLabelRunes {
			return fmt.Errorf("%s[%q] too long: %d runes (maximum %d)", name, key, len([]rune(value)), maxFormatLabelRunes)
		}
	}
	return nil
}

func validateWebhookFiltersAndQuietHours(cfg *Config) error {
	for _, target := range WebhookTargets(cfg) {
		if !target.Webhook.Enabled {
			continue
		}
		if err := validateWebhookFilters(target.QualifiedName(), target.Webhook.Filters); err != nil {
			return wrapInheritedValidationError(err, target, target.InheritedFilters, "filters")
		}
		if err := validateWebhookFilterScope(target.QualifiedName(), target.Name, target.Webhook.Filters); err != nil {
			return wrapInheritedValidationError(err, target, target.InheritedFilters, "filters")
		}
	}
	for _, target := range WebhookTargets(cfg) {
		if err := validateQuietHours(target.QualifiedName(), target.Webhook.QuietHours); err != nil {
			return wrapInheritedValidationError(err, target, target.InheritedQuietHours, "quiet_hours")
		}
	}

	return nil
}

// wrapInheritedValidationError adds inheritance context to a filters/quiet_hours
// validation error when the offending value was never set by this target but
// inherited from its parent webhook block. Without this an operator sees an
// error naming a field their target's own JSON never mentions.
func wrapInheritedValidationError(err error, target WebhookTargetConfig, inherited bool, key string) error {
	if !inherited {
		return err
	}
	return fmt.Errorf("%w (target %s omits %q and inherited this value from the %s.%s webhook block; remove the invalid field from that block, or set an explicit %q: {} on this target to opt out of inheriting it)",
		err, target.QualifiedName(), key, target.Platform, target.Name, key)
}

func validateWebhookFilterScope(name, webhookType string, filters *WebhookFilters) error {
	if filters == nil {
		return nil
	}

	switch webhookType {
	case WebhookTypeRansomware:
		if len(filters.IncludeCategories) > 0 {
			return fmt.Errorf("webhook %s: include_categories is only supported for RSS webhooks", name)
		}
		if len(filters.ExcludeCategories) > 0 {
			return fmt.Errorf("webhook %s: exclude_categories is only supported for RSS webhooks", name)
		}
	case WebhookTypeRSS, WebhookTypeGovernment:
		if len(filters.IncludeGroups) > 0 {
			return fmt.Errorf("webhook %s: include_groups is only supported for ransomware API webhooks", name)
		}
		if len(filters.ExcludeGroups) > 0 {
			return fmt.Errorf("webhook %s: exclude_groups is only supported for ransomware API webhooks", name)
		}
		if len(filters.IncludeCountries) > 0 {
			return fmt.Errorf("webhook %s: include_countries is only supported for ransomware API webhooks", name)
		}
		if len(filters.ExcludeCountries) > 0 {
			return fmt.Errorf("webhook %s: exclude_countries is only supported for ransomware API webhooks", name)
		}
		if len(filters.IncludeActivities) > 0 {
			return fmt.Errorf("webhook %s: include_activities is only supported for ransomware API webhooks", name)
		}
		if len(filters.ExcludeActivities) > 0 {
			return fmt.Errorf("webhook %s: exclude_activities is only supported for ransomware API webhooks", name)
		}
	}

	allowedFields := webhookFilterFieldNames(webhookType)
	if err := validateWebhookFieldMapScope(name, "include_fields", filters.IncludeFields, allowedFields); err != nil {
		return err
	}
	if err := validateWebhookFieldMapScope(name, "exclude_fields", filters.ExcludeFields, allowedFields); err != nil {
		return err
	}

	return nil
}

var apiWebhookFilterFields = map[string]struct{}{
	"id":          {},
	"group":       {},
	"victim":      {},
	"country":     {},
	"activity":    {},
	"attack_date": {},
	"claim_url":   {},
	"website":     {},
	"description": {},
	"screenshot":  {},
	"published":   {},
	"discovered":  {},
}

var rssWebhookFilterFields = map[string]struct{}{
	"title":       {},
	"link":        {},
	"description": {},
	"published":   {},
	"author":      {},
	"category":    {},
	"categories":  {},
	"guid":        {},
	"feed_title":  {},
	"feed_url":    {},
}

func webhookFilterFieldNames(webhookType string) map[string]struct{} {
	switch webhookType {
	case WebhookTypeRansomware:
		return apiWebhookFilterFields
	case WebhookTypeRSS, WebhookTypeGovernment:
		return rssWebhookFilterFields
	default:
		return nil
	}
}

func validateWebhookFieldMapScope(name, mapName string, fields map[string][]string, allowed map[string]struct{}) error {
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("webhook %s: invalid %s field %q", name, mapName, field)
		}
	}
	return nil
}

type WebhookTargetConfig struct {
	Platform          string
	Name              string
	DestinationSuffix string
	Webhook           WebhookConfig
	// InheritedFilters/InheritedQuietHours are true when this endpoint came
	// from a targets[] entry that omitted the key and inherited it from the
	// parent block. Always false for url/urls[]-derived endpoints, which have
	// always inherited unconditionally.
	InheritedFilters    bool
	InheritedQuietHours bool
}

func (target WebhookTargetConfig) QualifiedName() string {
	name := target.Platform + "." + target.Name
	if target.DestinationSuffix != "" {
		return name + "." + target.DestinationSuffix
	}
	return name
}

func WebhookTargets(cfg *Config) []WebhookTargetConfig {
	if cfg == nil {
		return nil
	}
	targets := make([]WebhookTargetConfig, 0, 9)
	targets = appendWebhookTargets(targets, WebhookPlatformDiscord, WebhookTypeRansomware, cfg.DiscordWebhooks.Ransomware)
	targets = appendWebhookTargets(targets, WebhookPlatformDiscord, WebhookTypeRSS, cfg.DiscordWebhooks.RSS)
	targets = appendWebhookTargets(targets, WebhookPlatformDiscord, WebhookTypeGovernment, cfg.DiscordWebhooks.Government)
	targets = appendWebhookTargets(targets, WebhookPlatformSlack, WebhookTypeRansomware, cfg.SlackWebhooks.Ransomware)
	targets = appendWebhookTargets(targets, WebhookPlatformSlack, WebhookTypeRSS, cfg.SlackWebhooks.RSS)
	targets = appendWebhookTargets(targets, WebhookPlatformSlack, WebhookTypeGovernment, cfg.SlackWebhooks.Government)
	targets = appendWebhookTargets(targets, WebhookPlatformSlackCompatible, WebhookTypeRansomware, cfg.SlackCompatibleWebhooks.Ransomware)
	targets = appendWebhookTargets(targets, WebhookPlatformSlackCompatible, WebhookTypeRSS, cfg.SlackCompatibleWebhooks.RSS)
	targets = appendWebhookTargets(targets, WebhookPlatformSlackCompatible, WebhookTypeGovernment, cfg.SlackCompatibleWebhooks.Government)
	return targets
}

func appendWebhookTargets(targets []WebhookTargetConfig, platform, name string, webhook WebhookConfig) []WebhookTargetConfig {
	endpoints, inheritedFilters, inheritedQuietHours := webhookEndpointConfigs(webhook)
	if len(endpoints) == 0 {
		return append(targets, WebhookTargetConfig{Platform: platform, Name: name, Webhook: webhook})
	}
	for i, endpoint := range endpoints {
		targetWebhook := webhook
		targetWebhook.URL = endpoint.URL
		targetWebhook.URLs = nil
		targetWebhook.Targets = nil
		targetWebhook.Filters = endpoint.Filters
		targetWebhook.QuietHours = endpoint.QuietHours
		targets = append(targets, WebhookTargetConfig{
			Platform:            platform,
			Name:                name,
			DestinationSuffix:   webhookDestinationSuffix(i),
			Webhook:             targetWebhook,
			InheritedFilters:    inheritedFilters[i],
			InheritedQuietHours: inheritedQuietHours[i],
		})
	}
	return targets
}

func webhookEndpointConfigs(webhook WebhookConfig) (endpoints []WebhookTarget, inheritedFilters, inheritedQuietHours []bool) {
	endpoints = make([]WebhookTarget, 0, 1+len(webhook.URLs)+len(webhook.Targets))
	if strings.TrimSpace(webhook.URL) != "" {
		endpoints = append(endpoints, WebhookTarget{
			URL:        strings.TrimSpace(webhook.URL),
			Filters:    webhook.Filters,
			QuietHours: webhook.QuietHours,
		})
		inheritedFilters = append(inheritedFilters, false)
		inheritedQuietHours = append(inheritedQuietHours, false)
	}
	for _, endpointURL := range webhook.URLs {
		endpointURL = strings.TrimSpace(endpointURL)
		if endpointURL != "" {
			endpoints = append(endpoints, WebhookTarget{
				URL:        endpointURL,
				Filters:    webhook.Filters,
				QuietHours: webhook.QuietHours,
			})
			inheritedFilters = append(inheritedFilters, false)
			inheritedQuietHours = append(inheritedQuietHours, false)
		}
	}
	for _, endpoint := range webhook.Targets {
		endpoint.URL = strings.TrimSpace(endpoint.URL)
		if endpoint.URL == "" {
			continue
		}
		inheritsFilters := endpoint.Filters == nil && webhook.Filters != nil
		inheritsQuietHours := endpoint.QuietHours == nil && webhook.QuietHours != nil
		if endpoint.Filters == nil {
			endpoint.Filters = webhook.Filters
		}
		if endpoint.QuietHours == nil {
			endpoint.QuietHours = webhook.QuietHours
		}
		endpoints = append(endpoints, endpoint)
		inheritedFilters = append(inheritedFilters, inheritsFilters)
		inheritedQuietHours = append(inheritedQuietHours, inheritsQuietHours)
	}
	return endpoints, inheritedFilters, inheritedQuietHours
}

func webhookDestinationSuffix(index int) string {
	if index <= 0 {
		return ""
	}
	return fmt.Sprint(index + 1)
}

func WebhookTargetsForType(cfg *Config, webhookType string) []WebhookTargetConfig {
	allTargets := WebhookTargets(cfg)
	targets := make([]WebhookTargetConfig, 0, len(allTargets))
	for _, target := range allTargets {
		if target.Name == webhookType {
			targets = append(targets, target)
		}
	}
	return targets
}

// SharedWebhookURLGroup names the enabled endpoints of one webhook kind that
// were configured with the same URL. Every endpoint in a group delivers on its
// own, so where their filters overlap the same alert reaches that channel once
// per endpoint. Endpoints of one kind are fanned out to across every platform,
// so a group may span platform blocks. Sharing a URL is allowed on purpose;
// see readme.md.
type SharedWebhookURLGroup struct {
	Kind        string   // ransomware, rss, or government
	Host        string   // webhook host only; never the path or the token
	EndpointIDs []string // QualifiedName() per endpoint, in configuration order
}

// SharedWebhookURLGroups reports webhook kinds whose flattened enabled endpoints
// (url, urls[], targets[], across all platforms) repeat a URL. Matching is
// byte-exact on the trimmed endpoint URL, which is the identity the legacy
// per-URL dedup alias uses (status.makeCompositeKey), so a group here is exactly
// a set of endpoints that both receive the same items and share dedup state.
// Disabled endpoints are ignored; endpoints of different kinds are never grouped
// together, because feed-type to webhook-kind routing is 1:1 and no single item
// can reach two kinds.
func SharedWebhookURLGroups(cfg *Config) []SharedWebhookURLGroup {
	if cfg == nil {
		return nil
	}
	type endpointKey struct{ kind, endpointURL string }
	order := make([]endpointKey, 0, 9)
	members := make(map[endpointKey][]string)
	for _, target := range WebhookTargets(cfg) {
		if !target.Webhook.Enabled {
			continue
		}
		endpointURL := strings.TrimSpace(target.Webhook.URL)
		if endpointURL == "" {
			continue
		}
		key := endpointKey{kind: target.Name, endpointURL: endpointURL}
		if _, seen := members[key]; !seen {
			order = append(order, key)
		}
		members[key] = append(members[key], target.QualifiedName())
	}
	groups := make([]SharedWebhookURLGroup, 0, len(order))
	for _, key := range order {
		endpointIDs := members[key]
		if len(endpointIDs) < 2 {
			continue
		}
		groups = append(groups, SharedWebhookURLGroup{
			Kind:        key.kind,
			Host:        webhookURLHost(key.endpointURL),
			EndpointIDs: endpointIDs,
		})
	}
	if len(groups) == 0 {
		return nil
	}
	return groups
}

// TargetInheritanceGroup names the targets[] endpoints of one webhook block
// that omitted "filters" and/or "quiet_hours" and therefore now inherit the
// block's value for that key. Never a load failure -- informational only,
// same as SharedWebhookURLGroup.
type TargetInheritanceGroup struct {
	Kind        string   // ransomware, rss, or government
	Platform    string   // discord, slack, or slack_compatible
	EndpointIDs []string // QualifiedName() per inheriting endpoint, in configuration order
}

// TargetInheritanceGroups reports, per enabled webhook block, every targets[]
// endpoint that omitted "filters" and/or "quiet_hours" while the block
// defines a non-nil value for that key -- such an endpoint now inherits the
// block's value for that key instead of getting unfiltered / always-deliver
// behaviour on it, unifying targets[] with the url/urls[] endpoints, which
// have always inherited. An endpoint that sets an explicit
// "filters": {} (or "quiet_hours": {}) never appears here -- an explicit
// empty object is the escape hatch and does not inherit.
func TargetInheritanceGroups(cfg *Config) []TargetInheritanceGroup {
	if cfg == nil {
		return nil
	}
	type blockKey struct{ platform, kind string }
	order := make([]blockKey, 0, 9)
	members := make(map[blockKey][]string)
	for _, target := range WebhookTargets(cfg) {
		if !target.Webhook.Enabled || (!target.InheritedFilters && !target.InheritedQuietHours) {
			continue
		}
		key := blockKey{platform: target.Platform, kind: target.Name}
		if _, seen := members[key]; !seen {
			order = append(order, key)
		}
		members[key] = append(members[key], target.QualifiedName())
	}
	groups := make([]TargetInheritanceGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, TargetInheritanceGroup{
			Kind:        key.kind,
			Platform:    key.platform,
			EndpointIDs: members[key],
		})
	}
	if len(groups) == 0 {
		return nil
	}
	return groups
}

// warnTargetInheritance logs one WARN per webhook block whose targets[]
// entries now inherit filters and/or quiet_hours because they omitted their
// own. Never a load failure; never a URL in the log.
func warnTargetInheritance(cfg *Config) {
	for _, group := range TargetInheritanceGroups(cfg) {
		log.WithFields(log.Fields{
			"endpoint_ids":     strings.Join(group.EndpointIDs, ", "),
			"endpoint_count":   len(group.EndpointIDs),
			"webhook_kind":     group.Kind,
			"webhook_platform": group.Platform,
		}).Warn("Webhook target(s) inherited filters/quiet_hours from their webhook block because they omit their own; set an explicit \"filters\": {} or \"quiet_hours\": {} on a target to opt out")
	}
}

// webhookURLHost returns the host of a webhook URL and nothing else, so a log
// line or CLI message can locate the endpoint without carrying its token.
func webhookURLHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func isPlaceholderAPIKey(apiKey string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(apiKey))
	return strings.Contains(normalized, "YOUR") || strings.Contains(normalized, "HERE")
}

func validateFieldOrder(name string, fields []string, maxRenderedItems int) error {
	if len(fields) == 0 {
		return fmt.Errorf("%s cannot be empty", name)
	}
	seen := make(map[string]string, len(fields))
	renderedItems := 0
	for _, field := range fields {
		canonical, ok := canonicalFormatField(field)
		if !ok {
			return fmt.Errorf("invalid %s entry: %q (valid: %s)", name, field, formatfields.ValidFieldNames)
		}
		if previous, exists := seen[canonical]; exists {
			return fmt.Errorf("duplicate %s entry: %q duplicates %q", name, field, previous)
		}
		seen[canonical] = field
		renderedItems++
		if name == "field_order" && canonical == formatfields.FieldDescription {
			renderedItems++
		}
	}
	if renderedItems > maxRenderedItems {
		return fmt.Errorf("%s renders %d items, maximum is %d", name, renderedItems, maxRenderedItems)
	}
	if !fieldOrderHasActionLink(seen) {
		return fmt.Errorf("%s must include at least one action link field: %s", name, formatfields.ActionLinkFieldNames)
	}
	return nil
}

func fieldOrderHasActionLink(seen map[string]string) bool {
	for canonical := range seen {
		if formatfields.IsActionLink(canonical) {
			return true
		}
	}
	return false
}

func canonicalFormatField(field string) (string, bool) {
	return formatfields.Normalize(field)
}

func validateRSSFieldOrder(name string, fields []string) error {
	if len(fields) == 0 {
		return fmt.Errorf("%s cannot be empty", name)
	}
	seen := make(map[string]string, len(fields))
	for _, field := range fields {
		canonical, ok := formatfields.NormalizeRSS(field)
		if !ok {
			return fmt.Errorf("invalid %s entry: %q (valid: %s)", name, field, formatfields.ValidRSSFieldNames)
		}
		if previous, exists := seen[canonical]; exists {
			return fmt.Errorf("duplicate %s entry: %q duplicates %q", name, field, previous)
		}
		seen[canonical] = field
	}
	if len(seen) > len(formatfields.DefaultRSSFieldOrder())+1 {
		return fmt.Errorf("%s renders %d items, maximum is %d", name, len(seen), len(formatfields.DefaultRSSFieldOrder())+1)
	}
	return nil
}

// validateWebhookFilters validates a single webhook's filter configuration
func validateWebhookFilters(name string, filters *WebhookFilters) error {
	if filters == nil {
		return nil
	}

	mode, err := validateKeywordMatchMode(name, filters)
	if err != nil {
		return err
	}

	filterLists := []struct {
		name     string
		values   []string
		maxRunes int
		apply    func(int, string)
	}{
		{
			name:     "include_groups",
			values:   filters.IncludeGroups,
			maxRunes: maxFilterValueRunes,
			apply:    func(i int, value string) { filters.IncludeGroups[i] = value },
		},
		{
			name:     "exclude_groups",
			values:   filters.ExcludeGroups,
			maxRunes: maxFilterValueRunes,
			apply:    func(i int, value string) { filters.ExcludeGroups[i] = value },
		},
		{
			name:     "include_activities",
			values:   filters.IncludeActivities,
			maxRunes: maxFilterValueRunes,
			apply:    func(i int, value string) { filters.IncludeActivities[i] = value },
		},
		{
			name:     "exclude_activities",
			values:   filters.ExcludeActivities,
			maxRunes: maxFilterValueRunes,
			apply:    func(i int, value string) { filters.ExcludeActivities[i] = value },
		},
		{
			name:     "include_categories",
			values:   filters.IncludeCategories,
			maxRunes: maxFilterValueRunes,
			apply:    func(i int, value string) { filters.IncludeCategories[i] = value },
		},
		{
			name:     "exclude_categories",
			values:   filters.ExcludeCategories,
			maxRunes: maxFilterValueRunes,
			apply:    func(i int, value string) { filters.ExcludeCategories[i] = value },
		},
		{
			name:     "include_keywords",
			values:   filters.IncludeKeywords,
			maxRunes: keywordFilterMaxRunes(mode),
			apply:    func(i int, value string) { filters.IncludeKeywords[i] = value },
		},
		{
			name:     "exclude_keywords",
			values:   filters.ExcludeKeywords,
			maxRunes: keywordFilterMaxRunes(mode),
			apply:    func(i int, value string) { filters.ExcludeKeywords[i] = value },
		},
		{
			name:     "include_countries",
			values:   filters.IncludeCountries,
			maxRunes: isoCountryCodeLength,
			apply:    func(i int, value string) { filters.IncludeCountries[i] = strings.ToUpper(value) },
		},
		{
			name:     "exclude_countries",
			values:   filters.ExcludeCountries,
			maxRunes: isoCountryCodeLength,
			apply:    func(i int, value string) { filters.ExcludeCountries[i] = strings.ToUpper(value) },
		},
	}
	for _, list := range filterLists {
		if err := normalizeFilterList(name, list.name, list.values, list.maxRunes, list.apply); err != nil {
			return err
		}
	}
	filters.IncludeFields, err = normalizeFilterFieldMap(name, "include_fields", filters.IncludeFields)
	if err != nil {
		return err
	}
	filters.ExcludeFields, err = normalizeFilterFieldMap(name, "exclude_fields", filters.ExcludeFields)
	if err != nil {
		return err
	}

	if err := validateCountryFilters(name, filters); err != nil {
		return err
	}
	if err := validateKeywordRegexes(name, mode, filters); err != nil {
		return err
	}

	return nil
}

func validateKeywordMatchMode(name string, filters *WebhookFilters) (string, error) {
	mode, err := keywordmode.Normalize(filters.KeywordMatchMode)
	if err != nil {
		return "", fmt.Errorf("webhook %s: %w", name, err)
	}
	filters.KeywordMatchMode = mode
	return mode, nil
}

func validateCountryFilters(name string, filters *WebhookFilters) error {
	for _, code := range append(filters.IncludeCountries, filters.ExcludeCountries...) {
		if !isUppercaseCountryCode(code) {
			return fmt.Errorf("webhook %s: invalid country code %q (must be %d letters, e.g. \"US\")", name, code, isoCountryCodeLength)
		}
	}
	return nil
}

func isUppercaseCountryCode(code string) bool {
	return len(code) == isoCountryCodeLength &&
		code[0] >= 'A' && code[0] <= 'Z' &&
		code[1] >= 'A' && code[1] <= 'Z'
}

func validateKeywordRegexes(name, mode string, filters *WebhookFilters) error {
	if mode != keywordmode.Regex {
		return nil
	}
	if err := validateKeywordRegexList(name, "include_keywords", filters.IncludeKeywords); err != nil {
		return err
	}
	return validateKeywordRegexList(name, "exclude_keywords", filters.ExcludeKeywords)
}

func validateKeywordRegexList(name, listName string, patterns []string) error {
	for _, pattern := range patterns {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("webhook %s: invalid %s regex %q: %w", name, listName, pattern, err)
		}
	}
	return nil
}

func keywordFilterMaxRunes(mode string) int {
	if mode == keywordmode.Regex {
		return maxRegexPatternRunes
	}
	return maxKeywordValueRunes
}

func normalizeFilterFieldMap(webhookName, mapName string, fields map[string][]string) (map[string][]string, error) {
	if fields == nil {
		return nil, nil
	}
	normalized := make(map[string][]string, len(fields))
	for field, values := range fields {
		canonical := strings.ToLower(strings.TrimSpace(field))
		if canonical == "" {
			return nil, fmt.Errorf("webhook %s: %s field name cannot be blank", webhookName, mapName)
		}
		if len([]rune(canonical)) > maxFilterFieldNameRunes {
			return nil, fmt.Errorf("webhook %s: %s field %q exceeds %d characters", webhookName, mapName, field, maxFilterFieldNameRunes)
		}
		if _, exists := normalized[canonical]; exists {
			return nil, fmt.Errorf("webhook %s: %s field %q is duplicated after normalization", webhookName, mapName, canonical)
		}
		copiedValues := append([]string(nil), values...)
		if err := normalizeFilterList(
			webhookName,
			fmt.Sprintf("%s.%s", mapName, canonical),
			copiedValues,
			maxFilterValueRunes,
			func(i int, value string) { copiedValues[i] = value },
		); err != nil {
			return nil, err
		}
		normalized[canonical] = copiedValues
	}
	return normalized, nil
}

func normalizeFilterList(
	webhookName, listName string,
	values []string,
	maxRunes int,
	normalize func(int, string),
) error {
	if len(values) > maxWebhookFilterValues {
		return fmt.Errorf(
			"webhook %s: %s has %d values, maximum is %d",
			webhookName,
			listName,
			len(values),
			maxWebhookFilterValues,
		)
	}
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("webhook %s: %s[%d] cannot be blank", webhookName, listName, i)
		}
		if len([]rune(value)) > maxRunes {
			return fmt.Errorf("webhook %s: %s[%d] exceeds %d characters", webhookName, listName, i, maxRunes)
		}
		normalize(i, value)
	}
	return nil
}

// validateDiscordWebhook validates a Discord webhook configuration
//
// Security checks:
// - Ensures webhook URL is from Discord's official domain
// - Prevents configuration of malicious webhook endpoints
// - URL format validation prevents injection attacks
//
// Only validates format - does not test webhook functionality
func validateDiscordWebhook(name string, webhook WebhookConfig) error {
	if webhook.Enabled {
		if webhook.URL == "" {
			return fmt.Errorf("discord %s webhook is enabled but URL is empty", name)
		}
		if err := validateDiscordWebhookURL(webhook.URL); err != nil {
			return fmt.Errorf("discord %s webhook URL is not valid: %w", name, err)
		}
	}
	return nil
}

func validateDiscordWebhookURL(webhookURL string) error {
	parts, err := discordurl.Parse(webhookURL)
	if err != nil {
		// discordurl.Parse wraps url.Parse's *url.Error with %w, which keeps
		// the raw input (token included) in its Error() text; redact before
		// this propagates to --check-config output or startup logs.
		return textutil.RedactWebhookErrorForURL(err, webhookURL)
	}
	if isPlaceholderWebhookSegment(parts.ID) || isPlaceholderWebhookSegment(parts.Token) {
		return fmt.Errorf("webhook ID or token appears to be a placeholder")
	}
	return nil
}

// validateSlackWebhook validates a Slack webhook configuration
//
// Security checks:
// - Ensures webhook URL is from Slack's official domain
// - Prevents configuration of malicious webhook endpoints
// - URL format validation prevents injection attacks
//
// Only validates format - does not test webhook functionality
func validateSlackWebhook(name string, webhook WebhookConfig) error {
	if webhook.Enabled {
		if webhook.URL == "" {
			return fmt.Errorf("slack %s webhook is enabled but URL is empty", name)
		}
		if err := validateSlackWebhookURL(webhook.URL); err != nil {
			return fmt.Errorf("slack %s webhook URL is not valid: %w", name, err)
		}
	}
	return nil
}

func validateSlackCompatibleWebhook(name string, webhook WebhookConfig, allowedHosts []string) error {
	if !webhook.Enabled {
		return nil
	}
	if webhook.URL == "" {
		return fmt.Errorf("slack-compatible %s webhook is enabled but URL is empty", name)
	}
	if err := validateSlackCompatibleWebhookURL(webhook.URL, allowedHosts); err != nil {
		return fmt.Errorf("slack-compatible %s webhook URL is not valid: %w", name, err)
	}
	return nil
}

func validateSlackWebhookURL(webhookURL string) error {
	parsed, err := url.Parse(webhookURL)
	if err != nil {
		// *url.Error's Error() text contains the raw input (token included);
		// redact before this propagates to --check-config output or startup
		// logs. Fully closed for every host, including hooks.slack.com,
		// hooks.slack-gov.com and any slack-compatible custom host.
		return textutil.RedactWebhookErrorForURL(err, webhookURL)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be https")
	}
	if !isAllowedSlackWebhookHost(parsed.Host) {
		return fmt.Errorf("host must be hooks.slack.com or hooks.slack-gov.com")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "services" {
		return fmt.Errorf("path must be /services/{team}/{channel-or-bot}/{token}")
	}
	for _, part := range parts[1:] {
		if part == "" {
			return fmt.Errorf("team, channel, and token path segments must be non-empty")
		}
		if isPlaceholderWebhookSegment(part) {
			return fmt.Errorf("team, channel, or token path segment appears to be a placeholder")
		}
	}
	return nil
}

func validateSlackCompatibleWebhookURL(webhookURL string, allowedHosts []string) error {
	parsed, err := url.Parse(webhookURL)
	if err != nil {
		// See validateSlackWebhookURL: same redaction, fully closed for a
		// custom host that itself fails to url.Parse.
		return textutil.RedactWebhookErrorForURL(err, webhookURL)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be https")
	}
	if parsed.User != nil {
		return fmt.Errorf("userinfo is not allowed")
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return fmt.Errorf("host is required")
	}
	if !isAllowedSlackCompatibleWebhookHost(parsed.Hostname(), allowedHosts) {
		return fmt.Errorf("host must be an approved host from slack_compatible_webhook_hosts")
	}
	if strings.Trim(parsed.EscapedPath(), "/") == "" {
		return fmt.Errorf("path must not be empty")
	}
	return nil
}

func isAllowedSlackWebhookHost(host string) bool {
	return strings.EqualFold(host, "hooks.slack.com") || strings.EqualFold(host, "hooks.slack-gov.com")
}

func normalizeAllowedWebhookHosts(hosts []string) []string {
	normalized := make([]string, 0, len(hosts))
	seen := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
		if host == "" {
			continue
		}
		if _, exists := seen[host]; exists {
			continue
		}
		seen[host] = struct{}{}
		normalized = append(normalized, host)
	}
	return normalized
}

func isAllowedSlackCompatibleWebhookHost(host string, allowedHosts []string) bool {
	host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	for _, allowedHost := range normalizeAllowedWebhookHosts(allowedHosts) {
		if host == allowedHost {
			return true
		}
	}
	return false
}

func isPlaceholderWebhookSegment(segment string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(segment))
	for _, marker := range []string{"YOUR", "PLACEHOLDER", "TOKEN", "WEBHOOK"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// validateFeedURLs validates all RSS feed URLs to prevent security issues
//
// Security checks:
// - Parses URLs instead of relying on string prefixes
// - Only allows http and https protocols
// - Rejects userinfo, fragments, malformed host/port values, and local literal hosts
// - Rejects duplicate feed URLs within and across feed categories
func validateFeedURLs(cfg *Config) error {
	seen := make(map[string]string)
	for _, group := range cfg.Feeds.Groups() {
		for i, rawURL := range group.URLs {
			location := fmt.Sprintf("%s[%d]", group.JSONName, i)
			if err := validateFeedURL(rawURL, location); err != nil {
				return err
			}
			canonicalURL, err := canonicalFeedURL(rawURL)
			if err != nil {
				return fmt.Errorf("feed '%s' URL is not valid: %w", location, err)
			}
			if firstLocation, exists := seen[canonicalURL]; exists {
				return fmt.Errorf("duplicate RSS feed URL at %s; first declared at %s", location, firstLocation)
			}
			seen[canonicalURL] = location
		}
	}

	return nil
}

// validateFeedURL validates a single RSS feed URL
//
// feedurl.Validate's only failure mode that can carry the raw URL (and
// therefore any secret embedded in its query, path or userinfo) into its
// message is the url.Parse failure feedurl.Parse wraps with %w -- every
// other rejection (scheme, host, sensitive query key, fragment, port range)
// echoes only a short, already-safe fragment (a scheme, a host, a key name).
// Redacting unconditionally, anchored on the exact raw feed URL, covers that
// one case and is a no-op on the safe ones since none of their messages
// contain rawURL to begin with. Without this, a feed URL whose parse fails
// writes its embedded secret in cleartext wherever this error is logged or
// printed -- this is the feed-path sibling of the webhook-URL leak fixed the
// same way in
// validateDiscordWebhookURL/validateSlackWebhookURL/validateSlackCompatibleWebhookURL
// above. Reproduced end to end against the real rotating logger before this
// fix: a feed URL with an invalid port and "apikey=..." in its query landed
// the secret in bot.log via the non-strict branch of
// loadFeedsConfigWithMode, which logs this error and still returns nil --
// the config loads successfully, so there is no failure for an operator to
// go and investigate.
func validateFeedURL(rawURL, name string) error {
	if rawURL == "" {
		return fmt.Errorf("feed '%s' has empty URL", name)
	}

	if err := feedurl.Validate(rawURL, feedurl.Options{}); err != nil {
		return fmt.Errorf("feed '%s' URL is not valid: %w", name, textutil.RedactWebhookErrorForURL(err, rawURL))
	}

	return nil
}

// canonicalFeedURL is only reached today after validateFeedURL has already
// parsed rawURL successfully (validateFeedURLs calls validateFeedURL first
// in the same loop iteration, and feedurl.Parse is a pure function of its
// input), so this feedurl.Parse call cannot itself fail via that path.
// Redacted anyway, the same way and for the same reason as validateFeedURL
// above: this is still a call site that returns feedurl.Parse's raw,
// potentially secret-bearing *url.Error to its caller, it is unit-tested
// directly with a malformed URL independent of validateFeedURL (see
// TestCanonicalFeedURLNormalizesHostAndPort), and nothing prevents a future
// caller from reaching it without validateFeedURL's guard first.
func canonicalFeedURL(rawURL string) (string, error) {
	parsed, err := feedurl.Parse(rawURL)
	if err != nil {
		return "", textutil.RedactWebhookErrorForURL(err, rawURL)
	}

	normalized := *parsed
	normalized.Scheme = strings.ToLower(normalized.Scheme)
	hostname := strings.ToLower(strings.TrimSuffix(normalized.Hostname(), "."))
	normalized.Host = canonicalFeedHost(hostname, canonicalFeedPort(normalized.Scheme, normalized.Port()))
	return normalized.String(), nil
}

// canonicalFeedPort drops the scheme default port so that https://host/rss and
// https://host:443/rss canonicalize to the same duplicate-check key, and
// otherwise normalizes the port to its plain decimal spelling so that
// https://host:8443/rss, https://host:08443/rss and https://host:0000008443/rss
// -- three different string spellings of the same TCP endpoint, since Go's
// dialer (like canonicalFeedPort's own strconv.Atoi) parses a port decimally
// -- all canonicalize to the same duplicate-check key. This value is a
// transient in-memory map key with a single caller, validateFeedURLs; it is
// never persisted, rendered, or used as a dedup marker or destination ID, so
// normalizing it cannot over-merge anything beyond "these two entries poll
// the same feed", which is exactly what canonicalization is for.
//
// The digit-only check below is defense in depth, not a fix for a reachable
// bug: strconv.Atoi accepts a leading '+' ("+8443" -> 8443), which would
// otherwise let canonicalFeedPort fuse ":+8443" onto ":8443", but
// feedurl.Parse (net/url under it) already rejects a signed port before
// canonicalFeedURL ever calls this function, so today no input reaches here
// that would trigger it.
func canonicalFeedPort(scheme, port string) string {
	for _, r := range port {
		if r < '0' || r > '9' {
			return port
		}
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return port
	}
	if (scheme == "https" && portNum == 443) || (scheme == "http" && portNum == 80) {
		return ""
	}
	return strconv.Itoa(portNum)
}

func canonicalFeedHost(hostname, port string) string {
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
}

// validateQuietHours validates a quiet hours configuration block.
func validateQuietHours(name string, qh *QuietHours) error {
	if qh == nil {
		return nil
	}

	// Only validate time fields when enabled (allows pre-configuring without errors)
	if !qh.Enabled {
		return nil
	}

	startMin, err := validateTimeString(qh.Start)
	if err != nil {
		return fmt.Errorf("webhook %s quiet_hours.start: %w", name, err)
	}
	endMin, err := validateTimeString(qh.End)
	if err != nil {
		return fmt.Errorf("webhook %s quiet_hours.end: %w", name, err)
	}
	if startMin == endMin {
		return fmt.Errorf(
			"webhook %s quiet_hours: start and end must differ (both resolve to %02d:%02d)",
			name,
			startMin/60,
			startMin%60,
		)
	}

	tz := qh.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("webhook %s quiet_hours.timezone: invalid IANA timezone %q: %w", name, qh.Timezone, err)
	}

	return nil
}

// validateTimeString validates a time string in 24h ("HH:MM") or 12h ("10pm", "10:30 PM") format.
// Returns the resolved minutes-of-day on success.
func validateTimeString(s string) (int, error) {
	m, err := parseTimeString(s)
	if err != nil {
		return 0, fmt.Errorf("%w (use 24h \"HH:MM\" e.g. \"22:00\" or 12h e.g. \"10pm\", \"10:30 PM\")", err)
	}
	return m, nil
}

// LatestConfigModTime returns the newest modification time across all config files.
// Used by the config watcher to detect changes.
func LatestConfigModTime(configDir string) (time.Time, error) {
	var latest time.Time

	for _, spec := range configFileRegistry {
		path := filepath.Join(configDir, spec.name)
		info, err := os.Stat(path)
		if err != nil {
			if !spec.required && os.IsNotExist(err) {
				continue
			}
			if spec.required {
				return latest, fmt.Errorf("required config file %s: %w", spec.name, err)
			}
			return latest, err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest, nil
}

// ConfigFileSignature returns a stable watch key for all config files.
// Unlike LatestConfigModTime, it changes when optional config files are created
// or deleted, not only when the newest modification time moves forward.
func ConfigFileSignature(configDir string) (string, error) {
	parts := make([]string, 0, len(configFileRegistry))
	for _, spec := range configFileRegistry {
		part, err := configFileStateSignature(configDir, spec.name, spec.required)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "|"), nil
}

func configFileStateSignature(configDir, name string, required bool) (string, error) {
	path := filepath.Join(configDir, name)
	info, err := os.Stat(path)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return name + ":missing", nil
		}
		if required {
			return "", fmt.Errorf("required config file %s: %w", name, err)
		}
		return "", err
	}
	return fmt.Sprintf("%s:present:%d:%d", name, info.ModTime().UnixNano(), info.Size()), nil
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
