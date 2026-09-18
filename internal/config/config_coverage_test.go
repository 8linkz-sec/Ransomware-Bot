package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestLoadGeneralConfigAppliesLogFilePathAndRotationOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	writeGeneralConfig(t, tmpDir, map[string]any{
		"log_file_path": "./logs/custom.log",
		"log_rotation": map[string]any{
			"max_size_mb":  25,
			"max_backups":  7,
			"max_age_days": 14,
			"compress":     false,
		},
	})

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.LogFilePath != "./logs/custom.log" {
		t.Fatalf("LogFilePath = %q, want ./logs/custom.log", cfg.LogFilePath)
	}
	if cfg.LogRotation.MaxSizeMB != 25 {
		t.Fatalf("LogRotation.MaxSizeMB = %d, want 25", cfg.LogRotation.MaxSizeMB)
	}
	if cfg.LogRotation.MaxBackups != 7 {
		t.Fatalf("LogRotation.MaxBackups = %d, want 7", cfg.LogRotation.MaxBackups)
	}
	if cfg.LogRotation.MaxAgeDays != 14 {
		t.Fatalf("LogRotation.MaxAgeDays = %d, want 14", cfg.LogRotation.MaxAgeDays)
	}
	if cfg.LogRotation.Compress {
		t.Fatal("LogRotation.Compress = true, want false override")
	}
}

func TestLoadGeneralConfigRejectsInvalidRetryWindow(t *testing.T) {
	tmpDir := t.TempDir()
	writeGeneralConfig(t, tmpDir, map[string]any{
		"retry_window": "soon",
	})

	_, err := LoadConfig(tmpDir)
	if err == nil {
		t.Fatal("LoadConfig() succeeded with invalid retry_window")
	}
	if !strings.Contains(err.Error(), "retry_window") {
		t.Fatalf("LoadConfig() error = %v, want retry_window context", err)
	}
}

func TestLoadGeneralConfigRejectsTrailingJSONContent(t *testing.T) {
	tmpDir := t.TempDir()
	writeFile(t, tmpDir, "config_general.json", `{}{}`)

	_, err := LoadConfig(tmpDir)
	if err == nil {
		t.Fatal("LoadConfig() succeeded with trailing JSON content")
	}
	if !strings.Contains(err.Error(), "single object") {
		t.Fatalf("LoadConfig() error = %v, want single object context", err)
	}
}

func TestLoadConfigReportsUnreadableOptionalConfigFiles(t *testing.T) {
	tests := []struct {
		name     string
		filename string
	}{
		{name: "feeds", filename: "config_feeds.json"},
		{name: "format", filename: "config_format.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeGeneralConfig(t, tmpDir, nil)
			// A directory with the config file name makes os.ReadFile fail with
			// an error that is not os.IsNotExist.
			if err := os.Mkdir(filepath.Join(tmpDir, tt.filename), 0o700); err != nil {
				t.Fatalf("Mkdir(%s) error = %v", tt.filename, err)
			}

			_, err := LoadConfig(tmpDir)
			if err == nil {
				t.Fatalf("LoadConfig() succeeded with unreadable %s", tt.filename)
			}
			if !strings.Contains(err.Error(), tt.filename) {
				t.Fatalf("LoadConfig() error = %v, want %s context", err, tt.filename)
			}
		})
	}
}

func TestLoadFeedsConfigNormalizesMissingFeedCategories(t *testing.T) {
	tmpDir := t.TempDir()
	writeGeneralConfig(t, tmpDir, nil)
	writeFile(t, tmpDir, "config_feeds.json", `{"ransomware_feeds":null,"government_feeds":null,"general_feeds":null}`)

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Feeds.RansomwareFeeds == nil || len(cfg.Feeds.RansomwareFeeds) != 0 {
		t.Fatalf("RansomwareFeeds = %#v, want non-nil empty slice", cfg.Feeds.RansomwareFeeds)
	}
	if cfg.Feeds.GovernmentFeeds == nil || len(cfg.Feeds.GovernmentFeeds) != 0 {
		t.Fatalf("GovernmentFeeds = %#v, want non-nil empty slice", cfg.Feeds.GovernmentFeeds)
	}
	if cfg.Feeds.GeneralFeeds == nil || len(cfg.Feeds.GeneralFeeds) != 0 {
		t.Fatalf("GeneralFeeds = %#v, want non-nil empty slice", cfg.Feeds.GeneralFeeds)
	}
}

