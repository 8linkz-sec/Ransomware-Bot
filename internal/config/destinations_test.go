package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// destinationTestConfig fills all nine webhook blocks with a mix of url, urls
// and targets so the enumeration order and the suffix numbering are both
// exercised end to end.
func destinationTestConfig() *Config {
	cfg := DefaultConfig()
	cfg.DiscordWebhooks.Ransomware = WebhookConfig{
		Enabled: true,
		URL:     "https://discord.example/d-ransomware-1",
		URLs:    []string{"https://discord.example/d-ransomware-2"},
	}
	cfg.DiscordWebhooks.RSS = WebhookConfig{
		Enabled: true,
		URL:     "https://discord.example/d-rss-1",
	}
	cfg.DiscordWebhooks.Government = WebhookConfig{
		Enabled: true,
		Targets: []WebhookTarget{{URL: "https://discord.example/d-gov-1"}},
	}
	cfg.SlackWebhooks.Ransomware = WebhookConfig{
		Enabled: true,
		URLs:    []string{"https://slack.example/s-ransomware-1", "https://slack.example/s-ransomware-2"},
	}
	cfg.SlackWebhooks.RSS = WebhookConfig{
		Enabled: true,
		URL:     "https://slack.example/s-rss-1",
		Targets: []WebhookTarget{{URL: "https://slack.example/s-rss-2"}},
	}
	cfg.SlackWebhooks.Government = WebhookConfig{
		Enabled: true,
		URL:     "https://slack.example/s-gov-1",
	}
	cfg.SlackCompatibleWebhooks.Ransomware = WebhookConfig{
		Enabled: true,
		URL:     "https://compat.example/c-ransomware-1",
	}
	cfg.SlackCompatibleWebhooks.RSS = WebhookConfig{
		Enabled: true,
		URL:     "https://compat.example/c-rss-1",
	}
	cfg.SlackCompatibleWebhooks.Government = WebhookConfig{
		Enabled: true,
		URL:     "https://compat.example/c-gov-1",
		URLs:    []string{"https://compat.example/c-gov-2"},
	}
	return cfg
}

func TestDestinationEndpointsEnumeratesAPIAndRSSIDs(t *testing.T) {
	endpoints := DestinationEndpoints(destinationTestConfig())

	gotIDs := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		gotIDs = append(gotIDs, endpoint.ID)
	}

	// Configuration order: discord ransomware/rss/government, then slack, then
	// slack_compatible. A ransomware endpoint yields both ID families.
	wantIDs := []string{
		"discord.ransomware", "discord.rss.ransomware",
		"discord.ransomware.2", "discord.rss.ransomware.2",
		"discord.rss.general",
		"discord.rss.government",
		"slack.ransomware", "slack.rss.ransomware",
		"slack.ransomware.2", "slack.rss.ransomware.2",
		"slack.rss.general",
		"slack.rss.general.2",
		"slack.rss.government",
		"slack_compatible.ransomware", "slack_compatible.rss.ransomware",
		"slack_compatible.rss.general",
		"slack_compatible.rss.government",
		"slack_compatible.rss.government.2",
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("DestinationEndpoints IDs =\n%v\nwant\n%v", gotIDs, wantIDs)
	}

	// The two ID families of one ransomware endpoint carry the same URL.
	urls := map[string]string{}
	for _, endpoint := range endpoints {
		urls[endpoint.ID] = endpoint.URL
	}
	if urls["discord.ransomware"] != urls["discord.rss.ransomware"] {
		t.Fatalf("discord.ransomware URL = %q, discord.rss.ransomware URL = %q, want equal",
			urls["discord.ransomware"], urls["discord.rss.ransomware"])
	}
	if urls["slack_compatible.rss.government.2"] != "https://compat.example/c-gov-2" {
		t.Fatalf("slack_compatible.rss.government.2 URL = %q", urls["slack_compatible.rss.government.2"])
	}
}

func TestDestinationEndpointsHandlesNilConfig(t *testing.T) {
	if got := DestinationEndpoints(nil); got != nil {
		t.Fatalf("DestinationEndpoints(nil) = %v, want nil", got)
	}
	if got := DestinationURLHashes(nil); len(got) != 0 {
		t.Fatalf("DestinationURLHashes(nil) = %v, want empty", got)
	}
}

