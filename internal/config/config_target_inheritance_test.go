package config

import (
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Tests in this file pin the rule that targets[] entries inherit the
// parent webhook block's filters/quiet_hours, independently per key, exactly
// like url/urls[] endpoints already do.

const targetInheritanceWarnFragment = "inherited filters/quiet_hours"

// countTargetInheritanceWarnings returns the WARN entries emitted for
// targets[] entries that inherited filters/quiet_hours from their block.
func countTargetInheritanceWarnings(entries []*logrus.Entry) []*logrus.Entry {
	matches := make([]*logrus.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Level == logrus.WarnLevel && strings.Contains(entry.Message, targetInheritanceWarnFragment) {
			matches = append(matches, entry)
		}
	}
	return matches
}

// TestWebhookEndpointConfigsTargetInheritsPerKeyIndependently pins §7 test 2:
// a targets[] entry inherits Filters and QuietHours independently -- setting
// one key does not force inheritance of the other, and vice versa.
func TestWebhookEndpointConfigsTargetInheritsPerKeyIndependently(t *testing.T) {
	blockFilters := &WebhookFilters{IncludeGroups: []string{"lockbit"}}
	blockQuietHours := &QuietHours{Enabled: true, Start: "22:00", End: "06:00"}
	ownFilters := &WebhookFilters{IncludeGroups: []string{"alphv"}}
	ownQuietHours := &QuietHours{Enabled: true, Start: "23:00", End: "05:00"}

	tests := []struct {
		name                    string
		target                  WebhookTarget
		wantFilters             *WebhookFilters
		wantQuietHours          *QuietHours
		wantInheritedFilters    bool
		wantInheritedQuietHours bool
	}{
		{
			name:                    "omits both inherits both",
			target:                  WebhookTarget{URL: "https://discord.com/api/webhooks/1/a"},
			wantFilters:             blockFilters,
			wantQuietHours:          blockQuietHours,
			wantInheritedFilters:    true,
			wantInheritedQuietHours: true,
		},
		{
			name:                    "sets only filters keeps its own filters, inherits quiet hours",
			target:                  WebhookTarget{URL: "https://discord.com/api/webhooks/2/b", Filters: ownFilters},
			wantFilters:             ownFilters,
			wantQuietHours:          blockQuietHours,
			wantInheritedFilters:    false,
			wantInheritedQuietHours: true,
		},
		{
			name:                    "sets only quiet hours inherits filters, keeps its own quiet hours",
			target:                  WebhookTarget{URL: "https://discord.com/api/webhooks/3/c", QuietHours: ownQuietHours},
			wantFilters:             blockFilters,
			wantQuietHours:          ownQuietHours,
			wantInheritedFilters:    true,
			wantInheritedQuietHours: false,
		},
		{
			name:                    "sets both inherits neither",
			target:                  WebhookTarget{URL: "https://discord.com/api/webhooks/4/d", Filters: ownFilters, QuietHours: ownQuietHours},
			wantFilters:             ownFilters,
			wantQuietHours:          ownQuietHours,
			wantInheritedFilters:    false,
			wantInheritedQuietHours: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			webhook := WebhookConfig{
				Enabled:    true,
				Filters:    blockFilters,
				QuietHours: blockQuietHours,
				Targets:    []WebhookTarget{tc.target},
			}
			endpoints, inheritedFilters, inheritedQuietHours := webhookEndpointConfigs(webhook)
			if len(endpoints) != 1 {
				t.Fatalf("len(endpoints) = %d, want 1", len(endpoints))
			}
			if endpoints[0].Filters != tc.wantFilters {
				t.Errorf("Filters = %p, want %p", endpoints[0].Filters, tc.wantFilters)
			}
			if endpoints[0].QuietHours != tc.wantQuietHours {
				t.Errorf("QuietHours = %p, want %p", endpoints[0].QuietHours, tc.wantQuietHours)
			}
			if len(inheritedFilters) != 1 || inheritedFilters[0] != tc.wantInheritedFilters {
				t.Errorf("inheritedFilters = %v, want [%v]", inheritedFilters, tc.wantInheritedFilters)
			}
			if len(inheritedQuietHours) != 1 || inheritedQuietHours[0] != tc.wantInheritedQuietHours {
				t.Errorf("inheritedQuietHours = %v, want [%v]", inheritedQuietHours, tc.wantInheritedQuietHours)
			}
		})
	}
}