func TestLoadConfigDisablesFeedsOnInvalidFeedURLsInNonStrictMode(t *testing.T) {
	tmpDir := t.TempDir()
	writeGeneralConfig(t, tmpDir, nil)
	writeFile(t, tmpDir, "config_feeds.json", `{"general_feeds":["ftp://feeds.example/rss"]}`)

	cfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want degraded feeds instead", err)
	}
	if len(cfg.Feeds.GeneralFeeds) != 0 || len(cfg.Feeds.GovernmentFeeds) != 0 || len(cfg.Feeds.RansomwareFeeds) != 0 {
		t.Fatalf("Feeds = %#v, want all feed lists emptied after failed validation", cfg.Feeds)
	}
}

func TestValidateConfigRejectsBlankLogFilePath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LogFilePath = "   "

	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("validateConfig() succeeded with blank log_file_path")
	}
	if !strings.Contains(err.Error(), "log_file_path") {
		t.Fatalf("validateConfig() error = %v, want log_file_path context", err)
	}
}

func TestValidateConfigWarnsAboutHighLogRotationDiskUsage(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	cfg := DefaultConfig()
	cfg.LogRotation.MaxSizeMB = 1000
	cfg.LogRotation.MaxBackups = 10

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "disk space") {
			found = true
		}
	}
	if !found {
		t.Fatal("validateConfig() did not warn about potential disk usage")
	}
}

func TestValidateConfigRejectsUnparseableAPIBaseURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIBaseURL = "://missing-scheme"

	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("validateConfig() succeeded with invalid api_base_url")
	}
	if !strings.Contains(err.Error(), "api_base_url") {
		t.Fatalf("validateConfig() error = %v, want api_base_url context", err)
	}
}

func TestValidateConfigWarnsOnPlaceholderAPIKeyForNonRansomwareWebhooks(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	cfg := DefaultConfig()
	cfg.APIKey = "YOUR_API_KEY"
	cfg.DiscordWebhooks.RSS = WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123/abc",
	}

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "placeholder") {
			found = true
		}
	}
	if !found {
		t.Fatal("validateConfig() did not warn about placeholder API key")
	}
}

func TestValidateConfigRejectsNegativeWebhookDelays(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Config)
		want   string
	}{
		{
			name:   "discord",
			modify: func(cfg *Config) { cfg.DiscordDelay = -time.Second },
			want:   "discord_delay cannot be negative",
		},
		{
			name:   "slack",
			modify: func(cfg *Config) { cfg.SlackDelay = -time.Second },
			want:   "slack_delay cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.modify(cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("validateConfig() succeeded with negative delay")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateConfig() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateConfigRejectsStatusRetentionAgeOutOfRange(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StatusRetention.RSSParsedMaxAge = time.Minute

	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("validateConfig() succeeded with too small retention age")
	}
	if !strings.Contains(err.Error(), "status_retention.rss_parsed_max_age") {
		t.Fatalf("validateConfig() error = %v, want retention age context", err)
	}
}

