package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/keywordmode"
)

//nolint:gocyclo // long sequential assertion list over every DefaultConfig() field; not a table split candidate
func TestDefaultConfigValues(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.LogLevel != "INFO" {
		t.Errorf("expected LogLevel INFO, got %q", cfg.LogLevel)
	}
	if cfg.LogFilePath != "./logs/bot.log" {
		t.Errorf("expected LogFilePath ./logs/bot.log, got %q", cfg.LogFilePath)
	}
	if cfg.MaxRSSWorkers != 5 {
		t.Errorf("expected MaxRSSWorkers 5, got %d", cfg.MaxRSSWorkers)
	}
	if cfg.APIBaseURL != defaultAPIBaseURL {
		t.Errorf("expected APIBaseURL %q, got %q", defaultAPIBaseURL, cfg.APIBaseURL)
	}
	if cfg.APIRequestTimeout != DefaultAPIRequestTimeout {
		t.Errorf("expected APIRequestTimeout %v, got %v", DefaultAPIRequestTimeout, cfg.APIRequestTimeout)
	}
	if cfg.APIMaxRetries != DefaultAPIMaxRetries {
		t.Errorf("expected APIMaxRetries %d, got %d", DefaultAPIMaxRetries, cfg.APIMaxRetries)
	}
	if cfg.APIRetryDelay != DefaultAPIRetryDelay {
		t.Errorf("expected APIRetryDelay %v, got %v", DefaultAPIRetryDelay, cfg.APIRetryDelay)
	}
	if cfg.APIPollInterval != time.Hour {
		t.Errorf("expected APIPollInterval 1h, got %v", cfg.APIPollInterval)
	}
	if cfg.APICheckTimeout != 5*time.Minute {
		t.Errorf("expected APICheckTimeout 5m, got %v", cfg.APICheckTimeout)
	}
	if cfg.RSSPollInterval != 30*time.Minute {
		t.Errorf("expected RSSPollInterval 30m, got %v", cfg.RSSPollInterval)
	}
	if cfg.RSSCheckTimeout != 10*time.Minute {
		t.Errorf("expected RSSCheckTimeout 10m, got %v", cfg.RSSCheckTimeout)
	}
	if cfg.DiscordDelay != 2*time.Second {
		t.Errorf("expected DiscordDelay 2s, got %v", cfg.DiscordDelay)
	}
	if cfg.SlackDelay != 2*time.Second {
		t.Errorf("expected SlackDelay 2s, got %v", cfg.SlackDelay)
	}
	if cfg.WebhookRequestTimeout != DefaultWebhookRequestTimeout {
		t.Errorf("expected WebhookRequestTimeout %v, got %v", DefaultWebhookRequestTimeout, cfg.WebhookRequestTimeout)
	}
	if cfg.WebhookMaxRetries != DefaultWebhookMaxRetries {
		t.Errorf("expected WebhookMaxRetries %d, got %d", DefaultWebhookMaxRetries, cfg.WebhookMaxRetries)
	}
	if cfg.WebhookRetryDelay != DefaultWebhookRetryDelay {
		t.Errorf("expected WebhookRetryDelay %v, got %v", DefaultWebhookRetryDelay, cfg.WebhookRetryDelay)
	}
	if cfg.RSSRetryCount != 3 {
		t.Errorf("expected RSSRetryCount 3, got %d", cfg.RSSRetryCount)
	}
	if cfg.RSSRetryDelay != 2*time.Second {
		t.Errorf("expected RSSRetryDelay 2s, got %v", cfg.RSSRetryDelay)
	}
	if cfg.RSSWorkerTimeout != 30*time.Second {
		t.Errorf("expected RSSWorkerTimeout 30s, got %v", cfg.RSSWorkerTimeout)
	}
	if cfg.APIMaxEntriesPerCycle != 100 {
		t.Errorf("expected APIMaxEntriesPerCycle 100, got %d", cfg.APIMaxEntriesPerCycle)
	}
	if cfg.RSSMaxEntriesPerCycle != 100 {
		t.Errorf("expected RSSMaxEntriesPerCycle 100, got %d", cfg.RSSMaxEntriesPerCycle)
	}
	if !cfg.Format.ShowUnicodeFlags {
		t.Error("expected ShowUnicodeFlags true")
	}
	if cfg.Format.DisplayLocale != "en" {
		t.Errorf("expected Format.DisplayLocale en, got %q", cfg.Format.DisplayLocale)
	}
	if cfg.Format.TimestampFormat != "2006-01-02 15:04:05 MST" {
		t.Errorf("expected default TimestampFormat, got %q", cfg.Format.TimestampFormat)
	}
	if cfg.Format.DisplayTimezone != "UTC" {
		t.Errorf("expected DisplayTimezone UTC, got %q", cfg.Format.DisplayTimezone)
	}
	if cfg.Format.ShowEmptyFields {
		t.Error("expected ShowEmptyFields false")
	}
}

func TestDefaultConfigWebhooksDisabled(t *testing.T) {
	cfg := DefaultConfig()

	webhooks := []struct {
		name    string
		enabled bool
	}{
		{"Discord.Ransomware", cfg.DiscordWebhooks.Ransomware.Enabled},
		{"Discord.RSS", cfg.DiscordWebhooks.RSS.Enabled},
		{"Discord.Government", cfg.DiscordWebhooks.Government.Enabled},
		{"Slack.Ransomware", cfg.SlackWebhooks.Ransomware.Enabled},
		{"Slack.RSS", cfg.SlackWebhooks.RSS.Enabled},
		{"Slack.Government", cfg.SlackWebhooks.Government.Enabled},
	}

	for _, wh := range webhooks {
		if wh.enabled {
			t.Errorf("expected %s webhook to be disabled by default", wh.name)
		}
	}
}

func TestWebhookTargetsEnumeratesAllConfiguredTargets(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DiscordWebhooks.RSS.URL = "https://discord.com/api/webhooks/123/rss"
	cfg.SlackWebhooks.Ransomware.URL = "https://hooks.slack.com/services/T/B/ransom"

	targets := WebhookTargets(cfg)
	if len(targets) != 9 {
		t.Fatalf("WebhookTargets() count = %d, want 9", len(targets))
	}

	got := make(map[string]WebhookConfig, len(targets))
	for _, target := range targets {
		got[target.QualifiedName()] = target.Webhook
	}

	for _, want := range []string{
		"discord.ransomware",
		"discord.rss",
		"discord.government",
		"slack.ransomware",
		"slack.rss",
		"slack.government",
		"slack_compatible.ransomware",
		"slack_compatible.rss",
		"slack_compatible.government",
	} {
		if _, ok := got[want]; !ok {
			t.Fatalf("WebhookTargets() missing %q in %#v", want, got)
		}
	}
	if got["discord.rss"].URL != cfg.DiscordWebhooks.RSS.URL {
		t.Fatalf("discord.rss URL = %q, want configured URL", got["discord.rss"].URL)
	}
	if got["slack.ransomware"].URL != cfg.SlackWebhooks.Ransomware.URL {
		t.Fatalf("slack.ransomware URL = %q, want configured URL", got["slack.ransomware"].URL)
	}
}

func TestWebhookTargetsForTypeFiltersAcrossPlatforms(t *testing.T) {
	cfg := DefaultConfig()

	targets := WebhookTargetsForType(cfg, WebhookTypeRansomware)
	if len(targets) != 3 {
		t.Fatalf("WebhookTargetsForType() count = %d, want 3", len(targets))
	}

	gotPlatforms := map[string]bool{}
	for _, target := range targets {
		if target.Name != WebhookTypeRansomware {
			t.Fatalf("target name = %q, want %q", target.Name, WebhookTypeRansomware)
		}
		gotPlatforms[target.Platform] = true
	}
	if !gotPlatforms[WebhookPlatformDiscord] ||
		!gotPlatforms[WebhookPlatformSlack] ||
		!gotPlatforms[WebhookPlatformSlackCompatible] {
		t.Fatalf("WebhookTargetsForType() platforms = %#v, want discord, slack, and slack-compatible", gotPlatforms)
	}
}

func TestWebhookTargetsExpandsMultipleURLsWithStableDestinationSuffixes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DiscordWebhooks.RSS = WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123/primary",
		URLs: []string{
			"https://discord.com/api/webhooks/123/secondary",
			"https://discord.com/api/webhooks/123/tertiary",
		},
	}

	targets := WebhookTargetsForType(cfg, WebhookTypeRSS)

	got := map[string]string{}
	for _, target := range targets {
		if target.Platform != WebhookPlatformDiscord {
			continue
		}
		got[target.DestinationSuffix] = target.Webhook.URL
	}
	want := map[string]string{
		"":  "https://discord.com/api/webhooks/123/primary",
		"2": "https://discord.com/api/webhooks/123/secondary",
		"3": "https://discord.com/api/webhooks/123/tertiary",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("expanded Discord RSS targets = %#v, want %#v", got, want)
	}
}

func TestWebhookTargetsSupportsEndpointSpecificFilters(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SlackWebhooks.Ransomware = WebhookConfig{
		Enabled: true,
		Targets: []WebhookTarget{
			{
				URL: "https://hooks.slack.com/services/T/B/security",
				Filters: &WebhookFilters{
					IncludeCountries: []string{"DE"},
				},
			},
			{
				URL: "https://hooks.slack.com/services/T/B/healthcare",
				Filters: &WebhookFilters{
					IncludeKeywords: []string{"hospital"},
				},
				QuietHours: &QuietHours{
					Enabled:  true,
					Start:    "22:00",
					End:      "06:00",
					Timezone: "Europe/Berlin",
				},
			},
		},
	}

	targets := WebhookTargetsForType(cfg, WebhookTypeRansomware)

	got := map[string]WebhookConfig{}
	for _, target := range targets {
		if target.Platform == WebhookPlatformSlack {
			got[target.DestinationSuffix] = target.Webhook
		}
	}
	if got[""].URL != "https://hooks.slack.com/services/T/B/security" {
		t.Fatalf("primary Slack target URL = %q", got[""].URL)
	}
	if fmt.Sprint(got[""].Filters.IncludeCountries) != "[DE]" {
		t.Fatalf("primary Slack filters = %#v, want include country DE", got[""].Filters)
	}
	if got["2"].URL != "https://hooks.slack.com/services/T/B/healthcare" {
		t.Fatalf("second Slack target URL = %q", got["2"].URL)
	}
	if fmt.Sprint(got["2"].Filters.IncludeKeywords) != "[hospital]" {
		t.Fatalf("second Slack filters = %#v, want include keyword hospital", got["2"].Filters)
	}
	if got["2"].QuietHours == nil || got["2"].QuietHours.Timezone != "Europe/Berlin" {
		t.Fatalf("second Slack quiet hours = %#v, want Europe/Berlin override", got["2"].QuietHours)
	}
}