// TestWebhookEndpointConfigsEmptyFiltersAndQuietHoursAreTheEscapeHatch pins
// §7 test 3: an explicit "filters": {} / "quiet_hours": {} on a target keeps
// its own distinct, non-nil, empty pointer and never inherits -- the
// forward-looking regression guard for the escape hatch (passes before and
// after the fix; not a RED/GREEN pin by itself).
func TestWebhookEndpointConfigsEmptyFiltersAndQuietHoursAreTheEscapeHatch(t *testing.T) {
	blockFilters := &WebhookFilters{IncludeGroups: []string{"lockbit"}}
	blockQuietHours := &QuietHours{Enabled: true, Start: "22:00", End: "06:00"}
	ownEmptyFilters := &WebhookFilters{}
	ownEmptyQuietHours := &QuietHours{}

	webhook := WebhookConfig{
		Enabled:    true,
		Filters:    blockFilters,
		QuietHours: blockQuietHours,
		Targets: []WebhookTarget{
			{URL: "https://discord.com/api/webhooks/1/a", Filters: ownEmptyFilters, QuietHours: ownEmptyQuietHours},
		},
	}

	endpoints, inheritedFilters, inheritedQuietHours := webhookEndpointConfigs(webhook)
	if len(endpoints) != 1 {
		t.Fatalf("len(endpoints) = %d, want 1", len(endpoints))
	}
	if endpoints[0].Filters == nil {
		t.Fatal("Filters = nil, want the target's own empty, non-nil object")
	}
	if endpoints[0].Filters == blockFilters {
		t.Error("Filters == blockFilters, want the target's own distinct pointer")
	}
	if endpoints[0].Filters != ownEmptyFilters {
		t.Errorf("Filters = %p, want %p (the target's own)", endpoints[0].Filters, ownEmptyFilters)
	}
	if endpoints[0].QuietHours == nil {
		t.Fatal("QuietHours = nil, want the target's own empty, non-nil object")
	}
	if endpoints[0].QuietHours == blockQuietHours {
		t.Error("QuietHours == blockQuietHours, want the target's own distinct pointer")
	}
	if endpoints[0].QuietHours != ownEmptyQuietHours {
		t.Errorf("QuietHours = %p, want %p (the target's own)", endpoints[0].QuietHours, ownEmptyQuietHours)
	}
	if len(inheritedFilters) != 1 || inheritedFilters[0] {
		t.Errorf("inheritedFilters = %v, want [false]", inheritedFilters)
	}
	if len(inheritedQuietHours) != 1 || inheritedQuietHours[0] {
		t.Errorf("inheritedQuietHours = %v, want [false]", inheritedQuietHours)
	}
}

