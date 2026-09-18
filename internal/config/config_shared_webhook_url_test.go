package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Fixture URLs. No path segment may contain YOUR, PLACEHOLDER, TOKEN or
// WEBHOOK: isPlaceholderWebhookSegment rejects those and validateConfig would
// fail before the shared-URL warning is reached.
const (
	sharedDiscordToken = "secretAAAABBBBCCCC012345"
	sharedDiscordURL   = "https://discord.com/api/webhooks/123456789012345678/" + sharedDiscordToken
	otherDiscordURL    = "https://discord.com/api/webhooks/987654321098765432/secretZZZZYYYYXXXX543210"
	// caseVariantDiscordURL is sharedDiscordURL with the token spelled in a
	// different case. It is a different byte string, so it is a different
	// dedup destination and deliberately NOT grouped (see decision 3 in the
	// plan; TESTING.md pins it as a known gap).
	caseVariantDiscordURL = "https://discord.com/api/webhooks/123456789012345678/SECRETaaaabbbbcccc012345"
	sharedSlackToken      = "secretDDDDEEEEFFFF678901"
	sharedSlackURL        = "https://hooks.slack.com/services/T12345678/B12345678/" + sharedSlackToken
	realAPIKey            = "live-api-key-1234567890"
)

const sharedWebhookWarnFragment = "share one webhook URL"

// enabledWebhook builds an enabled single-URL webhook block.
func enabledWebhook(endpointURL string) WebhookConfig {
	return WebhookConfig{Enabled: true, URL: endpointURL}
}

// countSharedURLWarnings returns the WARN entries emitted for shared webhook URLs.
func countSharedURLWarnings(entries []*logrus.Entry) []*logrus.Entry {
	matches := make([]*logrus.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Level == logrus.WarnLevel && strings.Contains(entry.Message, sharedWebhookWarnFragment) {
			matches = append(matches, entry)
		}
	}
	return matches
}

// assertNoWebhookURLLeak fails when any log entry carries URL material.
func assertNoWebhookURLLeak(t *testing.T, entries []*logrus.Entry, secrets ...string) {
	t.Helper()
	forbidden := append([]string{"/api/webhooks/", "/services/"}, secrets...)
	for _, entry := range entries {
		texts := []string{entry.Message}
		for _, value := range entry.Data {
			texts = append(texts, fmt.Sprint(value))
		}
		for _, text := range texts {
			for _, secret := range forbidden {
				if strings.Contains(text, secret) {
					t.Fatalf("log entry leaked webhook URL material %q in %q", secret, text)
				}
			}
		}
	}
}

// caseAConfig is probe case A: two enabled discord ransomware endpoints on one URL.
func caseAConfig() *Config {
	cfg := DefaultConfig()
	cfg.APIKey = realAPIKey
	cfg.DiscordWebhooks.Ransomware = WebhookConfig{
		Enabled: true,
		URL:     sharedDiscordURL,
		Targets: []WebhookTarget{{URL: sharedDiscordURL}},
	}
	return cfg
}

// caseHConfig is probe case H: one URL shared across two platform blocks of one kind.
func caseHConfig() *Config {
	cfg := DefaultConfig()
	cfg.SlackCompatibleWebhookHosts = []string{"hooks.slack.com"}
	cfg.SlackWebhooks.RSS = enabledWebhook(sharedSlackURL)
	cfg.SlackCompatibleWebhooks.RSS = enabledWebhook(sharedSlackURL)
	return cfg
}