func TestDestinationEndpointsIncludesDisabledBlocksAndSkipsBlankURLs(t *testing.T) {
	cfg := DefaultConfig()
	// Disabled on purpose: enabled never shifts positions, so a swap performed
	// while a block is disabled must still be caught when it is re-enabled.
	cfg.SlackWebhooks.Government = WebhookConfig{
		Enabled: false,
		URL:     "https://slack.example/disabled-gov",
	}
	// No URL at all: appendWebhookTargets still appends a URL-less target.
	cfg.DiscordWebhooks.RSS = WebhookConfig{Enabled: true}
	// Whitespace-only entries are dropped before numbering.
	cfg.DiscordWebhooks.Ransomware = WebhookConfig{
		Enabled: true,
		URL:     "https://discord.example/first",
		URLs:    []string{"   ", "https://discord.example/second"},
	}
	// Same for targets[]: a whitespace-only targets[].url is dropped before
	// numbering and does not consume a destination suffix -- the real target
	// right after it gets suffix "2", not "3" (webhookEndpointConfigs'
	// blank-target-URL skip branch, config.go L1096-1098).
	cfg.DiscordWebhooks.Government = WebhookConfig{
		Enabled: true,
		URL:     "https://discord.example/gov-first",
		Targets: []WebhookTarget{
			{URL: "   "},
			{URL: "https://discord.example/gov-second"},
		},
	}

	gotIDs := make([]string, 0)
	for _, endpoint := range DestinationEndpoints(cfg) {
		gotIDs = append(gotIDs, endpoint.ID)
	}

	wantIDs := []string{
		"discord.ransomware", "discord.rss.ransomware",
		"discord.ransomware.2", "discord.rss.ransomware.2",
		"discord.rss.government", "discord.rss.government.2",
		"slack.rss.government",
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("DestinationEndpoints IDs = %v, want %v", gotIDs, wantIDs)
	}
}

func TestDestinationURLHashIsTrimmedSHA256AndHidesTheURL(t *testing.T) {
	const webhookURL = "https://discord.com/api/webhooks/123456789012345678/secretTOKEN0123456789"

	hash := DestinationURLHash(webhookURL)
	if got := DestinationURLHash("  " + webhookURL + "\t"); got != hash {
		t.Fatalf("DestinationURLHash is not TrimSpace-normalised: %q vs %q", got, hash)
	}
	if len(hash) != 64 {
		t.Fatalf("hash length = %d, want 64", len(hash))
	}
	for _, r := range hash {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("hash %q contains a non lower-hex rune %q", hash, r)
		}
	}
	for _, fragment := range []string{"discord.com", "secretTOKEN0123456789", "123456789012345678", "https"} {
		if strings.Contains(hash, fragment) {
			t.Fatalf("hash %q leaks %q", hash, fragment)
		}
	}
	if DestinationURLHash(webhookURL+"/") == hash {
		t.Fatal("a trailing slash must produce a different hash")
	}
}

func TestDestinationURLHashesKeyedByDestinationID(t *testing.T) {
	cfg := destinationTestConfig()
	endpoints := DestinationEndpoints(cfg)

	hashes := DestinationURLHashes(cfg)
	if len(hashes) != len(endpoints) {
		t.Fatalf("DestinationURLHashes has %d rows, want %d", len(hashes), len(endpoints))
	}
	for _, endpoint := range endpoints {
		hash, ok := hashes[endpoint.ID]
		if !ok {
			t.Fatalf("DestinationURLHashes is missing %q", endpoint.ID)
		}
		if hash != DestinationURLHash(endpoint.URL) {
			t.Fatalf("hash for %q = %q, want %q", endpoint.ID, hash, DestinationURLHash(endpoint.URL))
		}
	}
}

