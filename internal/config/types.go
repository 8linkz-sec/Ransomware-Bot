// Package config defines all configuration structures and default values
// for the Ransomware-Bot.
//
// Configuration Philosophy:
// - Sensible defaults allow minimal setup
// - Extensive customization options for advanced users
// - Clear separation between general, feed, and format settings
// - Built-in validation prevents misconfigurations
package config

import (
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"
)

// Config represents the complete application configuration
type Config struct {
	// General configuration
	LogLevel              string        `json:"log_level"`
	LogFilePath           string        `json:"log_file_path"`
	LogRotation           LogRotation   `json:"log_rotation"`
	MaxRSSWorkers         int           `json:"max_rss_workers"`
	APIKey                string        `json:"api_key"`
	APIBaseURL            string        `json:"api_base_url"`
	APIRequestTimeout     time.Duration `json:"api_request_timeout"`
	APIMaxRetries         int           `json:"api_max_retries"`
	APIRetryDelay         time.Duration `json:"api_retry_delay"`
	APIPollInterval       time.Duration `json:"api_poll_interval"`
	APICheckTimeout       time.Duration `json:"api_check_timeout"`
	RSSPollInterval       time.Duration `json:"rss_poll_interval"`
	RSSCheckTimeout       time.Duration `json:"rss_check_timeout"`
	RSSRetryCount         int           `json:"rss_retry_count"`
	RSSRetryDelay         time.Duration `json:"rss_retry_delay"`
	RSSWorkerTimeout      time.Duration `json:"rss_worker_timeout"` // Timeout for RSS worker pool results
	RSSMaxItemAge         time.Duration `json:"rss_max_item_age"`   // Max age of RSS items to send (0 = disabled)
	APIMaxEntriesPerCycle int           `json:"max_api_entries_per_cycle"`
	RSSMaxEntriesPerCycle int           `json:"max_rss_entries_per_cycle"`

	// Directory for persistent status data, resolved to an absolute path.
	DataDir string `json:"data_dir"`

	DiscordDelay time.Duration `json:"discord_delay"` // Configurable delay before Discord push

	WebhookRequestTimeout time.Duration `json:"webhook_request_timeout"`
	WebhookMaxRetries     int           `json:"webhook_max_retries"`
	WebhookRetryDelay     time.Duration `json:"webhook_retry_delay"`

	// Retries after the first send, per item and destination, before dead-letter.
	// N allows N+1 sends. Zero means unlimited (requires retry_window).
	RetryMaxAttempts int `json:"retry_max_attempts"`

	// Max time to retry an item before dead-letter. Zero means unlimited.
	RetryWindow time.Duration `json:"retry_window"`

	// Retention bounds for persisted local status data.
	StatusRetention StatusRetentionConfig `json:"status_retention"`

	DiscordWebhooks DiscordWebhooks `json:"discord_webhooks"`
	SlackDelay      time.Duration   `json:"slack_delay"` // Configurable delay before Slack push
	SlackWebhooks   SlackWebhooks   `json:"slack_webhooks"`

	// Slack-compatible webhooks reuse Slack Block Kit payloads but allow
	// operator-approved self-hosted or sovereign endpoints.
	SlackCompatibleWebhookHosts []string                `json:"slack_compatible_webhook_hosts"`
	SlackCompatibleWebhooks     SlackCompatibleWebhooks `json:"slack_compatible_webhooks"`

	// Feed configuration
	Feeds FeedConfig `json:"feeds"`

	// Format configuration
	Format FormatConfig `json:"format"`
}

// LogRotation defines log rotation settings
type LogRotation struct {
	MaxSizeMB  int  `json:"max_size_mb"`  // Maximum size in MB before rotation
	MaxBackups int  `json:"max_backups"`  // Maximum number of old log files to keep
	MaxAgeDays int  `json:"max_age_days"` // Maximum number of days to retain log files
	Compress   bool `json:"compress"`     // Whether to compress old log files
}

