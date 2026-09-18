package config

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
)

// DestinationEndpoint binds one positional destination ID to the webhook URL it
// is configured with. The ID is the persisted routing identity used by dedup
// markers, the retry queue and dead letters.
type DestinationEndpoint struct {
	ID  string
	URL string
}

// MessengerForWebhookPlatform maps a configured webhook platform to the
// messenger value persisted in the status stores.
func MessengerForWebhookPlatform(platform string) (status.Messenger, bool) {
	switch platform {
	case WebhookPlatformDiscord:
		return status.MessengerDiscord, true
	case WebhookPlatformSlack:
		return status.MessengerSlack, true
	case WebhookPlatformSlackCompatible:
		return status.MessengerSlackCompatible, true
	default:
		return status.Messenger(""), false
	}
}

// AppendDestinationSuffix appends a positional endpoint suffix to a base
// destination ID. A blank suffix means the first endpoint of a block.
func AppendDestinationSuffix(base, suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return base
	}
	return base + "." + suffix
}

// APIDestinationID is the destination ID family for ransomware.live API alerts.
func APIDestinationID(messenger status.Messenger) string {
	return messenger.String() + "." + FeedTypeRansomware
}

// APIDestinationIDForTarget is APIDestinationID for one positional endpoint.
func APIDestinationIDForTarget(messenger status.Messenger, suffix string) string {
	return AppendDestinationSuffix(APIDestinationID(messenger), suffix)
}

// RSSDestinationID is the destination ID family for RSS deliveries of one feed type.
func RSSDestinationID(messenger status.Messenger, feedType string) string {
	return messenger.String() + "." + status.RetryItemTypeRSS.String() + "." + feedType
}

// RSSDestinationIDForTarget is RSSDestinationID for one positional endpoint.
func RSSDestinationIDForTarget(messenger status.Messenger, feedType, suffix string) string {
	return AppendDestinationSuffix(RSSDestinationID(messenger, feedType), suffix)
}

// rssFeedTypeForWebhookType mirrors the scheduler's feed-type to webhook-type
// routing, which is 1:1 in both directions.
func rssFeedTypeForWebhookType(webhookType string) (string, bool) {
	switch webhookType {
	case WebhookTypeRSS:
		return FeedTypeGeneral, true
	case WebhookTypeGovernment:
		return FeedTypeGovernment, true
	case WebhookTypeRansomware:
		return FeedTypeRansomware, true
	default:
		return "", false
	}
}

// DestinationEndpoints enumerates every configured webhook endpoint in
// configuration order, once per destination ID it owns. A ransomware block owns
// two ID families for the same URL: the API family and the ransomware RSS one.
//
// enabled is deliberately ignored. The suffix is positional and a disabled
// block keeps its positions, so a reorder performed while a block is disabled
// must still be visible when it is re-enabled.
func DestinationEndpoints(cfg *Config) []DestinationEndpoint {
	if cfg == nil {
		return nil
	}
	targets := WebhookTargets(cfg)
	endpoints := make([]DestinationEndpoint, 0, 2*len(targets))
	for _, target := range targets {
		endpoints = append(endpoints, destinationEndpointsForTarget(target)...)
	}
	return endpoints
}

// destinationEndpointsForTarget returns the destination IDs one flattened
// webhook endpoint owns. A blank URL, an unknown platform and an unknown
// webhook kind each contribute nothing.
func destinationEndpointsForTarget(target WebhookTargetConfig) []DestinationEndpoint {
	endpointURL := strings.TrimSpace(target.Webhook.URL)
	if endpointURL == "" {
		return nil
	}
	messenger, ok := MessengerForWebhookPlatform(target.Platform)
	if !ok {
		return nil
	}

	endpoints := make([]DestinationEndpoint, 0, 2)
	if target.Name == WebhookTypeRansomware {
		endpoints = append(endpoints, DestinationEndpoint{
			ID:  APIDestinationIDForTarget(messenger, target.DestinationSuffix),
			URL: endpointURL,
		})
	}
	feedType, ok := rssFeedTypeForWebhookType(target.Name)
	if !ok {
		return endpoints
	}
	return append(endpoints, DestinationEndpoint{
		ID:  RSSDestinationIDForTarget(messenger, feedType, target.DestinationSuffix),
		URL: endpointURL,
	})
}

// DestinationURLHash is the sha256 hex of the trimmed webhook URL. TrimSpace is
// the same normalisation webhookEndpointConfigs and the dedup composite key
// already use, so case and trailing-slash variants are different URLs here.
func DestinationURLHash(webhookURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(webhookURL)))
	return hex.EncodeToString(sum[:])
}

// DestinationURLHashes is what destinations.json stores: destination ID to the
// hash of its webhook URL. No URL, host or token is ever included.
func DestinationURLHashes(cfg *Config) map[string]string {
	endpoints := DestinationEndpoints(cfg)
	hashes := make(map[string]string, len(endpoints))
	for _, endpoint := range endpoints {
		hashes[endpoint.ID] = DestinationURLHash(endpoint.URL)
	}
	return hashes
}

// EnabledRSSFeedURLs returns the RSS feed URLs the scheduler actually polls:
// the URLs of every feed group whose webhook kind has at least one enabled
// endpoint, deduplicated and sorted. It mirrors the scheduler's
// rssFeedBatchesForConfig, which skips a group without an enabled target.
func EnabledRSSFeedURLs(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{})
	urls := make([]string, 0)
	for _, group := range cfg.Feeds.Groups() {
		if len(group.URLs) == 0 || !hasEnabledTargetForFeedType(cfg, group.Type) {
			continue
		}
		for _, url := range group.URLs {
			if url == "" {
				continue
			}
			if _, dup := seen[url]; dup {
				continue
			}
			seen[url] = struct{}{}
			urls = append(urls, url)
		}
	}
	sort.Strings(urls)
	return urls
}

// hasEnabledTargetForFeedType reports whether any configured webhook endpoint
// of the kind that routes feedType is enabled.
func hasEnabledTargetForFeedType(cfg *Config, feedType string) bool {
	for _, target := range WebhookTargets(cfg) {
		if !target.Webhook.Enabled {
			continue
		}
		if mapped, ok := rssFeedTypeForWebhookType(target.Name); ok && mapped == feedType {
			return true
		}
	}
	return false
}