func TestValidateSlackCompatibleWebhookAllowsExplicitApprovedHost(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SlackCompatibleWebhookHosts = []string{"hooks.eu.example"}
	cfg.SlackCompatibleWebhooks.RSS = WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.eu.example/services/ransomware-bot/rss",
	}

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestValidateSlackCompatibleWebhookRejectsUnapprovedHost(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SlackCompatibleWebhookHosts = []string{"hooks.eu.example"}
	cfg.SlackCompatibleWebhooks.RSS = WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.other.example/services/ransomware-bot/rss",
	}

	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("validateConfig() succeeded with unapproved slack-compatible host")
	}
	if !strings.Contains(err.Error(), "approved host") {
		t.Fatalf("validateConfig() error = %v, want approved host context", err)
	}
}

func TestDefaultConfigFieldOrder(t *testing.T) {
	cfg := DefaultConfig()

	expectedFields := []string{
		"group", "victim", "country", "activity", "discovered", "post_url",
	}

	if len(cfg.Format.FieldOrder) != len(expectedFields) {
		t.Fatalf("expected %d fields, got %d", len(expectedFields), len(cfg.Format.FieldOrder))
	}

	for i, expected := range expectedFields {
		if cfg.Format.FieldOrder[i] != expected {
			t.Errorf("FieldOrder[%d] = %q, want %q", i, cfg.Format.FieldOrder[i], expected)
		}
	}
}

func TestDefaultConfigSlackFormatDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Format.Slack.TitleText != DefaultSlackTitleText {
		t.Fatalf("Slack TitleText = %q, want %q", cfg.Format.Slack.TitleText, DefaultSlackTitleText)
	}
	if cfg.Format.Slack.RSSText != DefaultSlackRSSText {
		t.Fatalf("Slack RSSText = %q, want %q", cfg.Format.Slack.RSSText, DefaultSlackRSSText)
	}
	if got, want := strings.Join(cfg.Format.Slack.FieldOrder, ","), strings.Join(DefaultSlackFieldOrder(), ","); got != want {
		t.Fatalf("Slack FieldOrder = %q, want %q", got, want)
	}
	if cfg.Format.Slack.DescriptionMaxChars != DefaultDescriptionMaxChars {
		t.Fatalf("Slack DescriptionMaxChars = %d, want %d", cfg.Format.Slack.DescriptionMaxChars, DefaultDescriptionMaxChars)
	}
}

func TestDefaultConfigRSSFormatDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Format.RSS.TitleText != DefaultRSSTitleText {
		t.Fatalf("RSS TitleText = %q, want %q", cfg.Format.RSS.TitleText, DefaultRSSTitleText)
	}
	if got, want := strings.Join(cfg.Format.RSS.FieldOrder, ","), strings.Join(DefaultRSSFieldOrder(), ","); got != want {
		t.Fatalf("RSS FieldOrder = %q, want %q", got, want)
	}
	if cfg.Format.RSS.DescriptionMaxChars != 0 {
		t.Fatalf("RSS DescriptionMaxChars = %d, want zero legacy default", cfg.Format.RSS.DescriptionMaxChars)
	}
}

func TestDefaultConfigDiscordFormatDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.Format.Discord.ShowIcons {
		t.Fatal("Discord ShowIcons should default to true")
	}
	if cfg.Format.Discord.RansomwareColor != DefaultDiscordRansomColor {
		t.Fatalf("Discord RansomwareColor = %q, want %q", cfg.Format.Discord.RansomwareColor, DefaultDiscordRansomColor)
	}
	if cfg.Format.Discord.RSSColor != DefaultDiscordRSSColor {
		t.Fatalf("Discord RSSColor = %q, want %q", cfg.Format.Discord.RSSColor, DefaultDiscordRSSColor)
	}
	if cfg.Format.Discord.GovernmentColor != DefaultDiscordGovColor {
		t.Fatalf("Discord GovernmentColor = %q, want %q", cfg.Format.Discord.GovernmentColor, DefaultDiscordGovColor)
	}
	if cfg.Format.Discord.DescriptionMaxChars != DefaultDescriptionMaxChars {
		t.Fatalf("Discord DescriptionMaxChars = %d, want %d", cfg.Format.Discord.DescriptionMaxChars, DefaultDescriptionMaxChars)
	}
}

func TestDefaultConfigLogRotation(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.LogRotation.MaxSizeMB != 10 {
		t.Errorf("expected MaxSizeMB 10, got %d", cfg.LogRotation.MaxSizeMB)
	}
	if cfg.LogRotation.MaxBackups != 30 {
		t.Errorf("expected MaxBackups 30, got %d", cfg.LogRotation.MaxBackups)
	}
	if cfg.LogRotation.MaxAgeDays != 90 {
		t.Errorf("expected MaxAgeDays 90, got %d", cfg.LogRotation.MaxAgeDays)
	}
	if !cfg.LogRotation.Compress {
		t.Error("expected Compress true")
	}
}

func TestValidateConfigDefault(t *testing.T) {
	cfg := DefaultConfig()
	if err := validateConfig(cfg); err != nil {
		t.Errorf("default config should be valid, got error: %v", err)
	}
}

func TestValidateConfigInvalidLogLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LogLevel = "INVALID"
	if err := validateConfig(cfg); err == nil {
		t.Error("expected error for invalid log level")
	}
}

func TestValidateConfigAllowsWarnLogLevelAlias(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LogLevel = "WARN"
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestValidateConfigAllowsTraceLogLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LogLevel = "TRACE"
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestValidateConfigNormalizesAPIBaseURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIBaseURL = " https://sovereign.example/api/ "

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
	if cfg.APIBaseURL != "https://sovereign.example/api" {
		t.Fatalf("APIBaseURL = %q, want normalized URL without trailing slash", cfg.APIBaseURL)
	}
}

func TestValidateConfigLogRotationBounds(t *testing.T) {
	tests := []struct {
		name       string
		maxSize    int
		maxBackups int
		maxAge     int
		shouldErr  bool
	}{
		{"valid", 10, 5, 7, false},
		{"valid backups only", 10, 5, 0, false},
		{"valid max age only", 10, 0, 7, false},
		{"size too small", 0, 5, 7, true},
		{"size too large", 1001, 5, 7, true},
		{"backups negative", 10, -1, 7, true},
		{"backups too large", 10, 51, 7, true},
		{"age negative", 10, 5, -1, true},
		{"age too large", 10, 5, 366, true},
		{"unbounded retention", 10, 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.LogRotation.MaxSizeMB = tt.maxSize
			cfg.LogRotation.MaxBackups = tt.maxBackups
			cfg.LogRotation.MaxAgeDays = tt.maxAge
			err := validateConfig(cfg)
			if tt.shouldErr && err == nil {
				t.Error("expected error")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateConfigIntervals(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(*Config)
		shouldErr bool
	}{
		{"api poll too short", func(c *Config) { c.APIPollInterval = 30 * time.Second }, true},
		{"api check timeout too short", func(c *Config) { c.APICheckTimeout = 30 * time.Second }, true},
		{"api request timeout too short", func(c *Config) { c.APIRequestTimeout = 500 * time.Millisecond }, true},
		{"api request timeout too long", func(c *Config) { c.APIRequestTimeout = 10 * time.Minute }, true},
		{"api max retries too low", func(c *Config) { c.APIMaxRetries = 0 }, true},
		{"api max retries too high", func(c *Config) { c.APIMaxRetries = 11 }, true},
		{"api retry delay too short", func(c *Config) { c.APIRetryDelay = 50 * time.Millisecond }, true},
		{"api retry delay too long", func(c *Config) { c.APIRetryDelay = 2 * time.Minute }, true},
		{"webhook request timeout too short", func(c *Config) { c.WebhookRequestTimeout = 500 * time.Millisecond }, true},
		{"webhook request timeout too long", func(c *Config) { c.WebhookRequestTimeout = 10 * time.Minute }, true},
		{"webhook max retries too low", func(c *Config) { c.WebhookMaxRetries = 0 }, true},
		{"webhook max retries too high", func(c *Config) { c.WebhookMaxRetries = 11 }, true},
		{"webhook retry delay too short", func(c *Config) { c.WebhookRetryDelay = 50 * time.Millisecond }, true},
		{"webhook retry delay too long", func(c *Config) { c.WebhookRetryDelay = 2 * time.Minute }, true},
		{"rss poll too short", func(c *Config) { c.RSSPollInterval = 30 * time.Second }, true},
		{"rss check timeout too short", func(c *Config) { c.RSSCheckTimeout = 30 * time.Second }, true},
		{"rss retry delay too short", func(c *Config) { c.RSSRetryDelay = 500 * time.Millisecond }, true},
		{"discord delay too short", func(c *Config) { c.DiscordDelay = 100 * time.Millisecond }, true},
		{"discord delay too long", func(c *Config) { c.DiscordDelay = 60 * time.Second }, true},
		{"slack delay too short", func(c *Config) { c.SlackDelay = 500 * time.Millisecond }, true},
		{"slack delay too long", func(c *Config) { c.SlackDelay = 60 * time.Second }, true},
		{"workers too few", func(c *Config) { c.MaxRSSWorkers = 0 }, true},
		{"workers too many", func(c *Config) { c.MaxRSSWorkers = 11 }, true},
		{"retry count negative", func(c *Config) { c.RSSRetryCount = -1 }, true},
		{"retry count too high", func(c *Config) { c.RSSRetryCount = 11 }, true},
		{"send retry attempts negative", func(c *Config) { c.RetryMaxAttempts = -1 }, true},
		{"send retry attempts too high", func(c *Config) { c.RetryMaxAttempts = 101 }, true},
		{"send retry window negative", func(c *Config) { c.RetryWindow = -time.Hour }, true},
		{"rss max item age negative", func(c *Config) { c.RSSMaxItemAge = -time.Hour }, true},
		{"api max entries too low", func(c *Config) { c.APIMaxEntriesPerCycle = 0 }, true},
		{"api max entries too high", func(c *Config) { c.APIMaxEntriesPerCycle = 1001 }, true},
		{"rss max entries too low", func(c *Config) { c.RSSMaxEntriesPerCycle = 0 }, true},
		{"rss max entries too high", func(c *Config) { c.RSSMaxEntriesPerCycle = 1001 }, true},
		{"worker timeout too short", func(c *Config) { c.RSSWorkerTimeout = 2 * time.Second }, true},
		{"worker timeout too long", func(c *Config) { c.RSSWorkerTimeout = 10 * time.Minute }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.modify(cfg)
			err := validateConfig(cfg)
			if tt.shouldErr && err == nil {
				t.Error("expected error")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateConfigRejectsUnboundedPersistentRetries(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RetryMaxAttempts = 0
	cfg.RetryWindow = 0

	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("expected error when retry_max_attempts and retry_window are both unlimited")
	}
	if !strings.Contains(err.Error(), "cannot both be unlimited") {
		t.Fatalf("error = %v, want unbounded retry validation error", err)
	}
}

func TestValidateConfigRequiresRealAPIKeyForRansomwareWebhooks(t *testing.T) {
	tests := []struct {
		name   string
		apiKey string
	}{
		{name: "empty"},
		{name: "placeholder your", apiKey: "YOUR_RANSOMWARELIVE_API_KEY"},
		{name: "placeholder here", apiKey: "put-key-here"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.APIKey = tt.apiKey
			cfg.DiscordWebhooks.Ransomware = WebhookConfig{
				Enabled: true,
				URL:     "https://discord.com/api/webhooks/123/abc",
			}

			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("expected api_key validation error")
			}
			if !strings.Contains(err.Error(), "api_key") {
				t.Fatalf("error = %v, want api_key context", err)
			}
		})
	}
}

func TestValidateConfigAllowsRSSOnlyWithoutAPIKey(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIKey = ""
	cfg.DiscordWebhooks.RSS = WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123/abc",
	}

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestValidateConfigAllowsRansomwareWebhookWithRealAPIKey(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIKey = "live_test_key"
	cfg.DiscordWebhooks.Ransomware = WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123/abc",
	}

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestValidateConfigAllowsOnePersistentRetryBound(t *testing.T) {
	tests := []struct {
		name             string
		retryMaxAttempts int
		retryWindow      time.Duration
	}{
		{name: "attempts only", retryMaxAttempts: 5, retryWindow: 0},
		{name: "window only", retryMaxAttempts: 0, retryWindow: time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.RetryMaxAttempts = tt.retryMaxAttempts
			cfg.RetryWindow = tt.retryWindow
			if err := validateConfig(cfg); err != nil {
				t.Fatalf("validateConfig() error = %v", err)
			}
		})
	}
}

func TestValidateConfigRejectsDuplicateFieldOrderAliases(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Config)
	}{
		{
			name: "discord aliases",
			modify: func(cfg *Config) {
				cfg.Format.FieldOrder = []string{"post_url", "claim_url"}
			},
		},
		{
			name: "slack aliases",
			modify: func(cfg *Config) {
				cfg.Format.Slack.FieldOrder = []string{"website", "url"}
			},
		},
		{
			name: "discord duplicate",
			modify: func(cfg *Config) {
				cfg.Format.FieldOrder = []string{"victim", "victim"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.modify(cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("expected duplicate field_order validation error")
			}
			if !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("error = %v, want duplicate field_order error", err)
			}
		})
	}
}

func TestValidateConfigRejectsEmptyFieldOrder(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Config)
	}{
		{
			name: "discord",
			modify: func(cfg *Config) {
				cfg.Format.FieldOrder = []string{}
			},
		},
		{
			name: "slack",
			modify: func(cfg *Config) {
				cfg.Format.Slack.FieldOrder = []string{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.modify(cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("expected empty field_order validation error")
			}
			if !strings.Contains(err.Error(), "cannot be empty") {
				t.Fatalf("error = %v, want empty field_order error", err)
			}
		})
	}
}