// StatusRetentionConfig defines bounds for local JSON status data.
type StatusRetentionConfig struct {
	MaxAPISentItems    int                    `json:"max_api_sent_items"`
	MaxRSSParsedItems  int                    `json:"max_rss_parsed_items"`
	RSSParsedMaxAge    time.Duration          `json:"rss_parsed_max_age"`
	MaxRSSSentItems    int                    `json:"max_rss_sent_items"`
	MaxRetryQueueItems int                    `json:"max_retry_queue_items"`
	RetryQueueMaxAge   time.Duration          `json:"retry_queue_max_age"`
	MaxDeadLetterItems int                    `json:"max_dead_letter_items"`
	DeadLetterMaxAge   time.Duration          `json:"dead_letter_max_age"`
	AuditLogRotation   AuditLogRotationConfig `json:"audit_log_rotation"`
}

// AuditLogRotationConfig bounds delivery_audit.jsonl the same way LogRotation
// bounds the log file: size-triggered rotation, age/count-bounded backups.
type AuditLogRotationConfig struct {
	MaxSizeMB  int  `json:"max_size_mb"`
	MaxBackups int  `json:"max_backups"`
	MaxAgeDays int  `json:"max_age_days"`
	Compress   bool `json:"compress"`
}

// DiscordWebhooks configuration for Discord webhook URLs
type DiscordWebhooks struct {
	Ransomware WebhookConfig `json:"ransomware"`
	RSS        WebhookConfig `json:"rss"`
	Government WebhookConfig `json:"government"`
}

// SlackWebhooks configuration for Slack webhook URLs
type SlackWebhooks struct {
	Ransomware WebhookConfig `json:"ransomware"`
	RSS        WebhookConfig `json:"rss"`
	Government WebhookConfig `json:"government"`
}

// SlackCompatibleWebhooks configuration for approved Slack-compatible endpoints.
type SlackCompatibleWebhooks struct {
	Ransomware WebhookConfig `json:"ransomware"`
	RSS        WebhookConfig `json:"rss"`
	Government WebhookConfig `json:"government"`
}

// WebhookConfig represents one logical webhook target. URL keeps the legacy
// single-endpoint shape; URLs adds additional endpoints with the same filters
// and quiet-hours policy. Targets adds endpoint-specific overrides for
// operators that need independent filters or quiet-hours policies per URL.
// A Targets entry inherits this block's Filters and/or QuietHours,
// independently per key, whenever it omits that key -- exactly like URL/URLs
// already do. To deliver everything on one target
// regardless of the block's filters, set an explicit empty object,
// "filters": {} (non-nil, matches every entry) or "quiet_hours": {} (non-nil,
// never pauses delivery); both are pointer-typed for exactly this reason.
// Loading or reloading a config where a Targets entry newly inherits a value
// this way logs one WARN per block naming every affected endpoint
// (warnTargetInheritance).
type WebhookConfig struct {
	Enabled    bool            `json:"enabled"`
	URL        string          `json:"url"`
	URLs       []string        `json:"urls,omitempty"`
	Targets    []WebhookTarget `json:"targets,omitempty"`
	Filters    *WebhookFilters `json:"filters,omitempty"`
	QuietHours *QuietHours     `json:"quiet_hours,omitempty"`
}

// WebhookTarget is one endpoint-specific override entry under
// WebhookConfig.Targets. An entry that omits Filters and/or QuietHours
// inherits the parent WebhookConfig's value for that key, independently per
// key, the same way the URL/URLs endpoints built from the same block always
// have. Set an explicit "filters": {} / "quiet_hours": {}
// to opt a target out of the block's value for that key -- an empty, non-nil
// object matches everything / never pauses delivery, so it is not the same
// as omitting the key.
type WebhookTarget struct {
	URL        string          `json:"url"`
	Filters    *WebhookFilters `json:"filters,omitempty"`
	QuietHours *QuietHours     `json:"quiet_hours,omitempty"`
}

// QuietHours defines a time window during which message delivery is paused.
// nil or enabled=false means no quiet hours (always deliver). Messages remain
// unsent and are picked up by the next polling cycle after the window ends.
type QuietHours struct {
	Enabled  bool   `json:"enabled"`            // Must be true for quiet hours to take effect
	Start    string `json:"start"`              // 24h "HH:MM" (e.g. "22:00") or 12h (e.g. "10pm", "10:00 PM")
	End      string `json:"end"`                // 24h "HH:MM" (e.g. "07:00") or 12h (e.g. "7am", "7:00 AM")
	Timezone string `json:"timezone,omitempty"` // IANA timezone, e.g. "Europe/Berlin" (default: "UTC")
}