func TestValidateConfigRejectsInvalidFormatTextSettings(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Config)
		want   string
	}{
		{
			name:   "blank timestamp format",
			modify: func(cfg *Config) { cfg.Format.TimestampFormat = "  " },
			want:   "timestamp_format",
		},
		{
			name:   "blank display timezone",
			modify: func(cfg *Config) { cfg.Format.DisplayTimezone = "  " },
			want:   "display_timezone",
		},
		{
			name:   "blank display locale",
			modify: func(cfg *Config) { cfg.Format.DisplayLocale = "  " },
			want:   "display_locale",
		},
		{
			name:   "invalid rss color characters",
			modify: func(cfg *Config) { cfg.Format.Discord.RSSColor = "#gggggg" },
			want:   "discord.rss_color",
		},
		{
			name:   "invalid government color",
			modify: func(cfg *Config) { cfg.Format.Discord.GovernmentColor = "not-hex" },
			want:   "discord.government_color",
		},
		{
			name:   "invalid top-level field labels",
			modify: func(cfg *Config) { cfg.Format.FieldLabels = map[string]string{"victim": "  "} },
			want:   "field_labels",
		},
		{
			name:   "invalid rss field labels",
			modify: func(cfg *Config) { cfg.Format.RSS.FieldLabels = map[string]string{"title": "  "} },
			want:   "rss.field_labels",
		},
		{
			name:   "invalid discord field labels",
			modify: func(cfg *Config) { cfg.Format.Discord.FieldLabels = map[string]string{"victim": "  "} },
			want:   "discord.field_labels",
		},
		{
			name:   "invalid slack field labels",
			modify: func(cfg *Config) { cfg.Format.Slack.FieldLabels = map[string]string{"victim": "  "} },
			want:   "slack.field_labels",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.modify(cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("validateConfig() succeeded with invalid format settings")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateConfig() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateFormatLabelMapBounds(t *testing.T) {
	tooMany := make(map[string]string, maxFormatLabels+1)
	for i := 0; i <= maxFormatLabels; i++ {
		tooMany[fmt.Sprintf("field_%d", i)] = "Label"
	}
	err := validateFormatLabelMap("field_labels", tooMany)
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("validateFormatLabelMap(too many) error = %v, want too many entries", err)
	}

	err = validateFormatLabelMap("field_labels", map[string]string{
		"victim": strings.Repeat("a", maxFormatLabelRunes+1),
	})
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("validateFormatLabelMap(too long) error = %v, want too long", err)
	}

	if err := validateFormatLabelMap("field_labels", map[string]string{"victim": "Victim Label"}); err != nil {
		t.Fatalf("validateFormatLabelMap(valid) error = %v", err)
	}
}

func TestValidateConfigChecksQuietHoursOnDisabledWebhooks(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DiscordWebhooks.RSS.QuietHours = &QuietHours{
		Enabled: true,
		Start:   "not-a-time",
		End:     "07:00",
	}

	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("validateConfig() succeeded with invalid quiet hours")
	}
	if !strings.Contains(err.Error(), "quiet_hours.start") {
		t.Fatalf("validateConfig() error = %v, want quiet_hours.start context", err)
	}
}