func TestDestinationIDHelpersMatchTheSchedulerShapes(t *testing.T) {
	messenger, ok := MessengerForWebhookPlatform(WebhookPlatformSlackCompatible)
	if !ok {
		t.Fatal("MessengerForWebhookPlatform(slack_compatible) ok = false, want true")
	}
	if got := APIDestinationID(messenger); got != "slack_compatible.ransomware" {
		t.Fatalf("APIDestinationID() = %q", got)
	}
	if got := APIDestinationIDForTarget(messenger, " 4 "); got != "slack_compatible.ransomware.4" {
		t.Fatalf("APIDestinationIDForTarget() = %q", got)
	}
	if got := RSSDestinationID(messenger, FeedTypeGovernment); got != "slack_compatible.rss.government" {
		t.Fatalf("RSSDestinationID() = %q", got)
	}
	if got := RSSDestinationIDForTarget(messenger, FeedTypeGovernment, ""); got != "slack_compatible.rss.government" {
		t.Fatalf("RSSDestinationIDForTarget() = %q", got)
	}
	if got := AppendDestinationSuffix("base", "  "); got != "base" {
		t.Fatalf("AppendDestinationSuffix(base, blank) = %q, want base", got)
	}
	if _, ok := MessengerForWebhookPlatform("telegram"); ok {
		t.Fatal("MessengerForWebhookPlatform(telegram) ok = true, want false")
	}
}