func TestSharedWebhookURLGroupsFindsRepeatedEndpointURLs(t *testing.T) {
	unparsableURL := "https://[::1"

	tests := []struct {
		name   string
		cfg    *Config
		expect []SharedWebhookURLGroup
	}{
		{
			name: "A url plus targets entry share one URL",
			cfg:  caseAConfig(),
			expect: []SharedWebhookURLGroup{{
				Kind:        WebhookTypeRansomware,
				Host:        "discord.com",
				EndpointIDs: []string{"discord.ransomware", "discord.ransomware.2"},
			}},
		},
		{
			name: "B urls list repeats one URL and keeps the distinct one out",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URLs:    []string{sharedDiscordURL, sharedDiscordURL, otherDiscordURL},
				}
				return cfg
			}(),
			expect: []SharedWebhookURLGroup{{
				Kind:        WebhookTypeRSS,
				Host:        "discord.com",
				EndpointIDs: []string{"discord.rss", "discord.rss.2"},
			}},
		},
		{
			name: "C targets list repeats one URL",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.SlackWebhooks.Government = WebhookConfig{
					Enabled: true,
					Targets: []WebhookTarget{{URL: sharedSlackURL}, {URL: sharedSlackURL}},
				}
				return cfg
			}(),
			expect: []SharedWebhookURLGroup{{
				Kind:        WebhookTypeGovernment,
				Host:        "hooks.slack.com",
				EndpointIDs: []string{"slack.government", "slack.government.2"},
			}},
		},
		{
			name: "D distinct URLs in one block are not a group",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: otherDiscordURL}},
				}
				return cfg
			}(),
			expect: nil,
		},
		{
			name: "E one URL across different kinds is not a group",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = enabledWebhook(sharedDiscordURL)
				cfg.DiscordWebhooks.RSS = enabledWebhook(sharedDiscordURL)
				return cfg
			}(),
			expect: nil,
		},
		{
			name: "F disabled block sharing a URL is not a group",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: false,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: sharedDiscordURL}},
				}
				return cfg
			}(),
			expect: nil,
		},
		{
			name: "G trailing slash spelling is a different URL",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: sharedDiscordURL + "/"}},
				}
				return cfg
			}(),
			expect: nil,
		},
		{
			name: "G2 a case-differing spelling is a different URL",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: caseVariantDiscordURL}},
				}
				return cfg
			}(),
			expect: nil,
		},
		{
			name: "H one URL across two platform blocks of one kind is a group",
			cfg:  caseHConfig(),
			expect: []SharedWebhookURLGroup{{
				Kind:        WebhookTypeRSS,
				Host:        "hooks.slack.com",
				EndpointIDs: []string{"slack.rss", "slack_compatible.rss"},
			}},
		},
		{
			name: "three endpoints on one URL form one group",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					URLs:    []string{sharedDiscordURL, sharedDiscordURL},
				}
				return cfg
			}(),
			expect: []SharedWebhookURLGroup{{
				Kind:        WebhookTypeRSS,
				Host:        "discord.com",
				EndpointIDs: []string{"discord.rss", "discord.rss.2", "discord.rss.3"},
			}},
		},
		{
			name: "unparsable shared URL still groups but reports no host",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     unparsableURL,
					Targets: []WebhookTarget{{URL: unparsableURL}},
				}
				return cfg
			}(),
			expect: []SharedWebhookURLGroup{{
				Kind:        WebhookTypeRSS,
				Host:        "",
				EndpointIDs: []string{"discord.rss", "discord.rss.2"},
			}},
		},
		{
			name: "enabled block without a URL is skipped, the sharing group is not",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.Government = WebhookConfig{Enabled: true}
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: sharedDiscordURL}},
				}
				return cfg
			}(),
			expect: []SharedWebhookURLGroup{{
				Kind:        WebhookTypeRSS,
				Host:        "discord.com",
				EndpointIDs: []string{"discord.rss", "discord.rss.2"},
			}},
		},
		{
			name:   "nil config yields no groups",
			cfg:    nil,
			expect: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SharedWebhookURLGroups(tc.cfg)
			if len(got) != len(tc.expect) {
				t.Fatalf("SharedWebhookURLGroups() = %+v, want %+v", got, tc.expect)
			}
			for i, want := range tc.expect {
				if got[i].Kind != want.Kind {
					t.Errorf("group[%d].Kind = %q, want %q", i, got[i].Kind, want.Kind)
				}
				if got[i].Host != want.Host {
					t.Errorf("group[%d].Host = %q, want %q", i, got[i].Host, want.Host)
				}
				if strings.Join(got[i].EndpointIDs, ", ") != strings.Join(want.EndpointIDs, ", ") {
					t.Errorf("group[%d].EndpointIDs = %v, want %v", i, got[i].EndpointIDs, want.EndpointIDs)
				}
			}
		})
	}
}

