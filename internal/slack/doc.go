// Package slack formats and delivers Slack webhook notifications.
//
// The package builds Slack Block Kit payloads for ransomware API alerts and RSS
// entries, posts them to configured incoming webhooks, and maps HTTP,
// Retry-After, and validation responses into retryable or terminal delivery
// errors for the scheduler. Formatting is driven by notifyfmt.FormatOptions; nil
// format config values are treated as the repository defaults where supported.
package slack