// TestDestinationEndpointsForTargetRejectsUnknownPlatformsAndKinds covers the
// defensive guards that WebhookTargets itself can never trigger: it only ever
// emits the three known platforms and the three known webhook kinds.
func TestDestinationEndpointsForTargetRejectsUnknownPlatformsAndKinds(t *testing.T) {
	webhook := WebhookConfig{Enabled: true, URL: "https://example.invalid/hook"}

	tests := []struct {
		name    string
		target  WebhookTargetConfig
		wantIDs []string
	}{
		{
			name:   "blank url yields nothing",
			target: WebhookTargetConfig{Platform: WebhookPlatformDiscord, Name: WebhookTypeRSS, Webhook: WebhookConfig{URL: "  "}},
		},
		{
			name:   "unknown platform yields nothing",
			target: WebhookTargetConfig{Platform: "telegram", Name: WebhookTypeRSS, Webhook: webhook},
		},
		{
			name:   "unknown webhook kind yields nothing",
			target: WebhookTargetConfig{Platform: WebhookPlatformDiscord, Name: "pager", Webhook: webhook},
		},
		{
			name:    "ransomware kind yields both families",
			target:  WebhookTargetConfig{Platform: WebhookPlatformDiscord, Name: WebhookTypeRansomware, DestinationSuffix: "2", Webhook: webhook},
			wantIDs: []string{"discord.ransomware.2", "discord.rss.ransomware.2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make([]string, 0)
			for _, endpoint := range destinationEndpointsForTarget(tt.target) {
				got = append(got, endpoint.ID)
			}
			want := tt.wantIDs
			if want == nil {
				want = []string{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("destinationEndpointsForTarget IDs = %v, want %v", got, want)
			}
		})
	}
}

func TestRSSFeedTypeForWebhookTypeMirrorsTheSchedulerRouting(t *testing.T) {
	tests := []struct {
		webhookType string
		wantFeed    string
		wantOK      bool
	}{
		{webhookType: WebhookTypeRSS, wantFeed: FeedTypeGeneral, wantOK: true},
		{webhookType: WebhookTypeGovernment, wantFeed: FeedTypeGovernment, wantOK: true},
		{webhookType: WebhookTypeRansomware, wantFeed: FeedTypeRansomware, wantOK: true},
		{webhookType: "pager"},
		{webhookType: ""},
	}

	for _, tt := range tests {
		t.Run(tt.webhookType, func(t *testing.T) {
			feedType, ok := rssFeedTypeForWebhookType(tt.webhookType)
			if ok != tt.wantOK || feedType != tt.wantFeed {
				t.Fatalf("rssFeedTypeForWebhookType(%q) = (%q, %v), want (%q, %v)",
					tt.webhookType, feedType, ok, tt.wantFeed, tt.wantOK)
			}
		})
	}
}

// enabledRSSFeedTestConfig is a bare config: no webhook enabled, no feed URL.
// Each case switches on exactly what it needs.
func enabledRSSFeedTestConfig() *Config {
	cfg := DefaultConfig()
	cfg.Feeds = FeedConfig{}
	cfg.DiscordWebhooks.Ransomware = WebhookConfig{}
	cfg.DiscordWebhooks.RSS = WebhookConfig{}
	cfg.DiscordWebhooks.Government = WebhookConfig{}
	cfg.SlackWebhooks.Ransomware = WebhookConfig{}
	cfg.SlackWebhooks.RSS = WebhookConfig{}
	cfg.SlackWebhooks.Government = WebhookConfig{}
	cfg.SlackCompatibleWebhooks.Ransomware = WebhookConfig{}
	cfg.SlackCompatibleWebhooks.RSS = WebhookConfig{}
	cfg.SlackCompatibleWebhooks.Government = WebhookConfig{}
	return cfg
}

func TestEnabledRSSFeedURLs(t *testing.T) {
	const (
		generalA = "https://general-a.example/feed.xml"
		generalB = "https://general-b.example/feed.xml"
		govA     = "https://gov-a.example/feed.xml"
		ransomA  = "https://ransom-a.example/feed.xml"
		shared   = "https://shared.example/feed.xml"
	)

	tests := []struct {
		name  string
		build func() *Config
		want  []string
	}{
		{
			name:  "nil config",
			build: func() *Config { return nil },
			want:  nil,
		},
		{
			name: "feeds configured but no webhook enabled",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.Feeds.GeneralFeeds = []string{generalA}
				cfg.Feeds.GovernmentFeeds = []string{govA}
				cfg.DiscordWebhooks.RSS = WebhookConfig{Enabled: false, URL: "https://discord.example/rss"}
				return cfg
			},
			want: []string{},
		},
		{
			name: "discord rss enabled returns general feeds only",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.Feeds.GeneralFeeds = []string{generalB, generalA}
				cfg.Feeds.GovernmentFeeds = []string{govA}
				cfg.Feeds.RansomwareFeeds = []string{ransomA}
				cfg.DiscordWebhooks.RSS = WebhookConfig{Enabled: true, URL: "https://discord.example/rss"}
				return cfg
			},
			want: []string{generalA, generalB},
		},
		{
			name: "slack government enabled returns government feeds only",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.Feeds.GeneralFeeds = []string{generalA}
				cfg.Feeds.GovernmentFeeds = []string{govA}
				cfg.SlackWebhooks.Government = WebhookConfig{Enabled: true, URL: "https://slack.example/gov"}
				return cfg
			},
			want: []string{govA},
		},
		{
			name: "slack-compatible ransomware enabled returns ransomware feeds only",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.Feeds.GeneralFeeds = []string{generalA}
				cfg.Feeds.RansomwareFeeds = []string{ransomA}
				cfg.SlackCompatibleWebhooks.Ransomware = WebhookConfig{Enabled: true, URL: "https://hooks.example/ransomware"}
				return cfg
			},
			want: []string{ransomA},
		},
		{
			name: "url listed in two enabled groups is returned once",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.Feeds.GeneralFeeds = []string{shared}
				cfg.Feeds.GovernmentFeeds = []string{shared}
				cfg.DiscordWebhooks.RSS = WebhookConfig{Enabled: true, URL: "https://discord.example/rss"}
				cfg.SlackWebhooks.Government = WebhookConfig{Enabled: true, URL: "https://slack.example/gov"}
				return cfg
			},
			want: []string{shared},
		},
		{
			name: "url shared across kinds is returned when only the other kind is enabled",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.Feeds.GeneralFeeds = []string{shared}
				cfg.Feeds.GovernmentFeeds = []string{shared}
				cfg.SlackWebhooks.Government = WebhookConfig{Enabled: true, URL: "https://slack.example/gov"}
				return cfg
			},
			want: []string{shared},
		},
		{
			name: "url listed only under government is not returned when only rss is enabled",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.Feeds.GeneralFeeds = []string{generalA}
				cfg.Feeds.GovernmentFeeds = []string{shared}
				cfg.DiscordWebhooks.RSS = WebhookConfig{Enabled: true, URL: "https://discord.example/rss"}
				return cfg
			},
			want: []string{generalA},
		},
		{
			name: "blank urls are dropped",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.Feeds.GeneralFeeds = []string{"", generalA, ""}
				cfg.DiscordWebhooks.RSS = WebhookConfig{Enabled: true, URL: "https://discord.example/rss"}
				return cfg
			},
			want: []string{generalA},
		},
		{
			name: "enabled webhook without feed urls yields nothing",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.DiscordWebhooks.RSS = WebhookConfig{Enabled: true, URL: "https://discord.example/rss"}
				return cfg
			},
			want: []string{},
		},
		{
			name: "result is sorted across groups",
			build: func() *Config {
				cfg := enabledRSSFeedTestConfig()
				cfg.Feeds.GeneralFeeds = []string{generalB}
				cfg.Feeds.GovernmentFeeds = []string{govA}
				cfg.Feeds.RansomwareFeeds = []string{ransomA}
				cfg.DiscordWebhooks.RSS = WebhookConfig{Enabled: true, URL: "https://discord.example/rss"}
				cfg.SlackWebhooks.Government = WebhookConfig{Enabled: true, URL: "https://slack.example/gov"}
				cfg.SlackCompatibleWebhooks.Ransomware = WebhookConfig{Enabled: true, URL: "https://hooks.example/ransomware"}
				return cfg
			},
			want: []string{generalB, govA, ransomA},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EnabledRSSFeedURLs(tt.build())
			if tt.want == nil {
				if got != nil {
					t.Fatalf("EnabledRSSFeedURLs() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("EnabledRSSFeedURLs() = %v, want %v", got, tt.want)
			}
			if len(tt.want) > 0 && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("EnabledRSSFeedURLs() = %v, want %v", got, tt.want)
			}
			if !sort.StringsAreSorted(got) {
				t.Fatalf("EnabledRSSFeedURLs() = %v, want a sorted result", got)
			}
		})
	}
}