// WebhookFilters defines per-webhook include/exclude filter rules.
// All specified field groups are AND-combined; values within a group are OR-combined.
// nil (no filters block) means accept everything (backward-compatible default).
type WebhookFilters struct {
	// Include (whitelist) — if set, entry MUST match at least one value
	// Group, country, and activity filters apply to ransomware API webhooks only.
	// Category filters apply to RSS/government webhooks only. Keyword filters
	// apply to both API and RSS paths, but each path searches different fields.
	IncludeGroups     []string            `json:"include_groups,omitempty"`
	IncludeCountries  []string            `json:"include_countries,omitempty"`
	IncludeActivities []string            `json:"include_activities,omitempty"`
	IncludeKeywords   []string            `json:"include_keywords,omitempty"`
	IncludeCategories []string            `json:"include_categories,omitempty"` // RSS only
	IncludeFields     map[string][]string `json:"include_fields,omitempty"`

	// Exclude (blacklist) — if matched, entry is dropped
	// Field applicability follows the include filters above.
	ExcludeGroups     []string            `json:"exclude_groups,omitempty"`
	ExcludeCountries  []string            `json:"exclude_countries,omitempty"`
	ExcludeActivities []string            `json:"exclude_activities,omitempty"`
	ExcludeKeywords   []string            `json:"exclude_keywords,omitempty"`
	ExcludeCategories []string            `json:"exclude_categories,omitempty"` // RSS only
	ExcludeFields     map[string][]string `json:"exclude_fields,omitempty"`

	// keyword_match controls how keywords are matched: "literal" (default) or "regex"
	KeywordMatchMode string `json:"keyword_match,omitempty"`
}

// FeedConfig contains all RSS feed URLs organized by category
type FeedConfig struct {
	RansomwareFeeds []string `json:"ransomware_feeds"`
	GovernmentFeeds []string `json:"government_feeds"`
	GeneralFeeds    []string `json:"general_feeds"`
}

const (
	FeedTypeGeneral    = "general"
	FeedTypeGovernment = "government"
	FeedTypeRansomware = "ransomware"
)

type FeedGroup struct {
	Type     string
	JSONName string
	URLs     []string
}

func (f FeedConfig) Groups() []FeedGroup {
	return []FeedGroup{
		{Type: FeedTypeGeneral, JSONName: "general_feeds", URLs: f.GeneralFeeds},
		{Type: FeedTypeGovernment, JSONName: "government_feeds", URLs: f.GovernmentFeeds},
		{Type: FeedTypeRansomware, JSONName: "ransomware_feeds", URLs: f.RansomwareFeeds},
	}
}

func defaultStatusRetentionConfig() StatusRetentionConfig {
	policy := status.DefaultRetentionPolicy()
	return StatusRetentionConfig{
		MaxAPISentItems:    policy.MaxAPISentItems,
		MaxRSSParsedItems:  policy.MaxRSSParsedItems,
		RSSParsedMaxAge:    policy.RSSParsedMaxAge,
		MaxRSSSentItems:    policy.MaxRSSSentItems,
		MaxRetryQueueItems: policy.MaxRetryQueueItems,
		RetryQueueMaxAge:   policy.RetryQueueMaxAge,
		MaxDeadLetterItems: policy.MaxDeadLetterItems,
		DeadLetterMaxAge:   policy.DeadLetterMaxAge,
		AuditLogRotation: AuditLogRotationConfig{
			MaxSizeMB:  policy.AuditLogMaxSizeMB,
			MaxBackups: policy.AuditLogMaxBackups,
			MaxAgeDays: policy.AuditLogMaxAgeDays,
			Compress:   policy.AuditLogCompress != nil && *policy.AuditLogCompress,
		},
	}
}