func TestValidateConfigAllowsDocumentedIDAndPublishedFields(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Format.FieldOrder = []string{"id", "published", "website"}
	cfg.Format.Slack.FieldOrder = []string{"id", "published", "website"}

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestValidateConfigRejectsFieldOrderWithoutActionLinks(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Config)
	}{
		{
			name: "discord",
			modify: func(cfg *Config) {
				cfg.Format.FieldOrder = []string{"group", "victim", "country"}
			},
		},
		{
			name: "slack",
			modify: func(cfg *Config) {
				cfg.Format.Slack.FieldOrder = []string{"group", "victim", "country"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.modify(cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("expected action-link field_order validation error")
			}
			if !strings.Contains(err.Error(), "action link") {
				t.Fatalf("error = %v, want action-link field_order error", err)
			}
		})
	}
}

func TestValidateConfigRejectsInvalidFormatOptions(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Config)
		want      string
	}{
		{
			name: "invalid ransomware color",
			configure: func(cfg *Config) {
				cfg.Format.Discord.RansomwareColor = "red"
			},
			want: "discord.ransomware_color",
		},
		{
			name: "discord description too small",
			configure: func(cfg *Config) {
				cfg.Format.Discord.DescriptionMaxChars = 10
			},
			want: "discord.description_max_chars",
		},
		{
			name: "discord description too large",
			configure: func(cfg *Config) {
				cfg.Format.Discord.DescriptionMaxChars = 2000
			},
			want: "discord.description_max_chars",
		},
		{
			name: "slack description too small",
			configure: func(cfg *Config) {
				cfg.Format.Slack.DescriptionMaxChars = 10
			},
			want: "slack.description_max_chars",
		},
		{
			name: "rss field order rejects api field",
			configure: func(cfg *Config) {
				cfg.Format.RSS.FieldOrder = []string{"title", "victim"}
			},
			want: "rss.field_order",
		},
		{
			name: "rss description too large",
			configure: func(cfg *Config) {
				cfg.Format.RSS.DescriptionMaxChars = 4000
			},
			want: "rss.description_max_chars",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.configure(cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("expected format validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateConfigSkipsDisabledWebhookFilters(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DiscordWebhooks.RSS.Filters = &WebhookFilters{
		KeywordMatchMode: keywordmode.Regex,
		IncludeKeywords:  []string{"["},
	}

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() with disabled invalid filters error = %v", err)
	}

	cfg.DiscordWebhooks.RSS.Enabled = true
	cfg.DiscordWebhooks.RSS.URL = "https://discord.com/api/webhooks/123/abc"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("validateConfig() accepted invalid filters on enabled webhook")
	}
}

func TestValidateWebhookFilters(t *testing.T) {
	tests := []struct {
		name         string
		filters      *WebhookFilters
		wantErr      bool
		wantContains []string
	}{
		{
			name:    "nil filters",
			filters: nil,
		},
		{
			name: "valid literal filters",
			filters: &WebhookFilters{
				KeywordMatchMode: keywordmode.Literal,
				IncludeCountries: []string{"DE"},
				ExcludeCountries: []string{"US"},
				IncludeKeywords:  []string{"lockbit"},
			},
		},
		{
			name: "valid category filters trim whitespace",
			filters: &WebhookFilters{
				IncludeCategories: []string{" Malware "},
				ExcludeCategories: []string{" Spam "},
			},
		},
		{
			name: "valid regex filters",
			filters: &WebhookFilters{
				KeywordMatchMode: keywordmode.Regex,
				IncludeKeywords:  []string{`(?i)lockbit|blackcat`},
				ExcludeKeywords:  []string{`(?i)test victim`},
			},
		},
		{
			name: "invalid keyword mode",
			filters: &WebhookFilters{
				KeywordMatchMode: "glob",
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "keyword_match"},
		},
		{
			name: "valid lowercase include country",
			filters: &WebhookFilters{
				IncludeCountries: []string{"de"},
			},
		},
		{
			name: "invalid exclude country",
			filters: &WebhookFilters{
				ExcludeCountries: []string{"D"},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "country"},
		},
		{
			name: "invalid include regex",
			filters: &WebhookFilters{
				KeywordMatchMode: keywordmode.Regex,
				IncludeKeywords:  []string{"["},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "include_keywords"},
		},
		{
			name: "invalid exclude regex",
			filters: &WebhookFilters{
				KeywordMatchMode: keywordmode.Regex,
				ExcludeKeywords:  []string{"("},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "exclude_keywords"},
		},
		{
			name: "blank include category",
			filters: &WebhookFilters{
				IncludeCategories: []string{"security", " "},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "include_categories[1]", "blank"},
		},
		{
			name: "blank exclude category",
			filters: &WebhookFilters{
				ExcludeCategories: []string{""},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "exclude_categories[0]", "blank"},
		},
		{
			name: "blank include keyword",
			filters: &WebhookFilters{
				IncludeKeywords: []string{"ransomware", " "},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "include_keywords[1]", "blank"},
		},
		{
			name: "blank exclude keyword",
			filters: &WebhookFilters{
				ExcludeKeywords: []string{""},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "exclude_keywords[0]", "blank"},
		},
		{
			name: "blank group",
			filters: &WebhookFilters{
				IncludeGroups: []string{"lockbit", " "},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "include_groups[1]", "blank"},
		},
		{
			name: "too many keyword values",
			filters: &WebhookFilters{
				IncludeKeywords: makeStrings(maxWebhookFilterValues+1, "ransomware"),
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "include_keywords", "maximum"},
		},
		{
			name: "literal keyword too long",
			filters: &WebhookFilters{
				IncludeKeywords: []string{strings.Repeat("a", maxKeywordValueRunes+1)},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "include_keywords[0]", "exceeds"},
		},
		{
			name: "regex pattern too long",
			filters: &WebhookFilters{
				KeywordMatchMode: keywordmode.Regex,
				IncludeKeywords:  []string{strings.Repeat("a", maxRegexPatternRunes+1)},
			},
			wantErr:      true,
			wantContains: []string{"discord.rss", "include_keywords[0]", "exceeds"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWebhookFilters("discord.rss", tt.filters)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected validation error")
				}
				for _, want := range tt.wantContains {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error = %q, want substring %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
			if tt.name == "valid category filters trim whitespace" {
				if got := tt.filters.IncludeCategories[0]; got != "Malware" {
					t.Fatalf("include category = %q, want trimmed", got)
				}
				if got := tt.filters.ExcludeCategories[0]; got != "Spam" {
					t.Fatalf("exclude category = %q, want trimmed", got)
				}
			}
		})
	}
}

func TestValidateWebhookFiltersNormalizesCountryCodes(t *testing.T) {
	filters := &WebhookFilters{
		IncludeCountries: []string{"us"},
		ExcludeCountries: []string{" de "},
	}

	if err := validateWebhookFilters("discord.rss", filters); err != nil {
		t.Fatalf("validateWebhookFilters() error = %v", err)
	}
	if got := filters.IncludeCountries; len(got) != 1 || got[0] != "US" {
		t.Fatalf("IncludeCountries = %#v, want normalized US", got)
	}
	if got := filters.ExcludeCountries; len(got) != 1 || got[0] != "DE" {
		t.Fatalf("ExcludeCountries = %#v, want normalized DE", got)
	}
}

