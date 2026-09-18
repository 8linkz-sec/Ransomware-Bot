package config

import "github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"

// FormatLabel resolves an operator-configured display label from the provided
// maps in precedence order. Keys are matched exactly and in normalized form so
// JSON keys such as "feed-title", "feed_title", and "Feed Title" behave alike.
func FormatLabel(keys []string, fallback string, labelMaps ...map[string]string) string {
	return notifyfmt.FormatLabel(keys, fallback, labelMaps...)
}