// FormatConfig defines how messages should be formatted
type FormatConfig struct {
	ShowUnicodeFlags bool              `json:"show_unicode_flags"`
	DisplayLocale    string            `json:"display_locale,omitempty"`
	TimestampFormat  string            `json:"timestamp_format,omitempty"`
	DisplayTimezone  string            `json:"display_timezone,omitempty"`
	ShowEmptyFields  bool              `json:"show_empty_fields"` // Show placeholder for missing fields
	EmptyFieldText   string            `json:"empty_field_text"`  // Placeholder text for missing fields (e.g. "N/A")
	FieldOrder       []string          `json:"field_order"`       // Discord field order
	FieldLabels      map[string]string `json:"field_labels,omitempty"`
	// omitempty removed: encoding/json never treats a non-pointer struct value
	// as empty, so it was always a no-op here (these three fields always
	// marshal) -- see config_test.go TestFormatConfigOmitemptyOnStructFieldsIsInert.
	RSS     RSSFormatConfig     `json:"rss"`     // RSS-specific formatting
	Discord DiscordFormatConfig `json:"discord"` // Discord-specific formatting
	Slack   SlackFormatConfig   `json:"slack"`   // Slack-specific formatting
}

// DiscordFormatConfig defines Discord-specific formatting options.
type DiscordFormatConfig struct {
	ShowIcons           bool              `json:"show_icons"`
	RansomwareColor     string            `json:"ransomware_color"`
	RSSColor            string            `json:"rss_color"`
	GovernmentColor     string            `json:"government_color"`
	DescriptionMaxChars int               `json:"description_max_chars"`
	FieldLabels         map[string]string `json:"field_labels,omitempty"`
}

// SlackFormatConfig defines Slack-specific formatting options
type SlackFormatConfig struct {
	TitleText           string            `json:"title_text"`
	RSSText             string            `json:"rss_text"`
	FieldOrder          []string          `json:"field_order"`
	DescriptionMaxChars int               `json:"description_max_chars"`
	FieldLabels         map[string]string `json:"field_labels,omitempty"`
}

// RSSFormatConfig defines formatting options shared by Discord and Slack RSS messages.
type RSSFormatConfig struct {
	TitleText           string            `json:"title_text"`
	ShowAuthor          bool              `json:"show_author,omitempty"`
	FieldOrder          []string          `json:"field_order"`
	DescriptionMaxChars int               `json:"description_max_chars,omitempty"` // Optional; zero keeps legacy platform defaults
	FieldLabels         map[string]string `json:"field_labels,omitempty"`
}

const (
	DefaultSlackTitleText        = "Ransomware Alert"
	DefaultSlackRSSText          = "RSS Feed Update"
	DefaultRSSTitleText          = "RSS Feed Update"
	defaultAPIBaseURL            = "https://api-pro.ransomware.live"
	DefaultAPIRequestTimeout     = 30 * time.Second
	DefaultAPIMaxRetries         = 3
	DefaultAPIRetryDelay         = time.Second
	DefaultAPICheckTimeout       = 5 * time.Minute
	DefaultWebhookRequestTimeout = webhookhttp.RequestTimeout
	DefaultWebhookMaxRetries     = 3
	DefaultWebhookRetryDelay     = time.Second
	DefaultRSSCheckTimeout       = 10 * time.Minute
	DefaultDiscordRansomColor    = "#ff0000"
	DefaultDiscordRSSColor       = "#0099ff"
	DefaultDiscordGovColor       = "#ffa500"
	DefaultDescriptionMaxChars   = 500
)

func DefaultSlackFieldOrder() []string {
	return formatfields.DefaultSlackFieldOrder()
}

func DefaultDiscordFieldOrder() []string {
	return formatfields.DefaultDiscordFieldOrder()
}

func DefaultRSSFieldOrder() []string {
	return formatfields.DefaultRSSFieldOrder()
}

// generalLogRotation uses pointer fields for JSON parsing so that
// zero-values (0, false) can be distinguished from missing fields (nil).
type generalLogRotation struct {
	MaxSizeMB  *int  `json:"max_size_mb"`
	MaxBackups *int  `json:"max_backups"`
	MaxAgeDays *int  `json:"max_age_days"`
	Compress   *bool `json:"compress"`
}