// TestWebhookEndpointConfigsURLAndURLsEndpointsUnaffected pins §7 test 4: url
// and urls[] endpoints always inherit the block's Filters/QuietHours pointers
// (unchanged behaviour) but are never reported as "inherited" in the WARN
// sense -- that flag is reserved for targets[] entries whose behaviour
// actually changed by this fix. Regression guard: passes before and after.
func TestWebhookEndpointConfigsURLAndURLsEndpointsUnaffected(t *testing.T) {
	blockFilters := &WebhookFilters{IncludeGroups: []string{"lockbit"}}
	blockQuietHours := &QuietHours{Enabled: true, Start: "22:00", End: "06:00"}

	webhook := WebhookConfig{
		Enabled:    true,
		URL:        "https://discord.com/api/webhooks/1/a",
		URLs:       []string{"https://discord.com/api/webhooks/2/b", "https://discord.com/api/webhooks/3/c"},
		Filters:    blockFilters,
		QuietHours: blockQuietHours,
	}

	endpoints, inheritedFilters, inheritedQuietHours := webhookEndpointConfigs(webhook)
	if len(endpoints) != 3 {
		t.Fatalf("len(endpoints) = %d, want 3", len(endpoints))
	}
	for i, endpoint := range endpoints {
		if endpoint.Filters != blockFilters {
			t.Errorf("endpoint[%d].Filters = %p, want block pointer %p", i, endpoint.Filters, blockFilters)
		}
		if endpoint.QuietHours != blockQuietHours {
			t.Errorf("endpoint[%d].QuietHours = %p, want block pointer %p", i, endpoint.QuietHours, blockQuietHours)
		}
	}
	if len(inheritedFilters) != 3 {
		t.Fatalf("len(inheritedFilters) = %d, want 3", len(inheritedFilters))
	}
	for i, v := range inheritedFilters {
		if v {
			t.Errorf("inheritedFilters[%d] = true, want false (url/urls[] always inherit, never reported)", i)
		}
	}
	if len(inheritedQuietHours) != 3 {
		t.Fatalf("len(inheritedQuietHours) = %d, want 3", len(inheritedQuietHours))
	}
	for i, v := range inheritedQuietHours {
		if v {
			t.Errorf("inheritedQuietHours[%d] = true, want false (url/urls[] always inherit, never reported)", i)
		}
	}
}

// TestValidateConfigWarnsOnceForTargetInheritance pins §7 test 5.
func TestValidateConfigWarnsOnceForTargetInheritance(t *testing.T) {
	blockFilters := &WebhookFilters{IncludeGroups: []string{"lockbit"}}
	blockQuietHours := &QuietHours{Enabled: true, Start: "22:00", End: "06:00"}

	tests := []struct {
		name      string
		cfg       *Config
		wantWarns []map[string]any
	}{
		{
			name: "one block one bare target warns once",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = WebhookConfig{
					Enabled:    true,
					URL:        sharedDiscordURL,
					Filters:    blockFilters,
					QuietHours: blockQuietHours,
					Targets:    []WebhookTarget{{URL: otherDiscordURL}},
				}
				return cfg
			}(),
			wantWarns: []map[string]any{{
				"endpoint_ids":     "discord.ransomware.2",
				"endpoint_count":   1,
				"webhook_kind":     WebhookTypeRansomware,
				"webhook_platform": WebhookPlatformDiscord,
			}},
		},
		{
			name: "target with its own filters and quiet hours warns zero",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = WebhookConfig{
					Enabled:    true,
					URL:        sharedDiscordURL,
					Filters:    blockFilters,
					QuietHours: blockQuietHours,
					Targets: []WebhookTarget{{
						URL:        otherDiscordURL,
						Filters:    &WebhookFilters{IncludeGroups: []string{"alphv"}},
						QuietHours: &QuietHours{Enabled: true, Start: "23:00", End: "05:00"},
					}},
				}
				return cfg
			}(),
			wantWarns: nil,
		},
		{
			name: "target with explicit empty filters and quiet hours warns zero",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = WebhookConfig{
					Enabled:    true,
					URL:        sharedDiscordURL,
					Filters:    blockFilters,
					QuietHours: blockQuietHours,
					Targets: []WebhookTarget{{
						URL:        otherDiscordURL,
						Filters:    &WebhookFilters{},
						QuietHours: &QuietHours{},
					}},
				}
				return cfg
			}(),
			wantWarns: nil,
		},
		{
			name: "block with no filters or quiet hours and a bare target warns zero",
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
			wantWarns: nil,
		},
		{
			name: "two blocks each with one inheriting target warns twice",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = WebhookConfig{
					Enabled:    true,
					URL:        sharedDiscordURL,
					Filters:    blockFilters,
					QuietHours: blockQuietHours,
					Targets:    []WebhookTarget{{URL: otherDiscordURL}},
				}
				cfg.SlackWebhooks.RSS = WebhookConfig{
					Enabled:    true,
					URL:        sharedSlackURL,
					QuietHours: blockQuietHours,
					Targets:    []WebhookTarget{{URL: "https://hooks.slack.com/services/T00000000/B00000000/otherTargetFixtureToken"}}, //nolint:gosec // G101: test fixture, not a real credential
				}
				return cfg
			}(),
			wantWarns: []map[string]any{
				{
					"endpoint_ids":     "discord.ransomware.2",
					"endpoint_count":   1,
					"webhook_kind":     WebhookTypeRansomware,
					"webhook_platform": WebhookPlatformDiscord,
				},
				{
					"endpoint_ids":     "slack.rss.2",
					"endpoint_count":   1,
					"webhook_kind":     WebhookTypeRSS,
					"webhook_platform": WebhookPlatformSlack,
				},
			},
		},
		{
			// The rule is ONE warning per webhook block, not one per
			// inheriting target. Every other
			// fixture in this file produces a group of exactly one, so a
			// multi-member group was never exercised; this pins the real
			// behaviour of a group with more than one inheriting target.
			name: "one block three bare targets warns once with endpoint_count 3",
			cfg: func() *Config {
				cfg := DefaultConfig()
				cfg.APIKey = realAPIKey
				cfg.DiscordWebhooks.Ransomware = WebhookConfig{
					Enabled:    true,
					URL:        sharedDiscordURL,
					Filters:    blockFilters,
					QuietHours: blockQuietHours,
					Targets: []WebhookTarget{
						{URL: otherDiscordURL},
						{URL: "https://discord.com/api/webhooks/111111111111111111/secretGGGGHHHHIIII112233"}, //nolint:gosec // G101: test fixture, not a real credential
						{URL: "https://discord.com/api/webhooks/222222222222222222/secretJJJJKKKKLLLL445566"}, //nolint:gosec // G101: test fixture, not a real credential
					},
				}
				return cfg
			}(),
			wantWarns: []map[string]any{{
				"endpoint_ids":     "discord.ransomware.2, discord.ransomware.3, discord.ransomware.4",
				"endpoint_count":   3,
				"webhook_kind":     WebhookTypeRansomware,
				"webhook_platform": WebhookPlatformDiscord,
			}},
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
			warns := countTargetInheritanceWarnings(entries)
			if len(warns) != len(tc.wantWarns) {
				t.Fatalf("target-inheritance WARN count = %d, want %d (entries: %v)", len(warns), len(tc.wantWarns), warns)
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
			}
			if len(tc.wantWarns) > 1 {
				assertNoWebhookURLLeak(t, entries, sharedDiscordToken, sharedSlackToken)
			}
		})
	}
}

