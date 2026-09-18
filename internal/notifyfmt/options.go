package notifyfmt

import (
	"strings"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
)

const (
	DefaultSlackTitleText      = "Ransomware Alert"
	DefaultSlackRSSText        = "RSS Feed Update"
	DefaultRSSTitleText        = "RSS Feed Update"
	DefaultDescriptionMaxChars = 500

	FeedTypeGovernment = "government"
	FeedTypeRansomware = "ransomware"
)

// FormatOptions defines formatter-facing options independent of JSON config
// storage.
type FormatOptions struct {
	ShowUnicodeFlags bool
	DisplayLocale    string
	TimestampFormat  string
	DisplayTimezone  string
	ShowEmptyFields  bool
	EmptyFieldText   string
	FieldOrder       []string
	FieldLabels      map[string]string
	RSS              RSSFormatOptions
	Discord          DiscordFormatOptions
	Slack            SlackFormatOptions
}

type DiscordFormatOptions struct {
	ShowIcons           bool
	RansomwareColor     string
	RSSColor            string
	GovernmentColor     string
	DescriptionMaxChars int
	FieldLabels         map[string]string
}

type SlackFormatOptions struct {
	TitleText           string
	RSSText             string
	FieldOrder          []string
	DescriptionMaxChars int
	FieldLabels         map[string]string
}

type RSSFormatOptions struct {
	TitleText           string
	ShowAuthor          bool
	FieldOrder          []string
	DescriptionMaxChars int
	FieldLabels         map[string]string
}

func DefaultFormatOptions() FormatOptions {
	return FormatOptions{
		ShowUnicodeFlags: true,
		FieldOrder:       formatfields.DefaultDiscordFieldOrder(),
		EmptyFieldText:   "N/A",
		DisplayLocale:    "en",
		TimestampFormat:  "2006-01-02 15:04:05 MST",
		DisplayTimezone:  "UTC",
		Discord: DiscordFormatOptions{
			ShowIcons:           true,
			RansomwareColor:     "#ff0000",
			RSSColor:            "#0099ff",
			GovernmentColor:     "#ffa500",
			DescriptionMaxChars: DefaultDescriptionMaxChars,
		},
		Slack: SlackFormatOptions{
			TitleText:           DefaultSlackTitleText,
			RSSText:             DefaultSlackRSSText,
			FieldOrder:          formatfields.DefaultSlackFieldOrder(),
			DescriptionMaxChars: DefaultDescriptionMaxChars,
		},
		RSS: RSSFormatOptions{
			TitleText:  DefaultRSSTitleText,
			FieldOrder: formatfields.DefaultRSSFieldOrder(),
		},
	}
}

// FormatLabel resolves an operator-configured display label from the provided
// maps in precedence order. Keys are matched exactly and in normalized form so
// keys such as "feed-title", "feed_title", and "Feed Title" behave alike.
func FormatLabel(keys []string, fallback string, labelMaps ...map[string]string) string {
	normalizedKeys := make(map[string]struct{}, len(keys)*2)
	for _, key := range keys {
		for _, candidate := range labelKeyCandidates(key) {
			normalizedKeys[candidate] = struct{}{}
		}
	}
	if len(normalizedKeys) == 0 {
		return fallback
	}

	for _, labels := range labelMaps {
		if len(labels) == 0 {
			continue
		}
		if label, ok := lookupFormatLabel(labels, normalizedKeys); ok {
			return label
		}
	}
	return fallback
}

func lookupFormatLabel(labels map[string]string, normalizedKeys map[string]struct{}) (string, bool) {
	for key, value := range labels {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		for _, candidate := range labelKeyCandidates(key) {
			if _, ok := normalizedKeys[candidate]; ok {
				return value, true
			}
		}
	}
	return "", false
}

func labelKeyCandidates(key string) []string {
	trimmed := strings.TrimSpace(key)
	normalized := normalizeLabelKey(trimmed)
	if trimmed == "" || normalized == "" {
		return nil
	}
	if trimmed == normalized {
		return []string{normalized}
	}
	return []string{trimmed, normalized}
}

func normalizeLabelKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.NewReplacer(" ", "_", "-", "_", ".", "_").Replace(key)
	for strings.Contains(key, "__") {
		key = strings.ReplaceAll(key, "__", "_")
	}
	return strings.Trim(key, "_")
}