func TestValidateConfigWarnsOnceForSharedWebhookURL(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		wantWarns   []map[string]any
		wantSecrets []string
	}{
		{
			name: "case A warns once naming both discord endpoints",
			cfg:  caseAConfig(),
			wantWarns: []map[string]any{{
				"endpoint_ids":   "discord.ransomware, discord.ransomware.2",
				"endpoint_count": 2,
				"webhook_kind":   WebhookTypeRansomware,
				"webhook_host":   "discord.com",
			}},
			wantSecrets: []string{sharedDiscordToken},
		},
		{
			name: "case H warns once across platform blocks",
			cfg:  caseHConfig(),
			wantWarns: []map[string]any{{
				"endpoint_ids":   "slack.rss, slack_compatible.rss",
				"endpoint_count": 2,
				"webhook_kind":   WebhookTypeRSS,
				"webhook_host":   "hooks.slack.com",
			}},
			wantSecrets: []string{sharedSlackToken},
		},
		{
			// Three groups, so a map-iteration order in SharedWebhookURLGroups
			// reproduces the configuration order only 1 time in 6.
			name: "three independent groups warn once each in configuration order",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: sharedDiscordURL}},
				}
				cfg.DiscordWebhooks.Government = WebhookConfig{
					Enabled: true,
					URL:     otherDiscordURL,
					Targets: []WebhookTarget{{URL: otherDiscordURL}},
				}
				cfg.SlackWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     sharedSlackURL,
					Targets: []WebhookTarget{{URL: sharedSlackURL}},
				}
				return cfg
			}(),
			wantWarns: []map[string]any{
				{
					"endpoint_ids":   "discord.ransomware, discord.ransomware.2",
					"endpoint_count": 2,
					"webhook_kind":   WebhookTypeRansomware,
					"webhook_host":   "discord.com",
				},
				{
					"endpoint_ids":   "discord.government, discord.government.2",
					"endpoint_count": 2,
					"webhook_kind":   WebhookTypeGovernment,
					"webhook_host":   "discord.com",
				},
				{
					"endpoint_ids":   "slack.rss, slack.rss.2",
					"endpoint_count": 2,
					"webhook_kind":   WebhookTypeRSS,
					"webhook_host":   "hooks.slack.com",
				},
			},
			wantSecrets: []string{sharedDiscordToken, sharedSlackToken},
		},
		{
			name: "three endpoints on one URL warn once with endpoint_count 3",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					URLs:    []string{sharedDiscordURL, sharedDiscordURL},
				}
				return cfg
			}(),
			wantWarns: []map[string]any{{
				"endpoint_ids":   "discord.rss, discord.rss.2, discord.rss.3",
				"endpoint_count": 3,
				"webhook_kind":   WebhookTypeRSS,
				"webhook_host":   "discord.com",
			}},
			wantSecrets: []string{sharedDiscordToken},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hook := logtest.NewGlobal()
			defer hook.Reset()

			if err := validateConfig(tc.cfg); err != nil {
				t.Fatalf("validateConfig() error = %v, want nil", err)
			}

			entries := hook.AllEntries()
			warns := countSharedURLWarnings(entries)
			if len(warns) != len(tc.wantWarns) {
				t.Fatalf("shared-URL WARN count = %d, want %d", len(warns), len(tc.wantWarns))
			}
			for i, want := range tc.wantWarns {
				for field, wantValue := range want {
					gotValue, ok := warns[i].Data[field]
					if !ok {
						t.Fatalf("WARN[%d] has no field %q; fields = %v", i, field, warns[i].Data)
					}
					if gotValue != wantValue {
						t.Errorf("WARN[%d] field %q = %v, want %v", i, field, gotValue, wantValue)
					}
				}
				if _, exists := warns[i].Data["platform"]; exists {
					t.Errorf("WARN[%d] carries a platform field; the group may span platforms", i)
				}
			}
			assertNoWebhookURLLeak(t, entries, tc.wantSecrets...)
		})
	}
}

func TestValidateConfigDoesNotWarnForSharedWebhookURLVariants(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
	}{
		{
			name: "D distinct URLs in one block",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: otherDiscordURL}},
				}
				return cfg
			}(),
		},
		{
			name: "E one URL across different kinds",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = enabledWebhook(sharedDiscordURL)
				cfg.DiscordWebhooks.RSS = enabledWebhook(sharedDiscordURL)
				return cfg
			}(),
		},
		{
			name: "F disabled block sharing a URL",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: false,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: sharedDiscordURL}},
				}
				return cfg
			}(),
		},
		{
			name: "G trailing slash spelling of one channel",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: sharedDiscordURL + "/"}},
				}
				return cfg
			}(),
		},
		{
			name: "G2 case-differing spelling of one channel",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{
					Enabled: true,
					URL:     sharedDiscordURL,
					Targets: []WebhookTarget{{URL: caseVariantDiscordURL}},
				}
				return cfg
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hook := logtest.NewGlobal()
			defer hook.Reset()

			if err := validateConfig(tc.cfg); err != nil {
				t.Fatalf("validateConfig() error = %v, want nil (a rejected fixture cannot prove silence)", err)
			}
			if warns := countSharedURLWarnings(hook.AllEntries()); len(warns) != 0 {
				t.Fatalf("shared-URL WARN count = %d, want 0; first = %+v", len(warns), warns[0].Data)
			}
		})
	}
}