func TestValidateWebhookFilterScopeRejectsAllAPIOnlyListsOnRSSWebhooks(t *testing.T) {
	tests := []struct {
		name    string
		filters *WebhookFilters
		want    string
	}{
		{
			name:    "include groups",
			filters: &WebhookFilters{IncludeGroups: []string{"lockbit"}},
			want:    "include_groups",
		},
		{
			name:    "exclude countries",
			filters: &WebhookFilters{ExcludeCountries: []string{"US"}},
			want:    "exclude_countries",
		},
		{
			name:    "exclude activities",
			filters: &WebhookFilters{ExcludeActivities: []string{"data theft"}},
			want:    "exclude_activities",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWebhookFilterScope("discord.rss", WebhookTypeRSS, tt.filters)
			if err == nil {
				t.Fatal("validateWebhookFilterScope() accepted API-only filter on RSS webhook")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateWebhookFilterScopeRejectsExcludeFieldsOutOfScope(t *testing.T) {
	filters := &WebhookFilters{
		ExcludeFields: map[string][]string{"victim": {"hospital"}},
	}

	err := validateWebhookFilterScope("discord.rss", WebhookTypeRSS, filters)
	if err == nil {
		t.Fatal("validateWebhookFilterScope() accepted API field in exclude_fields on RSS webhook")
	}
	if !strings.Contains(err.Error(), "exclude_fields") {
		t.Fatalf("error = %v, want exclude_fields context", err)
	}
}

func TestWebhookFilterFieldNamesUnknownTypeHasNoAllowedFields(t *testing.T) {
	if fields := webhookFilterFieldNames("unknown"); fields != nil {
		t.Fatalf("webhookFilterFieldNames(unknown) = %#v, want nil", fields)
	}
}

func TestQualifiedNameIncludesDestinationSuffix(t *testing.T) {
	target := WebhookTargetConfig{
		Platform:          WebhookPlatformDiscord,
		Name:              WebhookTypeRSS,
		DestinationSuffix: "2",
	}
	if got := target.QualifiedName(); got != "discord.rss.2" {
		t.Fatalf("QualifiedName() = %q, want discord.rss.2", got)
	}
}

func TestWebhookTargetsNilConfig(t *testing.T) {
	if targets := WebhookTargets(nil); targets != nil {
		t.Fatalf("WebhookTargets(nil) = %#v, want nil", targets)
	}
}

func TestValidateFieldOrderEntryAndRenderLimits(t *testing.T) {
	err := validateFieldOrder("field_order", []string{"bogus"}, discordFieldOrderMaxItems)
	if err == nil || !strings.Contains(err.Error(), "invalid field_order entry") {
		t.Fatalf("validateFieldOrder(invalid entry) error = %v, want invalid entry", err)
	}

	// description renders as two items in the Discord field order.
	err = validateFieldOrder("field_order", []string{"description", "post_url"}, 2)
	if err == nil || !strings.Contains(err.Error(), "renders") {
		t.Fatalf("validateFieldOrder(render limit) error = %v, want render limit error", err)
	}

	if err := validateFieldOrder("field_order", []string{"description", "post_url"}, discordFieldOrderMaxItems); err != nil {
		t.Fatalf("validateFieldOrder(valid description order) error = %v", err)
	}
}

func TestValidateRSSFieldOrderRejectsEmptyAndDuplicateEntries(t *testing.T) {
	err := validateRSSFieldOrder("rss.field_order", nil)
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("validateRSSFieldOrder(empty) error = %v, want cannot be empty", err)
	}

	err = validateRSSFieldOrder("rss.field_order", []string{"title", "Title"})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("validateRSSFieldOrder(duplicate) error = %v, want duplicate", err)
	}
}

func TestValidateWebhookFiltersNormalizesExcludeActivities(t *testing.T) {
	filters := &WebhookFilters{
		ExcludeActivities: []string{"  data theft  "},
	}

	if err := validateWebhookFilters("discord.ransomware", filters); err != nil {
		t.Fatalf("validateWebhookFilters() error = %v", err)
	}
	if got := filters.ExcludeActivities[0]; got != "data theft" {
		t.Fatalf("ExcludeActivities[0] = %q, want trimmed value", got)
	}
}

func TestValidateWebhookFiltersRejectsInvalidFieldMaps(t *testing.T) {
	tests := []struct {
		name    string
		filters *WebhookFilters
		want    string
	}{
		{
			name:    "blank include field name",
			filters: &WebhookFilters{IncludeFields: map[string][]string{"  ": {"x"}}},
			want:    "include_fields field name cannot be blank",
		},
		{
			name:    "blank exclude field name",
			filters: &WebhookFilters{ExcludeFields: map[string][]string{"": {"x"}}},
			want:    "exclude_fields field name cannot be blank",
		},
		{
			name: "field name too long",
			filters: &WebhookFilters{
				IncludeFields: map[string][]string{strings.Repeat("f", maxFilterFieldNameRunes+1): {"x"}},
			},
			want: "exceeds",
		},
		{
			name: "duplicate after normalization",
			filters: &WebhookFilters{
				IncludeFields: map[string][]string{"Victim": {"a"}, " victim ": {"b"}}, //nolint:gocritic // mapKey: whitespace is the point, testing normalization of a space-padded field name
			},
			want: "duplicated after normalization",
		},
		{
			name: "blank field value",
			filters: &WebhookFilters{
				IncludeFields: map[string][]string{"victim": {"  "}},
			},
			want: "cannot be blank",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWebhookFilters("discord.ransomware", tt.filters)
			if err == nil {
				t.Fatal("validateWebhookFilters() accepted invalid field map")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateSlackCompatibleWebhookRequiresURLWhenEnabled(t *testing.T) {
	err := validateSlackCompatibleWebhook("rss", WebhookConfig{Enabled: true}, nil)
	if err == nil {
		t.Fatal("validateSlackCompatibleWebhook() accepted enabled webhook without URL")
	}
	if !strings.Contains(err.Error(), "URL is empty") {
		t.Fatalf("error = %v, want URL is empty context", err)
	}
}

func TestValidateSlackWebhookURLErrorPaths(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "unparseable url", url: "https://hooks.slack.com/services/%zz", want: "invalid URL escape"},
		{name: "http scheme", url: "http://hooks.slack.com/services/T00/B00/xxx", want: "scheme must be https"},
		{name: "empty path segment", url: "https://hooks.slack.com/services/T00//xxx", want: "non-empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSlackWebhookURL(tt.url)
			if err == nil {
				t.Fatal("validateSlackWebhookURL() accepted invalid URL")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateSlackCompatibleWebhookURLErrorPaths(t *testing.T) {
	allowed := []string{"hooks.eu.example"}
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "unparseable url", url: "https://hooks.eu.example/%zz", want: "invalid URL escape"},
		{name: "http scheme", url: "http://hooks.eu.example/webhook", want: "scheme must be https"},
		{name: "userinfo", url: "https://user@hooks.eu.example/webhook", want: "userinfo"},
		{name: "empty host", url: "https:///webhook", want: "host is required"},
		{name: "empty path", url: "https://hooks.eu.example/", want: "path must not be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSlackCompatibleWebhookURL(tt.url, allowed)
			if err == nil {
				t.Fatal("validateSlackCompatibleWebhookURL() accepted invalid URL")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}

	if err := validateSlackCompatibleWebhookURL("https://hooks.eu.example/webhook", allowed); err != nil {
		t.Fatalf("validateSlackCompatibleWebhookURL(valid) error = %v", err)
	}
}

func TestNormalizeAllowedWebhookHostsSkipsBlankAndDuplicateEntries(t *testing.T) {
	got := normalizeAllowedWebhookHosts([]string{"  ", "Hooks.EU.Example.", "hooks.eu.example", "other.example"})
	if len(got) != 2 || got[0] != "hooks.eu.example" || got[1] != "other.example" {
		t.Fatalf("normalizeAllowedWebhookHosts() = %#v, want deduplicated lowercase hosts", got)
	}
}

func TestCanonicalFeedURLNormalizesHostAndPort(t *testing.T) {
	if _, err := canonicalFeedURL("://bad"); err == nil {
		t.Fatal("canonicalFeedURL() accepted malformed URL")
	}

	got, err := canonicalFeedURL("https://Feeds.Example.:8443/rss")
	if err != nil {
		t.Fatalf("canonicalFeedURL(port) error = %v", err)
	}
	if got != "https://feeds.example:8443/rss" {
		t.Fatalf("canonicalFeedURL(port) = %q, want normalized host with port", got)
	}

	got, err = canonicalFeedURL("https://[2001:db8::1]/rss")
	if err != nil {
		t.Fatalf("canonicalFeedURL(ipv6) error = %v", err)
	}
	if got != "https://[2001:db8::1]/rss" {
		t.Fatalf("canonicalFeedURL(ipv6) = %q, want bracketed IPv6 host", got)
	}
}

func TestConfigFileSignatureRequiresGeneralConfig(t *testing.T) {
	_, err := ConfigFileSignature(t.TempDir())
	if err == nil {
		t.Fatal("ConfigFileSignature() succeeded without config_general.json")
	}
	if !strings.Contains(err.Error(), "required config file config_general.json") {
		t.Fatalf("ConfigFileSignature() error = %v, want required file context", err)
	}
}