// TestTargetInheritanceGroupsFindsInheritingTargets pins §7 test 6.
func TestTargetInheritanceGroupsFindsInheritingTargets(t *testing.T) {
	blockFilters := &WebhookFilters{IncludeGroups: []string{"lockbit"}}

	t.Run("enabled block with one inheriting target", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.APIKey = realAPIKey
		cfg.DiscordWebhooks.Ransomware = WebhookConfig{
			Enabled: true,
			URL:     sharedDiscordURL,
			Filters: blockFilters,
			Targets: []WebhookTarget{{URL: otherDiscordURL}},
		}

		groups := TargetInheritanceGroups(cfg)
		if len(groups) != 1 {
			t.Fatalf("len(groups) = %d, want 1: %+v", len(groups), groups)
		}
		got := groups[0]
		if got.Kind != WebhookTypeRansomware {
			t.Errorf("Kind = %q, want %q", got.Kind, WebhookTypeRansomware)
		}
		if got.Platform != WebhookPlatformDiscord {
			t.Errorf("Platform = %q, want %q", got.Platform, WebhookPlatformDiscord)
		}
		wantIDs := []string{"discord.ransomware.2"}
		if len(got.EndpointIDs) != len(wantIDs) || got.EndpointIDs[0] != wantIDs[0] {
			t.Errorf("EndpointIDs = %v, want %v", got.EndpointIDs, wantIDs)
		}
	})

	t.Run("disabled block is excluded", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.DiscordWebhooks.Ransomware = WebhookConfig{
			Enabled: false,
			URL:     sharedDiscordURL,
			Filters: blockFilters,
			Targets: []WebhookTarget{{URL: otherDiscordURL}},
		}

		groups := TargetInheritanceGroups(cfg)
		if len(groups) != 0 {
			t.Fatalf("len(groups) = %d, want 0 for a disabled block: %+v", len(groups), groups)
		}
	})

	t.Run("nil config yields no groups", func(t *testing.T) {
		if groups := TargetInheritanceGroups(nil); groups != nil {
			t.Fatalf("TargetInheritanceGroups(nil) = %+v, want nil", groups)
		}
	})
}