// TestValidateConfigDoesNotWarnWhenValidationFails pins the ORDER inside
// validateConfig: the shared-URL WARN runs after every validator, so an invalid
// configuration — which is never loaded — does not emit an advisory about a
// double post that can never happen. Moving warnSharedWebhookURLs above the
// validator loop makes both rows warn.
func TestValidateConfigDoesNotWarnWhenValidationFails(t *testing.T) {
	sharedRansomware := func() WebhookConfig {
		return WebhookConfig{
			Enabled: true,
			URL:     sharedDiscordURL,
			Targets: []WebhookTarget{{URL: sharedDiscordURL}},
		}
	}

	tests := []struct {
		name    string
		cfg     *Config
		wantErr string
	}{
		{
			// validateLogConfig is the first validator in the chain.
			name: "first validator rejects the config",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.LogLevel = "NOT-A-LEVEL"
				cfg.DiscordWebhooks.Ransomware = sharedRansomware()
				return cfg
			}(),
			wantErr: "invalid log level",
		},
		{
			// validateWebhookFiltersAndQuietHours is the last validator in the
			// chain, so this row fails only after every earlier one passed.
			name: "last validator rejects the config",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				webhook := sharedRansomware()
				webhook.QuietHours = &QuietHours{Enabled: true, Start: "25:00", End: "02:00", Timezone: "UTC"}
				cfg.DiscordWebhooks.Ransomware = webhook
				return cfg
			}(),
			wantErr: "quiet_hours",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Sanity: the same config without the defect does warn, so a
			// silent row cannot be silent for the wrong reason.
			hook := logtest.NewGlobal()
			defer hook.Reset()

			err := validateConfig(tc.cfg)
			if err == nil {
				t.Fatalf("validateConfig() error = nil, want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateConfig() error = %v, want it to contain %q", err, tc.wantErr)
			}
			if warns := countSharedURLWarnings(hook.AllEntries()); len(warns) != 0 {
				t.Fatalf("shared-URL WARN count = %d on an invalid config, want 0; first = %+v", len(warns), warns[0].Data)
			}

			// Control: remove the validation defect and the very same shared
			// URL warns exactly once. Without this the rows above would pass
			// even if the WARN had been deleted outright.
			hook.Reset()
			control := DefaultConfig()
			control.APIKey = realAPIKey
			control.DiscordWebhooks.Ransomware = sharedRansomware()
			if err := validateConfig(control); err != nil {
				t.Fatalf("control validateConfig() error = %v, want nil", err)
			}
			if warns := countSharedURLWarnings(hook.AllEntries()); len(warns) != 1 {
				t.Fatalf("control shared-URL WARN count = %d, want 1", len(warns))
			}
		})
	}
}

func TestLoadConfigWarnsOnceForSharedWebhookURLAndStillSucceeds(t *testing.T) {
	loaders := []struct {
		name string
		load func(string) (*Config, error)
	}{
		{name: "LoadConfig", load: LoadConfig},
		{name: "LoadConfigStrict", load: LoadConfigStrict},
	}

	for _, loader := range loaders {
		t.Run(loader.name, func(t *testing.T) {
			dir := t.TempDir()
			writeGeneralConfig(t, dir, map[string]any{
				"api_key": realAPIKey,
				"discord_webhooks": map[string]any{
					"ransomware": map[string]any{
						"enabled": true,
						"url":     sharedDiscordURL,
						"targets": []any{map[string]any{"url": sharedDiscordURL}},
					},
				},
			})

			hook := logtest.NewGlobal()
			defer hook.Reset()

			cfg, err := loader.load(dir)
			if err != nil {
				t.Fatalf("%s() error = %v, want nil (a shared URL must never be rejected)", loader.name, err)
			}
			enabled := 0
			for _, target := range WebhookTargets(cfg) {
				if target.Webhook.Enabled && strings.TrimSpace(target.Webhook.URL) != "" {
					enabled++
				}
			}
			if enabled != 2 {
				t.Fatalf("enabled endpoints = %d, want 2", enabled)
			}

			warns := countSharedURLWarnings(hook.AllEntries())
			if len(warns) != 1 {
				t.Fatalf("shared-URL WARN count = %d, want 1", len(warns))
			}
			if got := warns[0].Data["endpoint_ids"]; got != "discord.ransomware, discord.ransomware.2" {
				t.Fatalf("endpoint_ids = %v, want %q", got, "discord.ransomware, discord.ransomware.2")
			}
			assertNoWebhookURLLeak(t, hook.AllEntries(), sharedDiscordToken)
		})
	}
}