// TestEnabledRSSFeedURLsMatchesSchedulerRouting is the written-down mirror of
// the scheduler's rssFeedBatchesForConfig: a feed group is polled exactly when
// its webhook kind has at least one enabled endpoint. If the scheduler's rule
// changes, this test must fail.
//
// One known divergence, unreachable today: webhookTargetsForRoute additionally
// skips a target whose platform has no messenger (messengerForWebhookPlatform),
// while hasEnabledTargetForFeedType checks only Webhook.Enabled. WebhookTargets
// only ever emits the three known platforms, so the two cannot disagree today —
// a fourth platform would make them disagree, and this comment is the record.
func TestEnabledRSSFeedURLsMatchesSchedulerRouting(t *testing.T) {
	cfg := destinationTestConfig()
	cfg.Feeds = FeedConfig{
		GeneralFeeds:    []string{"https://g1.example/feed", "https://g2.example/feed"},
		GovernmentFeeds: []string{"https://gov1.example/feed"},
		RansomwareFeeds: []string{"https://r1.example/feed"},
	}
	cfg.DiscordWebhooks.RSS.Enabled = false
	cfg.SlackWebhooks.RSS.Enabled = false
	cfg.SlackCompatibleWebhooks.RSS.Enabled = false

	want := referenceEnabledRSSFeedURLs(cfg)
	got := EnabledRSSFeedURLs(cfg)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnabledRSSFeedURLs() = %v, want the scheduler-routed set %v", got, want)
	}
	for _, url := range got {
		if strings.HasPrefix(url, "https://g1") || strings.HasPrefix(url, "https://g2") {
			t.Fatalf("EnabledRSSFeedURLs() = %v, want no general feed while every rss webhook is disabled", got)
		}
	}

	cfg.DiscordWebhooks.RSS.Enabled = true
	want = referenceEnabledRSSFeedURLs(cfg)
	got = EnabledRSSFeedURLs(cfg)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnabledRSSFeedURLs() after enabling discord rss = %v, want %v", got, want)
	}
	if len(got) != 4 {
		t.Fatalf("EnabledRSSFeedURLs() = %v, want all four feed URLs once discord rss is enabled", got)
	}
}

// referenceEnabledRSSFeedURLs recomputes the expected set from the scheduler's
// rule, independently of the implementation under test.
func referenceEnabledRSSFeedURLs(cfg *Config) []string {
	enabledKinds := map[string]bool{}
	for _, target := range WebhookTargets(cfg) {
		if target.Webhook.Enabled {
			enabledKinds[target.Name] = true
		}
	}
	seen := map[string]bool{}
	urls := []string{}
	for _, group := range []struct {
		webhookType string
		feedURLs    []string
	}{
		{WebhookTypeRSS, cfg.Feeds.GeneralFeeds},
		{WebhookTypeGovernment, cfg.Feeds.GovernmentFeeds},
		{WebhookTypeRansomware, cfg.Feeds.RansomwareFeeds},
	} {
		if !enabledKinds[group.webhookType] {
			continue
		}
		for _, url := range group.feedURLs {
			if url == "" || seen[url] {
				continue
			}
			seen[url] = true
			urls = append(urls, url)
		}
	}
	sort.Strings(urls)
	return urls
}