// TestValidateWebhookFilterScopeActivatesOnInheritedBlockFilters pins an
// operator-decided fix: a block-level filters or
// quiet_hours value that was dead configuration before this fix (only
// targets[] children, all self-declaring their own) can now surface as a
// config-load failure once a bare target inherits it, and the resulting
// error must be wrapped with the block, field, reason, inheritance cause and
// both remedies -- reproduced byte-for-byte from a real --check-config run.
// A target with its OWN invalid filters must keep today's
// plain, unwrapped message.
func TestValidateWebhookFilterScopeActivatesOnInheritedBlockFilters(t *testing.T) {
	buildFiltersConfig := func() *Config {
		cfg := DefaultConfig()
		cfg.APIKey = realAPIKey
		cfg.DiscordWebhooks.Ransomware = WebhookConfig{
			Enabled: true,
			Filters: &WebhookFilters{IncludeCategories: []string{"malware"}}, // out of scope for ransomware
			Targets: []WebhookTarget{
				{URL: sharedDiscordURL, Filters: &WebhookFilters{IncludeGroups: []string{"lockbit"}}},
				{URL: otherDiscordURL},
			},
		}
		return cfg
	}
	buildQuietHoursConfig := func() *Config {
		cfg := DefaultConfig()
		cfg.APIKey = realAPIKey
		cfg.DiscordWebhooks.Ransomware = WebhookConfig{
			Enabled:    true,
			QuietHours: &QuietHours{Enabled: true, Start: "22:00", End: "22:00"}, // start == end, invalid
			Targets: []WebhookTarget{
				{URL: sharedDiscordURL, QuietHours: &QuietHours{Enabled: true, Start: "23:00", End: "05:00"}},
				{URL: otherDiscordURL},
			},
		}
		return cfg
	}
	buildOwnInvalidFiltersConfig := func() *Config {
		cfg := DefaultConfig()
		cfg.APIKey = realAPIKey
		cfg.DiscordWebhooks.Ransomware = WebhookConfig{
			Enabled: true,
			Filters: &WebhookFilters{IncludeGroups: []string{"lockbit"}}, // in scope, never reached by this target
			Targets: []WebhookTarget{
				{URL: otherDiscordURL, Filters: &WebhookFilters{IncludeCategories: []string{"malware"}}}, // its own, out of scope
			},
		}
		return cfg
	}

	tests := []struct {
		name    string
		cfg     *Config
		wantErr string
	}{
		{
			name:    "inherited filters row",
			cfg:     buildFiltersConfig(),
			wantErr: `webhook discord.ransomware.2: include_categories is only supported for RSS webhooks (target discord.ransomware.2 omits "filters" and inherited this value from the discord.ransomware webhook block; remove the invalid field from that block, or set an explicit "filters": {} on this target to opt out of inheriting it)`,
		},
		{
			name:    "inherited quiet_hours row",
			cfg:     buildQuietHoursConfig(),
			wantErr: `webhook discord.ransomware.2 quiet_hours: start and end must differ (both resolve to 22:00) (target discord.ransomware.2 omits "quiet_hours" and inherited this value from the discord.ransomware webhook block; remove the invalid field from that block, or set an explicit "quiet_hours": {} on this target to opt out of inheriting it)`,
		},
		{
			name:    "own invalid filters, not inherited, stays byte-identical to today",
			cfg:     buildOwnInvalidFiltersConfig(),
			wantErr: `webhook discord.ransomware: include_categories is only supported for RSS webhooks`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.cfg)
			if err == nil {
				t.Fatal("validateConfig() error = nil, want an error")
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("validateConfig() error =\n%q\nwant\n%q", err.Error(), tc.wantErr)
			}
		})
	}
}
