// Package discord formats and delivers Discord webhook notifications.
//
// The package builds Discord embeds for ransomware API alerts and RSS entries,
// validates webhook URLs before sending, and maps HTTP and rate-limit responses
// into retryable or terminal delivery errors for the scheduler. Formatting is
// driven by notifyfmt.FormatOptions; nil format config values are treated as the
// repository defaults.
package discord