func TestValidateWebhookFiltersNormalizesGenericFieldMaps(t *testing.T) {
	filters := &WebhookFilters{
		IncludeFields: map[string][]string{
			" Victim ": {" hospital "},
		},
		ExcludeFields: map[string][]string{
			" Claim_URL ": {" test "},
		},
	}

	if err := validateWebhookFilters("discord.ransomware", filters); err != nil {
		t.Fatalf("validateWebhookFilters() error = %v", err)
	}
	if err := validateWebhookFilterScope("discord.ransomware", WebhookTypeRansomware, filters); err != nil {
		t.Fatalf("validateWebhookFilterScope() error = %v", err)
	}
	if got := filters.IncludeFields["victim"]; len(got) != 1 || got[0] != "hospital" {
		t.Fatalf("IncludeFields[victim] = %#v, want trimmed hospital", got)
	}
	if got := filters.ExcludeFields["claim_url"]; len(got) != 1 || got[0] != "test" {
		t.Fatalf("ExcludeFields[claim_url] = %#v, want trimmed test", got)
	}
}

func TestValidateWebhookFilterScopeRejectsGenericFieldsForWrongSource(t *testing.T) {
	apiFilters := &WebhookFilters{IncludeFields: map[string][]string{"feed_title": {"threat"}}}
	if err := validateWebhookFilters("discord.ransomware", apiFilters); err != nil {
		t.Fatalf("validateWebhookFilters(api) error = %v", err)
	}
	if err := validateWebhookFilterScope("discord.ransomware", WebhookTypeRansomware, apiFilters); err == nil {
		t.Fatal("validateWebhookFilterScope() accepted RSS field on ransomware webhook")
	}

	rssFilters := &WebhookFilters{IncludeFields: map[string][]string{"victim": {"hospital"}}}
	if err := validateWebhookFilters("discord.rss", rssFilters); err != nil {
		t.Fatalf("validateWebhookFilters(rss) error = %v", err)
	}
	if err := validateWebhookFilterScope("discord.rss", WebhookTypeRSS, rssFilters); err == nil {
		t.Fatal("validateWebhookFilterScope() accepted API field on RSS webhook")
	}
}

func makeStrings(count int, value string) []string {
	values := make([]string, count)
	for i := range values {
		values[i] = value
	}
	return values
}

func TestValidateConfigRejectsRSSOnlyFiltersOnRansomwareWebhooks(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*Config)
		wantFields []string
	}{
		{
			name: "discord ransomware include categories",
			configure: func(cfg *Config) {
				cfg.APIKey = "real-api-key"
				cfg.DiscordWebhooks.Ransomware = WebhookConfig{
					Enabled: true,
					URL:     "https://discord.com/api/webhooks/123/abc",
					Filters: &WebhookFilters{IncludeCategories: []string{"security"}},
				}
			},
			wantFields: []string{"discord.ransomware", "include_categories", "RSS"},
		},
		{
			name: "slack ransomware exclude categories",
			configure: func(cfg *Config) {
				cfg.APIKey = "real-api-key"
				cfg.SlackWebhooks.Ransomware = WebhookConfig{
					Enabled: true,
					URL:     "https://hooks.slack.com/services/T123/B456/abcdef",
					Filters: &WebhookFilters{ExcludeCategories: []string{"noise"}},
				}
			},
			wantFields: []string{"slack.ransomware", "exclude_categories", "RSS"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.configure(cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("expected unsupported filter validation error")
			}
			for _, want := range tt.wantFields {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want substring %q", err, want)
				}
			}
		})
	}
}

func TestValidateConfigRejectsAPIOnlyFiltersOnRSSWebhooks(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*Config)
		wantFields []string
	}{
		{
			name: "discord rss include countries",
			configure: func(cfg *Config) {
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     "https://discord.com/api/webhooks/123/abc",
					Filters: &WebhookFilters{IncludeCountries: []string{"US"}},
				}
			},
			wantFields: []string{"discord.rss", "include_countries", "ransomware API"},
		},
		{
			name: "slack government exclude groups",
			configure: func(cfg *Config) {
				cfg.SlackWebhooks.Government = WebhookConfig{
					Enabled: true,
					URL:     "https://hooks.slack.com/services/T123/B456/abcdef",
					Filters: &WebhookFilters{ExcludeGroups: []string{"lockbit"}},
				}
			},
			wantFields: []string{"slack.government", "exclude_groups", "ransomware API"},
		},
		{
			name: "discord rss include activities",
			configure: func(cfg *Config) {
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     "https://discord.com/api/webhooks/123/abc",
					Filters: &WebhookFilters{IncludeActivities: []string{"data theft"}},
				}
			},
			wantFields: []string{"discord.rss", "include_activities", "ransomware API"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.configure(cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("expected unsupported filter validation error")
			}
			for _, want := range tt.wantFields {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want substring %q", err, want)
				}
			}
		})
	}
}

func TestValidateConfigAllowsSourceAppropriateFilters(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIKey = "real-api-key"
	cfg.DiscordWebhooks.Ransomware = WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123/abc",
		Filters: &WebhookFilters{
			IncludeGroups:    []string{"lockbit"},
			IncludeCountries: []string{"DE"},
		},
	}
	cfg.SlackWebhooks.RSS = WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.com/services/T123/B456/abcdef",
		Filters: &WebhookFilters{
			IncludeCategories: []string{"security"},
			IncludeKeywords:   []string{"ransomware"},
		},
	}

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() rejected source-appropriate filters: %v", err)
	}
}