// generalConfig represents the main configuration file structure.
// Pointer fields (*int) allow distinguishing "not set" (nil) from "explicitly 0".
type generalConfig struct {
	LogLevel          string             `json:"log_level"`
	LogFilePath       string             `json:"log_file_path"`
	LogRotation       generalLogRotation `json:"log_rotation"`
	MaxRSSWorkers     *int               `json:"max_rss_workers"`
	APIKey            string             `json:"api_key"`
	APIBaseURL        string             `json:"api_base_url"`
	APIRequestTimeout string             `json:"api_request_timeout"` // Will be parsed to time.Duration
	APIMaxRetries     *int               `json:"api_max_retries"`
	APIRetryDelay     string             `json:"api_retry_delay"`   // Will be parsed to time.Duration
	APIPollInterval   string             `json:"api_poll_interval"` // Will be parsed to time.Duration
	APICheckTimeout   string             `json:"api_check_timeout"` // Will be parsed to time.Duration
	RSSPollInterval   string             `json:"rss_poll_interval"`
	RSSCheckTimeout   string             `json:"rss_check_timeout"` // Will be parsed to time.Duration
	RSSRetryCount     *int               `json:"rss_retry_count"`
	RSSRetryDelay     string             `json:"rss_retry_delay"`    // Will be parsed to time.Duration
	RSSWorkerTimeout  string             `json:"rss_worker_timeout"` // Will be parsed to time.Duration

	// Will be parsed to time.Duration. Zero or empty disables RSS max age.
	RSSMaxItemAge string `json:"rss_max_item_age"`

	APIMaxEntriesPerCycle       *int                    `json:"max_api_entries_per_cycle"`
	RSSMaxEntriesPerCycle       *int                    `json:"max_rss_entries_per_cycle"`
	DataDir                     string                  `json:"data_dir"`      // Directory for persistent status data
	DiscordDelay                string                  `json:"discord_delay"` // Will be parsed to time.Duration
	DiscordWebhooks             DiscordWebhooks         `json:"discord_webhooks"`
	SlackDelay                  string                  `json:"slack_delay"` // Will be parsed to time.Duration
	SlackWebhooks               SlackWebhooks           `json:"slack_webhooks"`
	SlackCompatibleWebhookHosts []string                `json:"slack_compatible_webhook_hosts"`
	SlackCompatibleWebhooks     SlackCompatibleWebhooks `json:"slack_compatible_webhooks"`
	WebhookRequestTimeout       string                  `json:"webhook_request_timeout"`
	WebhookMaxRetries           *int                    `json:"webhook_max_retries"`
	WebhookRetryDelay           string                  `json:"webhook_retry_delay"`
	RetryMaxAttempts            *int                    `json:"retry_max_attempts"`
	RetryWindow                 string                  `json:"retry_window"` // Will be parsed to time.Duration
	StatusRetention             generalStatusRetention  `json:"status_retention"`
}

type generalStatusRetention struct {
	MaxAPISentItems    *int                    `json:"max_api_sent_items"`
	MaxRSSParsedItems  *int                    `json:"max_rss_parsed_items"`
	RSSParsedMaxAge    string                  `json:"rss_parsed_max_age"`
	MaxRSSSentItems    *int                    `json:"max_rss_sent_items"`
	MaxRetryQueueItems *int                    `json:"max_retry_queue_items"`
	RetryQueueMaxAge   string                  `json:"retry_queue_max_age"`
	MaxDeadLetterItems *int                    `json:"max_dead_letter_items"`
	DeadLetterMaxAge   string                  `json:"dead_letter_max_age"`
	AuditLogRotation   generalAuditLogRotation `json:"audit_log_rotation"`
}

// generalAuditLogRotation mirrors generalLogRotation: pointer fields so a
// zero value can be distinguished from an omitted key.
type generalAuditLogRotation struct {
	MaxSizeMB  *int  `json:"max_size_mb"`
	MaxBackups *int  `json:"max_backups"`
	MaxAgeDays *int  `json:"max_age_days"`
	Compress   *bool `json:"compress"`
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	statusRetention := defaultStatusRetentionConfig()
	return &Config{
		LogLevel:      "INFO",
		LogFilePath:   "./logs/bot.log",
		MaxRSSWorkers: 5, // Default number of RSS workers
		LogRotation: LogRotation{
			MaxSizeMB:  10,   // 10 MB per file
			MaxBackups: 30,   // Keep 30 old files for incident reconstruction
			MaxAgeDays: 90,   // Delete files older than 90 days
			Compress:   true, // Compress old files
		},
		APIBaseURL:            defaultAPIBaseURL,
		APIRequestTimeout:     DefaultAPIRequestTimeout,
		APIMaxRetries:         DefaultAPIMaxRetries,
		APIRetryDelay:         DefaultAPIRetryDelay,
		APIPollInterval:       time.Hour,
		APICheckTimeout:       DefaultAPICheckTimeout,
		RSSPollInterval:       30 * time.Minute, // NEW: Default RSS poll interval
		RSSCheckTimeout:       DefaultRSSCheckTimeout,
		RSSRetryCount:         3,
		RSSRetryDelay:         2 * time.Second,
		RSSWorkerTimeout:      30 * time.Second, // Default 30 seconds timeout for RSS worker pool
		APIMaxEntriesPerCycle: 100,
		RSSMaxEntriesPerCycle: 100,
		DataDir:               "./data",        // Default data directory (resolved to absolute path at load time)
		DiscordDelay:          2 * time.Second, // Default 2 seconds delay before Discord push
		WebhookRequestTimeout: DefaultWebhookRequestTimeout,
		WebhookMaxRetries:     DefaultWebhookMaxRetries,
		WebhookRetryDelay:     DefaultWebhookRetryDelay,
		RetryMaxAttempts:      5,              // Default 5 retries after the first send (six sends per item)
		RetryWindow:           24 * time.Hour, // Default 24h retry window
		StatusRetention:       statusRetention,
		DiscordWebhooks: DiscordWebhooks{
			Ransomware: WebhookConfig{Enabled: false, URL: ""},
			RSS:        WebhookConfig{Enabled: false, URL: ""},
			Government: WebhookConfig{Enabled: false, URL: ""},
		},
		SlackDelay: 2 * time.Second, // Default 2 seconds delay before Slack push
		SlackWebhooks: SlackWebhooks{
			Ransomware: WebhookConfig{Enabled: false, URL: ""},
			RSS:        WebhookConfig{Enabled: false, URL: ""},
			Government: WebhookConfig{Enabled: false, URL: ""},
		},
		SlackCompatibleWebhookHosts: []string{},
		SlackCompatibleWebhooks: SlackCompatibleWebhooks{
			Ransomware: WebhookConfig{Enabled: false, URL: ""},
			RSS:        WebhookConfig{Enabled: false, URL: ""},
			Government: WebhookConfig{Enabled: false, URL: ""},
		},
		Feeds: FeedConfig{
			RansomwareFeeds: []string{},
			GovernmentFeeds: []string{},
			GeneralFeeds:    []string{},
		},
		Format: FormatConfig{
			ShowUnicodeFlags: true,
			DisplayLocale:    "en",
			TimestampFormat:  "2006-01-02 15:04:05 MST",
			DisplayTimezone:  "UTC",
			ShowEmptyFields:  false,
			EmptyFieldText:   "N/A",
			FieldOrder:       DefaultDiscordFieldOrder(),
			RSS: RSSFormatConfig{
				TitleText:  DefaultRSSTitleText,
				FieldOrder: DefaultRSSFieldOrder(),
			},
			Discord: DiscordFormatConfig{
				ShowIcons:           true,
				RansomwareColor:     DefaultDiscordRansomColor,
				RSSColor:            DefaultDiscordRSSColor,
				GovernmentColor:     DefaultDiscordGovColor,
				DescriptionMaxChars: DefaultDescriptionMaxChars,
			},
			Slack: SlackFormatConfig{
				TitleText:           DefaultSlackTitleText,
				RSSText:             DefaultSlackRSSText,
				FieldOrder:          DefaultSlackFieldOrder(),
				DescriptionMaxChars: DefaultDescriptionMaxChars,
			},
		},
	}
}