func TestValidateDiscordWebhook(t *testing.T) {
	tests := []struct {
		name      string
		webhook   WebhookConfig
		shouldErr bool
	}{
		{"disabled", WebhookConfig{Enabled: false}, false},
		{"disabled empty url", WebhookConfig{Enabled: false, URL: ""}, false},
		{"enabled valid", WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/123/abc"}, false},
		{"enabled valid legacy host", WebhookConfig{Enabled: true, URL: "https://discordapp.com/api/webhooks/123/abc"}, false},
		{"enabled empty url", WebhookConfig{Enabled: true, URL: ""}, true},
		{"enabled invalid url", WebhookConfig{Enabled: true, URL: "https://example.com"}, true},
		{"enabled http url", WebhookConfig{Enabled: true, URL: "http://discord.com/api/webhooks/123/abc"}, true},
		{"enabled prefix only", WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/"}, true},
		{"enabled missing token", WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/123"}, true},
		{"enabled empty token", WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/123/"}, true},
		{"enabled placeholder id", WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/YOUR_WEBHOOK_ID/abc"}, true},
		{"enabled placeholder token", WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/123/YOUR_WEBHOOK_TOKEN"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDiscordWebhook("test", tt.webhook)
			if tt.shouldErr && err == nil {
				t.Error("expected error")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateSlackWebhook(t *testing.T) {
	tests := []struct {
		name      string
		webhook   WebhookConfig
		shouldErr bool
	}{
		{"disabled", WebhookConfig{Enabled: false}, false},
		{"enabled valid", WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/T00/B00/xxx"}, false},
		{"enabled valid gov", WebhookConfig{Enabled: true, URL: "https://hooks.slack-gov.com/services/T00/B00/xxx"}, false},
		{"enabled empty url", WebhookConfig{Enabled: true, URL: ""}, true},
		{"enabled invalid url", WebhookConfig{Enabled: true, URL: "https://example.com"}, true},
		{"enabled prefix only", WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/"}, true},
		{"enabled missing token", WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/T00/B00"}, true},
		{"enabled wrong host suffix", WebhookConfig{Enabled: true, URL: "https://hooks.slack.com.evil.test/services/T00/B00/xxx"}, true},
		{"enabled placeholder team", WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/YOUR/B00/xxx"}, true},
		{"enabled placeholder token", WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/T00/B00/WEBHOOK"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSlackWebhook("test", tt.webhook)
			if tt.shouldErr && err == nil {
				t.Error("expected error")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateFeedURL(t *testing.T) {
	tests := []struct {
		url       string
		shouldErr bool
	}{
		{"https://example.com/feed.xml", false},
		{"http://example.com/feed", true},
		{"", true},
		{"ftp://example.com/feed", true},
		{"file:///etc/passwd", true},
		{"example.com/feed", true},
		{"http://localhost/feed", true},
		{"http://127.0.0.1/feed", true},
		{"http://[::1]/feed", true},
		{"https://user:pass@example.com/feed", true},
		{"https://example.com/feed#fragment", true},
		{"https://example.com:bad/feed", true},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := validateFeedURL(tt.url, "test")
			if tt.shouldErr && err == nil {
				t.Error("expected error")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateFeedURLDoesNotEchoSensitiveURLParts(t *testing.T) {
	rawURL := "ftp://user:pass@example.com/feed.xml?token=secret#frag" //nolint:gosec // G101: test fixture, not a real credential

	err := validateFeedURL(rawURL, "general_feeds[0]")
	if err == nil {
		t.Fatal("expected invalid feed URL error")
	}
	msg := err.Error()
	for _, leaked := range []string{rawURL, "user:pass", "token=secret", "#frag"} {
		if strings.Contains(msg, leaked) {
			t.Fatalf("validation error leaked %q: %v", leaked, err)
		}
	}
	if !strings.Contains(msg, "general_feeds[0]") {
		t.Fatalf("validation error = %v, want config field context", err)
	}
	if !strings.Contains(msg, "ftp") {
		t.Fatalf("validation error = %v, want rejected scheme context", err)
	}
}

func TestValidateFeedURLsBatch(t *testing.T) {
	tests := []struct {
		name      string
		feeds     FeedConfig
		shouldErr bool
		wantErr   string
	}{
		{
			"all valid",
			FeedConfig{
				GeneralFeeds:    []string{"https://feed1.com/rss", "https://feed2.com/rss"},
				GovernmentFeeds: []string{"https://gov.com/feed"},
				RansomwareFeeds: []string{"https://ransom.com/feed"},
			},
			false,
			"",
		},
		{
			"empty feeds",
			FeedConfig{},
			false,
			"",
		},
		{
			"invalid general feed",
			FeedConfig{
				GeneralFeeds: []string{"https://valid.com/rss", "ftp://invalid.com/rss"},
			},
			true,
			"URL is not valid",
		},
		{
			"invalid government feed",
			FeedConfig{
				GovernmentFeeds: []string{"file:///etc/passwd"},
			},
			true,
			"URL is not valid",
		},
		{
			"invalid ransomware feed",
			FeedConfig{
				RansomwareFeeds: []string{""},
			},
			true,
			"has empty URL",
		},
		{
			"mixed valid and invalid",
			FeedConfig{
				GeneralFeeds:    []string{"https://valid.com/rss"},
				GovernmentFeeds: []string{"https://gov.com/feed"},
				RansomwareFeeds: []string{"no-protocol.com/feed"},
			},
			true,
			"URL is not valid",
		},
		{
			"duplicate within same feed list",
			FeedConfig{
				GeneralFeeds: []string{"https://example.com/rss", "https://example.com/rss"},
			},
			true,
			"duplicate RSS feed URL",
		},
		{
			"duplicate across feed categories",
			FeedConfig{
				GeneralFeeds:    []string{"https://example.com/rss"},
				GovernmentFeeds: []string{"https://example.com/rss"},
			},
			true,
			"duplicate RSS feed URL",
		},
		{
			"duplicate host differs only by case",
			FeedConfig{
				GeneralFeeds:    []string{"https://EXAMPLE.com/rss"},
				RansomwareFeeds: []string{"https://example.com/rss"},
			},
			true,
			"duplicate RSS feed URL",
		},
		{
			"duplicate https default port vs implicit",
			FeedConfig{
				GeneralFeeds: []string{"https://example.com/rss", "https://example.com:443/rss"},
			},
			true,
			"duplicate RSS feed URL",
		},
		{
			"duplicate https default port across categories",
			FeedConfig{
				GeneralFeeds:    []string{"https://example.com/rss"},
				RansomwareFeeds: []string{"https://example.com:443/rss"},
			},
			true,
			"duplicate RSS feed URL",
		},
		{
			"duplicate uppercase host with default port",
			FeedConfig{
				GeneralFeeds:    []string{"https://EXAMPLE.com:443/rss"},
				GovernmentFeeds: []string{"https://example.com/rss"},
			},
			true,
			"duplicate RSS feed URL",
		},
		{
			"duplicate trailing dot with default port",
			FeedConfig{
				GeneralFeeds:    []string{"https://example.com.:443/rss"},
				GovernmentFeeds: []string{"https://example.com/rss"},
			},
			true,
			"duplicate RSS feed URL",
		},
		{
			"duplicate ipv6 with default port",
			FeedConfig{
				GeneralFeeds: []string{"https://[2001:db8::1]/rss", "https://[2001:db8::1]:443/rss"},
			},
			true,
			"duplicate RSS feed URL",
		},
		{
			"non-default port is a distinct feed",
			FeedConfig{
				GeneralFeeds: []string{"https://example.com/rss", "https://example.com:8443/rss"},
			},
			false,
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Feeds = tt.feeds
			err := validateFeedURLs(cfg)
			if tt.shouldErr {
				if err == nil {
					t.Fatal("expected error")
				}
				// Assert the reason, not just that something failed: a
				// duplicate row would otherwise go green if the URL were
				// rejected by an unrelated check (blocked host, scheme).
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("validateFeedURLs() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestCanonicalFeedURLDropsDefaultPorts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"https default port", "https://h.example:443/rss", "https://h.example/rss"},
		{"https implicit port", "https://h.example/rss", "https://h.example/rss"},
		{"http default port", "http://h.example:80/rss", "http://h.example/rss"},
		{"https non-default port kept", "https://h.example:8443/rss", "https://h.example:8443/rss"},
		{"http non-default port kept", "http://h.example:8080/rss", "http://h.example:8080/rss"},
		{"cross-scheme default port kept", "https://h.example:80/rss", "https://h.example:80/rss"},
		{"http with https default port kept", "http://h.example:443/rss", "http://h.example:443/rss"},
		// The port is compared numerically, so a leading-zero spelling of the
		// default port canonicalizes the same as the plain spelling.
		{"leading zero https default port is recognised", "https://h.example:0443/rss", "https://h.example/rss"},
		{"leading zero http default port is recognised", "http://h.example:080/rss", "http://h.example/rss"},
		// Fixed 2026-09-04 (review finding F3, internal/config/config.go
		// L1807): canonicalFeedPort left a non-default port's string
		// spelling unchanged, so a leading-zero spelling of it (":08443")
		// canonicalized to a different duplicate-check key than its
		// unpadded form (":8443") even though Go's dialer parses both
		// decimally and dials the same TCP endpoint -- the exact
		// double-poll defect this fix set out to close, one step short.
		// The port is now numerically normalized on every return, so both
		// spellings collapse onto the same key.
		{"leading zero non-default port collapses onto its unpadded form", "https://h.example:08443/rss", "https://h.example:8443/rss"},
		{"many-leading-zeros non-default port collapses onto its unpadded form", "https://h.example:0000008443/rss", "https://h.example:8443/rss"},
		// A genuinely different non-default port is numerically distinct
		// and must not collapse onto another one: :8443 and :9443 stay two
		// different duplicate-check keys, same as any other two different
		// ports would ("https non-default port kept" above proves a single
		// non-default port round-trips unchanged; this proves two distinct
		// ones do not converge).
		{"genuinely different non-default port stays distinct", "https://h.example:9443/rss", "https://h.example:9443/rss"},
		// net/url reports an empty port for a bare trailing colon, so the colon
		// is dropped by canonicalFeedHost and both spellings share one key.
		{"empty port after colon", "https://h.example:/rss", "https://h.example/rss"},
		{"ipv6 default port", "https://[2001:db8::1]:443/rss", "https://[2001:db8::1]/rss"},
		{"ipv6 loopback default port", "https://[::1]:443/rss", "https://[::1]/rss"},
		{"uppercase host and default port", "https://H.EXAMPLE.COM:443/rss", "https://h.example.com/rss"},
		{"trailing dot and default port", "https://h.example.:443/rss", "https://h.example/rss"},
		{"uppercase scheme trailing dot default port", "HTTPS://H.Example.:443/rss", "https://h.example/rss"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := canonicalFeedURL(tt.in)
			if err != nil {
				t.Fatalf("canonicalFeedURL(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("canonicalFeedURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestCanonicalFeedPortRejectsSignedPort pins the F5 defense-in-depth guard:
// strconv.Atoi alone accepts a leading '+' ("+8443" -> 8443), which would
// otherwise fuse a signed port onto its unsigned form. canonicalFeedURL never
// reaches this input today -- feedurl.Parse (net/url under it) already
// rejects a signed port earlier -- so this calls the unexported helper
// directly rather than through canonicalFeedURL.
func TestCanonicalFeedPortRejectsSignedPort(t *testing.T) {
	if got := canonicalFeedPort("https", "+8443"); got != "+8443" {
		t.Errorf(`canonicalFeedPort("https", "+8443") = %q, want it returned unchanged`, got)
	}
}

// writeFile is a test helper that writes content to a file in the given directory.
func writeFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0600); err != nil {
		t.Fatalf("failed to write %s: %v", filename, err)
	}
}

func writeGeneralConfig(t *testing.T, dir string, overrides map[string]any) {
	t.Helper()
	config := map[string]any{
		"log_level":          "INFO",
		"api_poll_interval":  "2h",
		"rss_poll_interval":  "1h",
		"rss_retry_delay":    "5s",
		"discord_delay":      "1s",
		"slack_delay":        "2s",
		"rss_worker_timeout": "30s",
	}
	for key, value := range overrides {
		config[key] = value
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal config_general.json fixture: %v", err)
	}
	writeFile(t, dir, "config_general.json", string(data))
}

func TestLoadConfig_FromTempDir(t *testing.T) {
	tmpDir := t.TempDir()

	writeGeneralConfig(t, tmpDir, map[string]any{
		"log_level": "DEBUG",
	})

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() returned error: %v", err)
	}

	// Verify overridden values
	if cfg.LogLevel != "DEBUG" {
		t.Errorf("expected LogLevel DEBUG, got %q", cfg.LogLevel)
	}
	if cfg.APIPollInterval != 2*time.Hour {
		t.Errorf("expected APIPollInterval 2h, got %v", cfg.APIPollInterval)
	}
	if cfg.RSSPollInterval != time.Hour {
		t.Errorf("expected RSSPollInterval 1h, got %v", cfg.RSSPollInterval)
	}
	if cfg.RSSRetryDelay != 5*time.Second {
		t.Errorf("expected RSSRetryDelay 5s, got %v", cfg.RSSRetryDelay)
	}
	if cfg.DiscordDelay != time.Second {
		t.Errorf("expected DiscordDelay 1s, got %v", cfg.DiscordDelay)
	}
	if cfg.SlackDelay != 2*time.Second {
		t.Errorf("expected SlackDelay 2s, got %v", cfg.SlackDelay)
	}
	if cfg.RSSWorkerTimeout != 30*time.Second {
		t.Errorf("expected RSSWorkerTimeout 30s, got %v", cfg.RSSWorkerTimeout)
	}

	// Verify defaults remain for fields not in config_general.json
	if cfg.MaxRSSWorkers != 5 {
		t.Errorf("expected default MaxRSSWorkers 5, got %d", cfg.MaxRSSWorkers)
	}
	if cfg.RSSRetryCount != 3 {
		t.Errorf("expected default RSSRetryCount 3, got %d", cfg.RSSRetryCount)
	}
	if !cfg.Format.ShowUnicodeFlags {
		t.Error("expected default ShowUnicodeFlags true")
	}
	if cfg.LogRotation.MaxSizeMB != 10 {
		t.Errorf("expected default LogRotation.MaxSizeMB 10, got %d", cfg.LogRotation.MaxSizeMB)
	}
}

func TestLoadConfigAppliesStatusRetention(t *testing.T) {
	tmpDir := t.TempDir()

	writeGeneralConfig(t, tmpDir, map[string]any{
		"status_retention": map[string]any{
			"max_api_sent_items":    5000,
			"max_rss_parsed_items":  6000,
			"rss_parsed_max_age":    "720h",
			"max_rss_sent_items":    7000,
			"max_retry_queue_items": 8000,
			"retry_queue_max_age":   "72h",
			"max_dead_letter_items": 9000,
			"dead_letter_max_age":   "96h",
		},
	})

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() returned error: %v", err)
	}

	retention := cfg.StatusRetention
	if retention.MaxAPISentItems != 5000 ||
		retention.MaxRSSParsedItems != 6000 ||
		retention.RSSParsedMaxAge != 720*time.Hour ||
		retention.MaxRSSSentItems != 7000 ||
		retention.MaxRetryQueueItems != 8000 ||
		retention.RetryQueueMaxAge != 72*time.Hour ||
		retention.MaxDeadLetterItems != 9000 ||
		retention.DeadLetterMaxAge != 96*time.Hour {
		t.Fatalf("StatusRetention = %+v, want configured values", retention)
	}
}

func TestLoadConfigRejectsInvalidStatusRetention(t *testing.T) {
	tmpDir := t.TempDir()
	writeGeneralConfig(t, tmpDir, map[string]any{
		"status_retention": map[string]any{
			"max_api_sent_items": 0,
		},
	})

	_, err := LoadConfig(tmpDir)
	if err == nil {
		t.Fatal("LoadConfig() succeeded with invalid status_retention")
	}
	if !strings.Contains(err.Error(), "status_retention.max_api_sent_items") {
		t.Fatalf("LoadConfig() error = %v, want status_retention.max_api_sent_items", err)
	}
}

func TestDefaultConfigIncludesAuditLogRotationDefaults(t *testing.T) {
	cfg := DefaultConfig()

	want := AuditLogRotationConfig{MaxSizeMB: 60, MaxBackups: 20, MaxAgeDays: 365, Compress: true}
	if got := cfg.StatusRetention.AuditLogRotation; got != want {
		t.Fatalf("StatusRetention.AuditLogRotation = %+v, want %+v", got, want)
	}
}

func TestValidateStatusRetentionConfigRejectsAuditLogRotationOutOfRange(t *testing.T) {
	base := func() *Config {
		cfg := DefaultConfig()
		return cfg
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "max_size_mb too small",
			mutate:  func(c *Config) { c.StatusRetention.AuditLogRotation.MaxSizeMB = 0 },
			wantErr: "status_retention.audit_log_rotation.max_size_mb",
		},
		{
			name:    "max_size_mb too large",
			mutate:  func(c *Config) { c.StatusRetention.AuditLogRotation.MaxSizeMB = 1001 },
			wantErr: "status_retention.audit_log_rotation.max_size_mb",
		},
		{
			name:    "max_backups too large",
			mutate:  func(c *Config) { c.StatusRetention.AuditLogRotation.MaxBackups = 51 },
			wantErr: "status_retention.audit_log_rotation.max_backups",
		},
		{
			name:    "max_backups negative",
			mutate:  func(c *Config) { c.StatusRetention.AuditLogRotation.MaxBackups = -1 },
			wantErr: "status_retention.audit_log_rotation.max_backups",
		},
		{
			name:    "max_age_days too large",
			mutate:  func(c *Config) { c.StatusRetention.AuditLogRotation.MaxAgeDays = 366 },
			wantErr: "status_retention.audit_log_rotation.max_age_days",
		},
		{
			name:    "max_age_days negative",
			mutate:  func(c *Config) { c.StatusRetention.AuditLogRotation.MaxAgeDays = -1 },
			wantErr: "status_retention.audit_log_rotation.max_age_days",
		},
		{
			name: "max_backups and max_age_days both zero",
			mutate: func(c *Config) {
				c.StatusRetention.AuditLogRotation.MaxBackups = 0
				c.StatusRetention.AuditLogRotation.MaxAgeDays = 0
			},
			wantErr: "status_retention.audit_log_rotation retention: max_backups and max_age_days cannot both be 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatalf("validateConfig() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateConfig() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfigParsesAuditLogRotationOverrides(t *testing.T) {
	tmpDir := t.TempDir()

	writeGeneralConfig(t, tmpDir, map[string]any{
		"status_retention": map[string]any{
			"audit_log_rotation": map[string]any{
				"max_size_mb":  25,
				"max_backups":  5,
				"max_age_days": 100,
				"compress":     false,
			},
		},
	})

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() returned error: %v", err)
	}

	want := AuditLogRotationConfig{MaxSizeMB: 25, MaxBackups: 5, MaxAgeDays: 100, Compress: false}
	if got := cfg.StatusRetention.AuditLogRotation; got != want {
		t.Fatalf("StatusRetention.AuditLogRotation = %+v, want %+v (explicit compress:false must not fall back to the default true)", got, want)
	}
}

func TestLoadConfigAuditLogRotationOmittedKeepsDefaults(t *testing.T) {
	tmpDir := t.TempDir()

	writeGeneralConfig(t, tmpDir, map[string]any{
		"status_retention": map[string]any{
			"max_api_sent_items": 5000,
		},
	})

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() returned error: %v", err)
	}

	want := AuditLogRotationConfig{MaxSizeMB: 60, MaxBackups: 20, MaxAgeDays: 365, Compress: true}
	if got := cfg.StatusRetention.AuditLogRotation; got != want {
		t.Fatalf("StatusRetention.AuditLogRotation = %+v, want unchanged defaults %+v when the sub-object is omitted", got, want)
	}
}

func TestLoadConfig_MissingGeneralConfig(t *testing.T) {
	tmpDir := t.TempDir()
	// No config_general.json created

	_, err := LoadConfig(tmpDir)
	if err == nil {
		t.Fatal("expected error when config_general.json is missing")
	}
	if !strings.Contains(err.Error(), "config_general.json") {
		t.Errorf("error should mention config_general.json, got: %v", err)
	}
}

func TestLoadConfig_OptionalFeedsConfig(t *testing.T) {
	tmpDir := t.TempDir()

	writeGeneralConfig(t, tmpDir, nil)
	// No config_feeds.json created

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() should succeed without config_feeds.json, got error: %v", err)
	}

	// Feeds should have empty default slices
	if len(cfg.Feeds.GeneralFeeds) != 0 {
		t.Errorf("expected empty GeneralFeeds, got %v", cfg.Feeds.GeneralFeeds)
	}
	if len(cfg.Feeds.GovernmentFeeds) != 0 {
		t.Errorf("expected empty GovernmentFeeds, got %v", cfg.Feeds.GovernmentFeeds)
	}
	if len(cfg.Feeds.RansomwareFeeds) != 0 {
		t.Errorf("expected empty RansomwareFeeds, got %v", cfg.Feeds.RansomwareFeeds)
	}
}

func TestConfigFileSignatureDetectsOptionalConfigDeletion(t *testing.T) {
	tmpDir := t.TempDir()

	writeFile(t, tmpDir, "config_general.json", `{"api_poll_interval":"2h","rss_poll_interval":"1h","rss_retry_delay":"5s","discord_delay":"1s","slack_delay":"2s","rss_worker_timeout":"30s"}`)
	writeFile(t, tmpDir, "config_feeds.json", `{"general":["https://example.com/feed.xml"]}`)

	before, err := ConfigFileSignature(tmpDir)
	if err != nil {
		t.Fatalf("ConfigFileSignature() before deletion error = %v", err)
	}
	if err := os.Remove(filepath.Join(tmpDir, "config_feeds.json")); err != nil {
		t.Fatalf("Remove(config_feeds.json) error = %v", err)
	}

	after, err := ConfigFileSignature(tmpDir)
	if err != nil {
		t.Fatalf("ConfigFileSignature() after deletion error = %v", err)
	}
	if before == after {
		t.Fatalf("ConfigFileSignature() did not change after optional config deletion: %q", after)
	}
	if !strings.Contains(after, "config_feeds.json:missing") {
		t.Fatalf("ConfigFileSignature() after deletion = %q, want missing feed state", after)
	}
}

func TestLatestConfigModTimeReportsRequiredFileErrors(t *testing.T) {
	_, err := LatestConfigModTime(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing required config_general.json")
	}
	if !strings.Contains(err.Error(), "required config file config_general.json") {
		t.Fatalf("error = %v, want required config file context", err)
	}
}

func TestLatestConfigModTimeUsesLatestOptionalConfigTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	generalPath := filepath.Join(tmpDir, "config_general.json")
	feedsPath := filepath.Join(tmpDir, "config_feeds.json")
	writeFile(t, tmpDir, "config_general.json", `{}`)
	writeFile(t, tmpDir, "config_feeds.json", `{"general_feeds":[]}`)

	older := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	newer := older.Add(time.Hour)
	if err := os.Chtimes(generalPath, older, older); err != nil {
		t.Fatalf("Chtimes(config_general.json) error = %v", err)
	}
	if err := os.Chtimes(feedsPath, newer, newer); err != nil {
		t.Fatalf("Chtimes(config_feeds.json) error = %v", err)
	}

	got, err := LatestConfigModTime(tmpDir)
	if err != nil {
		t.Fatalf("LatestConfigModTime() error = %v", err)
	}
	if got.Before(newer.Add(-time.Second)) || got.After(newer.Add(time.Second)) {
		t.Fatalf("LatestConfigModTime() = %v, want latest optional timestamp near %v", got, newer)
	}
}

func TestLoadConfig_OptionalFormatConfig(t *testing.T) {
	tmpDir := t.TempDir()

	writeGeneralConfig(t, tmpDir, nil)
	// No config_format.json created

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() should succeed without config_format.json, got error: %v", err)
	}

	// Format should retain defaults
	if !cfg.Format.ShowUnicodeFlags {
		t.Error("expected default ShowUnicodeFlags true")
	}
	if len(cfg.Format.FieldOrder) != 6 {
		t.Errorf("expected 6 default FieldOrder entries, got %d", len(cfg.Format.FieldOrder))
	}
}

func TestLoadGeneralConfigWebhookWithoutQuietHoursIsNoop(t *testing.T) {
	tmpDir := t.TempDir()
	writeGeneralConfig(t, tmpDir, map[string]any{
		"discord_webhooks": map[string]any{
			"ransomware": map[string]any{
				"enabled": false,
				"url":     "",
			},
		},
	})

	cfg := DefaultConfig()
	if err := loadGeneralConfig(cfg, tmpDir); err != nil {
		t.Fatalf("loadGeneralConfig() error = %v", err)
	}
	if cfg.DiscordWebhooks.Ransomware.QuietHours != nil {
		t.Fatalf("QuietHours = %#v, want nil no-op when omitted", cfg.DiscordWebhooks.Ransomware.QuietHours)
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestLoadGeneralConfig_DurationParsing(t *testing.T) {
	tmpDir := t.TempDir()

	generalJSON := `{
		"api_base_url": "https://sovereign.example/api/",
		"api_request_timeout": "45s",
		"api_max_retries": 4,
		"api_retry_delay": "250ms",
		"api_poll_interval": "90m",
		"api_check_timeout": "7m",
		"rss_poll_interval": "45m",
		"rss_check_timeout": "12m",
		"rss_retry_delay": "10s",
		"discord_delay": "500ms",
		"slack_delay": "3s",
		"webhook_request_timeout": "20s",
		"webhook_max_retries": 4,
		"webhook_retry_delay": "750ms",
		"rss_worker_timeout": "2m",
		"rss_max_item_age": "24h",
		"max_api_entries_per_cycle": 7,
		"max_rss_entries_per_cycle": 8
	}`
	writeFile(t, tmpDir, "config_general.json", generalJSON)

	cfg := DefaultConfig()
	err := loadGeneralConfig(cfg, tmpDir)
	if err != nil {
		t.Fatalf("loadGeneralConfig() returned error: %v", err)
	}

	if cfg.APIPollInterval != 90*time.Minute {
		t.Errorf("expected APIPollInterval 90m, got %v", cfg.APIPollInterval)
	}
	if cfg.APICheckTimeout != 7*time.Minute {
		t.Errorf("expected APICheckTimeout 7m, got %v", cfg.APICheckTimeout)
	}
	if cfg.APIBaseURL != "https://sovereign.example/api/" {
		t.Errorf("expected raw APIBaseURL from general config, got %q", cfg.APIBaseURL)
	}
	if cfg.APIRequestTimeout != 45*time.Second {
		t.Errorf("expected APIRequestTimeout 45s, got %v", cfg.APIRequestTimeout)
	}
	if cfg.APIMaxRetries != 4 {
		t.Errorf("expected APIMaxRetries 4, got %d", cfg.APIMaxRetries)
	}
	if cfg.APIRetryDelay != 250*time.Millisecond {
		t.Errorf("expected APIRetryDelay 250ms, got %v", cfg.APIRetryDelay)
	}
	if cfg.RSSPollInterval != 45*time.Minute {
		t.Errorf("expected RSSPollInterval 45m, got %v", cfg.RSSPollInterval)
	}
	if cfg.RSSCheckTimeout != 12*time.Minute {
		t.Errorf("expected RSSCheckTimeout 12m, got %v", cfg.RSSCheckTimeout)
	}
	if cfg.RSSRetryDelay != 10*time.Second {
		t.Errorf("expected RSSRetryDelay 10s, got %v", cfg.RSSRetryDelay)
	}
	if cfg.DiscordDelay != 500*time.Millisecond {
		t.Errorf("expected DiscordDelay 500ms, got %v", cfg.DiscordDelay)
	}
	if cfg.SlackDelay != 3*time.Second {
		t.Errorf("expected SlackDelay 3s, got %v", cfg.SlackDelay)
	}
	if cfg.WebhookRequestTimeout != 20*time.Second {
		t.Errorf("expected WebhookRequestTimeout 20s, got %v", cfg.WebhookRequestTimeout)
	}
	if cfg.WebhookMaxRetries != 4 {
		t.Errorf("expected WebhookMaxRetries 4, got %d", cfg.WebhookMaxRetries)
	}
	if cfg.WebhookRetryDelay != 750*time.Millisecond {
		t.Errorf("expected WebhookRetryDelay 750ms, got %v", cfg.WebhookRetryDelay)
	}
	if cfg.RSSWorkerTimeout != 2*time.Minute {
		t.Errorf("expected RSSWorkerTimeout 2m, got %v", cfg.RSSWorkerTimeout)
	}
	if cfg.RSSMaxItemAge != 24*time.Hour {
		t.Errorf("expected RSSMaxItemAge 24h, got %v", cfg.RSSMaxItemAge)
	}
	if cfg.APIMaxEntriesPerCycle != 7 {
		t.Errorf("expected APIMaxEntriesPerCycle 7, got %d", cfg.APIMaxEntriesPerCycle)
	}
	if cfg.RSSMaxEntriesPerCycle != 8 {
		t.Errorf("expected RSSMaxEntriesPerCycle 8, got %d", cfg.RSSMaxEntriesPerCycle)
	}
}

func TestLoadGeneralConfig_RetrySettings(t *testing.T) {
	tmpDir := t.TempDir()

	generalJSON := `{
		"retry_max_attempts": 0,
		"retry_window": "48h"
	}`
	writeFile(t, tmpDir, "config_general.json", generalJSON)

	cfg := DefaultConfig()
	if err := loadGeneralConfig(cfg, tmpDir); err != nil {
		t.Fatalf("loadGeneralConfig() returned error: %v", err)
	}
	if cfg.RetryMaxAttempts != 0 {
		t.Fatalf("RetryMaxAttempts = %d, want 0", cfg.RetryMaxAttempts)
	}
	if cfg.RetryWindow != 48*time.Hour {
		t.Fatalf("RetryWindow = %v, want 48h", cfg.RetryWindow)
	}
}

func TestLoadGeneralConfig_InvalidDuration(t *testing.T) {
	tmpDir := t.TempDir()

	generalJSON := `{
		"api_poll_interval": "invalid_duration"
	}`
	writeFile(t, tmpDir, "config_general.json", generalJSON)

	cfg := DefaultConfig()
	err := loadGeneralConfig(cfg, tmpDir)
	if err == nil {
		t.Fatal("expected error for invalid duration string")
	}
	if !strings.Contains(err.Error(), "api_poll_interval") {
		t.Errorf("error should mention api_poll_interval, got: %v", err)
	}
}

func TestConfigLoadersRejectUnknownKeys(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		body     string
		loadFunc func(*Config, string) error
	}{
		{
			name:     "general",
			file:     "config_general.json",
			body:     `{"log_level":"INFO","unexpected":true}`,
			loadFunc: loadGeneralConfig,
		},
		{
			name:     "feeds",
			file:     "config_feeds.json",
			body:     `{"general_feeds":[],"unexpected":[]}`,
			loadFunc: loadFeedsConfig,
		},
		{
			name:     "format",
			file:     "config_format.json",
			body:     `{"show_unicode_flags":true,"unexpected":true}`,
			loadFunc: loadFormatConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeFile(t, tmpDir, tt.file, tt.body)

			err := tt.loadFunc(DefaultConfig(), tmpDir)
			if err == nil {
				t.Fatal("expected unknown-field error")
			}
			if !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("error = %v, want unknown field", err)
			}
		})
	}
}

func TestLoadGeneralConfigRejectsMistypedFilterKeys(t *testing.T) {
	tmpDir := t.TempDir()
	writeFile(t, tmpDir, "config_general.json", `{
		"discord_webhooks": {
			"ransomware": {
				"enabled": true,
				"url": "https://discord.com/api/webhooks/123/abc",
				"filters": {
					"include_keyword": ["lockbit"]
				}
			}
		}
	}`)

	err := loadGeneralConfig(DefaultConfig(), tmpDir)
	if err == nil {
		t.Fatal("expected unknown nested filter key error")
	}
	if !strings.Contains(err.Error(), "include_keyword") || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown include_keyword field", err)
	}
}

func TestConfigLoadersRejectMalformedJSON(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		loadFunc func(*Config, string) error
	}{
		{
			name:     "general",
			file:     "config_general.json",
			loadFunc: loadGeneralConfig,
		},
		{
			name:     "feeds",
			file:     "config_feeds.json",
			loadFunc: loadFeedsConfig,
		},
		{
			name:     "format",
			file:     "config_format.json",
			loadFunc: loadFormatConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeFile(t, tmpDir, tt.file, `{"broken":`)

			err := tt.loadFunc(DefaultConfig(), tmpDir)
			if err == nil {
				t.Fatal("expected malformed JSON error")
			}
			if !strings.Contains(err.Error(), tt.file) {
				t.Fatalf("error = %v, want file name %q", err, tt.file)
			}
		})
	}
}

func TestLoadConfigDegradesMalformedOptionalConfigs(t *testing.T) {
	tmpDir := t.TempDir()
	writeFile(t, tmpDir, "config_general.json", `{}`)
	writeFile(t, tmpDir, "config_feeds.json", `{"broken":`)
	writeFile(t, tmpDir, "config_format.json", `{"show_unicode_flags":`)

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.Feeds.GeneralFeeds) != 0 || len(cfg.Feeds.GovernmentFeeds) != 0 || len(cfg.Feeds.RansomwareFeeds) != 0 {
		t.Fatalf("Feeds = %#v, want RSS feeds disabled after malformed optional config", cfg.Feeds)
	}
	defaultCfg := DefaultConfig()
	if strings.Join(cfg.Format.FieldOrder, ",") != strings.Join(defaultCfg.Format.FieldOrder, ",") {
		t.Fatalf("Format.FieldOrder = %v, want default %v", cfg.Format.FieldOrder, defaultCfg.Format.FieldOrder)
	}
	if cfg.Format.ShowUnicodeFlags != defaultCfg.Format.ShowUnicodeFlags {
		t.Fatalf("Format.ShowUnicodeFlags = %v, want default %v", cfg.Format.ShowUnicodeFlags, defaultCfg.Format.ShowUnicodeFlags)
	}
}

func TestLoadConfigStrictRejectsMalformedOptionalConfigs(t *testing.T) {
	tests := []struct {
		name string
		file string
		body string
	}{
		{name: "feeds", file: "config_feeds.json", body: `{"broken":`},
		{name: "format", file: "config_format.json", body: `{"show_unicode_flags":`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeFile(t, tmpDir, "config_general.json", `{}`)
			writeFile(t, tmpDir, tt.file, tt.body)

			_, err := LoadConfigStrict(tmpDir)
			if err == nil {
				t.Fatal("expected strict optional config error")
			}
			if !strings.Contains(err.Error(), tt.file) {
				t.Fatalf("error = %v, want file name %q", err, tt.file)
			}
		})
	}
}

func TestLoadGeneralConfig_ZeroValuePointers(t *testing.T) {
	tmpDir := t.TempDir()

	// max_rss_workers=1 (valid, minimum is 1) and rss_retry_count=0 (valid, means no retry)
	generalJSON := `{
		"max_rss_workers": 1,
		"rss_retry_count": 0,
		"api_poll_interval": "2h",
		"rss_poll_interval": "1h",
		"rss_retry_delay": "5s",
		"discord_delay": "1s",
		"slack_delay": "2s",
		"rss_worker_timeout": "30s"
	}`
	writeFile(t, tmpDir, "config_general.json", generalJSON)

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() returned error: %v", err)
	}

	if cfg.MaxRSSWorkers != 1 {
		t.Errorf("expected MaxRSSWorkers 1, got %d", cfg.MaxRSSWorkers)
	}
	if cfg.RSSRetryCount != 0 {
		t.Errorf("expected RSSRetryCount 0, got %d", cfg.RSSRetryCount)
	}
}

func TestLoadGeneralConfig_DataDirResolution(t *testing.T) {
	tmpDir := t.TempDir()

	generalJSON := `{
		"data_dir": "relative/path",
		"api_poll_interval": "2h",
		"rss_poll_interval": "1h",
		"rss_retry_delay": "5s",
		"discord_delay": "1s",
		"slack_delay": "2s",
		"rss_worker_timeout": "30s"
	}`
	writeFile(t, tmpDir, "config_general.json", generalJSON)

	cfg := DefaultConfig()
	err := loadGeneralConfig(cfg, tmpDir)
	if err != nil {
		t.Fatalf("loadGeneralConfig() returned error: %v", err)
	}

	if !filepath.IsAbs(cfg.DataDir) {
		t.Errorf("expected DataDir to be absolute path, got %q", cfg.DataDir)
	}
	if !strings.Contains(cfg.DataDir, "relative") || !strings.Contains(cfg.DataDir, "path") {
		t.Errorf("expected DataDir to contain 'relative/path' segments, got %q", cfg.DataDir)
	}
}

func TestLoadFeedsConfig_ValidFeeds(t *testing.T) {
	tmpDir := t.TempDir()

	feedsJSON := `{
		"general_feeds": ["https://example.com/feed1.xml", "https://example.com/feed2.xml"],
		"government_feeds": ["https://gov.example.com/feed.xml"],
		"ransomware_feeds": ["https://security.example.com/rss"]
	}`
	writeFile(t, tmpDir, "config_feeds.json", feedsJSON)

	cfg := DefaultConfig()
	err := loadFeedsConfig(cfg, tmpDir)
	if err != nil {
		t.Fatalf("loadFeedsConfig() returned error: %v", err)
	}

	if len(cfg.Feeds.GeneralFeeds) != 2 {
		t.Errorf("expected 2 general feeds, got %d", len(cfg.Feeds.GeneralFeeds))
	}
	if len(cfg.Feeds.GovernmentFeeds) != 1 {
		t.Errorf("expected 1 government feed, got %d", len(cfg.Feeds.GovernmentFeeds))
	}
	if len(cfg.Feeds.RansomwareFeeds) != 1 {
		t.Errorf("expected 1 ransomware feed, got %d", len(cfg.Feeds.RansomwareFeeds))
	}
	if cfg.Feeds.GeneralFeeds[0] != "https://example.com/feed1.xml" {
		t.Errorf("unexpected first general feed: %q", cfg.Feeds.GeneralFeeds[0])
	}
}

func TestLoadFeedsConfig_InvalidProtocol(t *testing.T) {
	tmpDir := t.TempDir()

	feedsJSON := `{
		"general_feeds": ["ftp://example.com/feed.xml"]
	}`
	writeFile(t, tmpDir, "config_feeds.json", feedsJSON)

	cfg := DefaultConfig()
	err := loadFeedsConfig(cfg, tmpDir)
	if err == nil {
		t.Fatal("expected error for ftp:// feed URL")
	}
	if !strings.Contains(err.Error(), "http") {
		t.Errorf("error should mention http protocol requirement, got: %v", err)
	}
}

func TestLoadFormatConfig_CustomFields(t *testing.T) {
	tmpDir := t.TempDir()

	formatJSON := `{
		"show_unicode_flags": false,
		"show_empty_fields": false,
		"empty_field_text": "Unknown",
		"display_locale": "de",
		"timestamp_format": "02.01.2006 15:04 MST",
		"display_timezone": "Europe/Berlin",
		"field_order": ["group", "victim", "country"]
	}`
	writeFile(t, tmpDir, "config_format.json", formatJSON)

	cfg := DefaultConfig()
	err := loadFormatConfig(cfg, tmpDir)
	if err != nil {
		t.Fatalf("loadFormatConfig() returned error: %v", err)
	}

	if cfg.Format.ShowUnicodeFlags {
		t.Error("expected ShowUnicodeFlags false after override")
	}
	if cfg.Format.ShowEmptyFields {
		t.Error("expected ShowEmptyFields false after override")
	}
	if cfg.Format.EmptyFieldText != "Unknown" {
		t.Errorf("expected EmptyFieldText 'Unknown', got %q", cfg.Format.EmptyFieldText)
	}
	if cfg.Format.DisplayLocale != "de" {
		t.Errorf("expected DisplayLocale 'de', got %q", cfg.Format.DisplayLocale)
	}
	if cfg.Format.TimestampFormat != "02.01.2006 15:04 MST" {
		t.Errorf("expected custom TimestampFormat, got %q", cfg.Format.TimestampFormat)
	}
	if cfg.Format.DisplayTimezone != "Europe/Berlin" {
		t.Errorf("expected DisplayTimezone Europe/Berlin, got %q", cfg.Format.DisplayTimezone)
	}

	expectedOrder := []string{"group", "victim", "country"}
	if len(cfg.Format.FieldOrder) != len(expectedOrder) {
		t.Fatalf("expected %d field_order entries, got %d", len(expectedOrder), len(cfg.Format.FieldOrder))
	}
	for i, expected := range expectedOrder {
		if cfg.Format.FieldOrder[i] != expected {
			t.Errorf("FieldOrder[%d] = %q, want %q", i, cfg.Format.FieldOrder[i], expected)
		}
	}
}

func TestValidateFormatConfigRejectsInvalidDisplayLocale(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Format.DisplayLocale = "not a locale"

	err := validateFormatConfig(cfg)
	if err == nil {
		t.Fatal("validateFormatConfig() error = nil, want invalid display_locale error")
	}
	if !strings.Contains(err.Error(), "display_locale") {
		t.Fatalf("error = %v, want display_locale context", err)
	}
}

func TestValidateFormatConfigRejectsInvalidDisplayTimezone(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Format.DisplayTimezone = "Mars/Olympus"

	err := validateFormatConfig(cfg)
	if err == nil {
		t.Fatal("validateFormatConfig() error = nil, want invalid display_timezone error")
	}
	if !strings.Contains(err.Error(), "display_timezone") {
		t.Fatalf("error = %v, want display_timezone context", err)
	}
}

// TestWebhookTargetsOmittedFilterAndQuietHoursInheritFromParent pins the
// fixed behaviour: a targets[]
// entry that omits "filters"/"quiet_hours" inherits the parent block's
// values for that key -- the same pointer, exactly like url/urls[] endpoints
// already do -- rather than getting nil (unfiltered, always-deliver).
func TestWebhookTargetsOmittedFilterAndQuietHoursInheritFromParent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SlackWebhooks.Ransomware = WebhookConfig{
		Enabled: true,
		Filters: &WebhookFilters{
			IncludeCountries: []string{"DE"},
		},
		QuietHours: &QuietHours{
			Enabled: true,
			Start:   "22:00",
			End:     "06:00",
		},
		Targets: []WebhookTarget{
			{URL: "https://hooks.slack.com/services/T/B/bare"},
		},
	}

	targets := WebhookTargetsForType(cfg, WebhookTypeRansomware)
	var got WebhookConfig
	found := false
	for _, target := range targets {
		if target.Platform == WebhookPlatformSlack {
			got = target.Webhook
			found = true
		}
	}
	if !found {
		t.Fatal("no Slack ransomware target found")
	}

	if got.Filters != cfg.SlackWebhooks.Ransomware.Filters {
		t.Fatalf("Filters = %p, want the block's own pointer %p (inherited)", got.Filters, cfg.SlackWebhooks.Ransomware.Filters)
	}
	if got.QuietHours != cfg.SlackWebhooks.Ransomware.QuietHours {
		t.Fatalf("QuietHours = %p, want the block's own pointer %p (inherited)", got.QuietHours, cfg.SlackWebhooks.Ransomware.QuietHours)
	}
}

// TestValidateSlackWebhookURLRedactsTokenOnParseError pins the fix: a
// hooks.slack.com URL that fails url.Parse (e.g. a stray
// control character from a copy-paste) must not echo the raw token in the
// returned error.
func TestValidateSlackWebhookURLRedactsTokenOnParseError(t *testing.T) {
	const token = "verySecretSlackTokenABCDEF123456"
	malformed := "https://hooks.slack.com/services/T00000000/B00000000/" + token + "\t"

	err := validateSlackWebhookURL(malformed)
	if err == nil {
		t.Fatal("validateSlackWebhookURL() error = nil, want a parse error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks the raw token: %v", err)
	}
}

// TestValidateSlackCompatibleWebhookURLRedactsTokenOnCustomHostParseError pins
// the fix for a gap in the original fix above:
// textutil.RedactWebhookErrorForURL's
// fallback no longer requires url.Parse(webhookURL) to succeed on the exact
// string it is redacting -- it now falls back to matching the raw webhook URL
// AND its strconv.Quote-escaped form directly, which closes this for any
// host, not just hooks.slack.com/Discord's hardcoded text regex.
func TestValidateSlackCompatibleWebhookURLRedactsTokenOnCustomHostParseError(t *testing.T) {
	const token = "very-secret-compatible-token-xyz987"
	// The control character must not be trailing whitespace: strings.TrimSpace
	// inside the marker-fallback path would strip a bare trailing "\t" and let
	// url.Parse succeed on the trimmed copy, accidentally passing this by
	// coincidence instead of exercising the parse-failure branch. A trailing
	// "/tail" after the tab keeps it embedded, matching the plan's verified
	// reproduction.
	malformed := "https://chat.example.test/hooks/" + token + "\t/tail"

	err := validateSlackCompatibleWebhookURL(malformed, []string{"chat.example.test"})
	if err == nil {
		t.Fatal("validateSlackCompatibleWebhookURL() error = nil, want a parse error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks the raw token: %v", err)
	}
}

// TestValidateSlackWebhookURLRedactsTokenOnCustomHostParseError is the
// companion case for the other call site the same regex widening affects:
// hooks.slack-gov.com through validateSlackWebhookURL, not previously
// exercised by any config package test with a control-character input.
func TestValidateSlackWebhookURLRedactsTokenOnCustomHostParseError(t *testing.T) {
	const token = "very-secret-slackgov-token-abc123"
	malformed := "https://hooks.slack-gov.com/services/T00/B00/" + token + "\t/tail"

	err := validateSlackWebhookURL(malformed)
	if err == nil {
		t.Fatal("validateSlackWebhookURL() error = nil, want a parse error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks the raw token: %v", err)
	}
}

// TestValidateDiscordWebhookURLRedactsTokenOnParseError pins the matching
// Discord fix: a discord.com URL that
// fails url.Parse must not echo the raw token in the returned error. Unlike
// the Slack-compatible-host case above, this fix has no residual gap.
func TestValidateDiscordWebhookURLRedactsTokenOnParseError(t *testing.T) {
	const token = "verySecretDiscordToken555"
	malformed := "https://discord.com/api/webhooks/111111111111111111/" + token + "\t/tail"

	err := validateDiscordWebhookURL(malformed)
	if err == nil {
		t.Fatal("validateDiscordWebhookURL() error = nil, want a parse error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks the raw token: %v", err)
	}
}

// TestFormatConfigOmitemptyOnStructFieldsIsInert is a permanent regression
// test proving that removing the no-op omitempty tags from
// FormatConfig.RSS/Discord/Slack changes zero bytes of the marshaled shape --
// encoding/json never treats a non-pointer struct value as "empty", so the
// tag was always inert. Config is decode-only (never marshaled to disk), so
// this only guards against a future accidental behaviour change.
func TestFormatConfigOmitemptyOnStructFieldsIsInert(t *testing.T) {
	data, err := json.Marshal(FormatConfig{})
	if err != nil {
		t.Fatalf("json.Marshal(FormatConfig{}) error = %v", err)
	}
	t.Logf("marshaled zero-value FormatConfig: %s", data)

	for _, key := range []string{`"rss":`, `"discord":`, `"slack":`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("marshaled output missing %s -- omitempty removal must not change the marshaled shape:\n%s", key, data)
		}
	}
}
