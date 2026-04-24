package textutil

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Pre-compiled regex patterns for HTML stripping
var (
	htmlTagRegex    = regexp.MustCompile(`<[^>]*>`)
	whitespaceRegex = regexp.MustCompile(`\s+`)
)

// DefangURL converts URLs to a safe format by replacing http/https with hxxp/hxxps.
// Returns the original string if the URL has no http/https prefix.
func DefangURL(url string) string {
	if url == "" {
		return url
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "https://") {
		return "hxxps://" + url[8:]
	}
	if strings.HasPrefix(lower, "http://") {
		return "hxxp://" + url[7:]
	}
	return url
}

// StripHTML removes HTML tags and decodes common HTML entities from text.
// Multiple whitespace characters are collapsed into a single space.
func StripHTML(input string) string {
	if input == "" {
		return ""
	}

	// Remove HTML tags using pre-compiled regex
	text := htmlTagRegex.ReplaceAllString(input, "")

	// Decode common HTML entities
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")

	// Clean up multiple spaces and trim
	text = whitespaceRegex.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// TruncateText truncates text to a maximum number of runes (Unicode-safe).
// If the text exceeds maxLength runes, it is truncated with "..." appended.
func TruncateText(text string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= maxLength {
		return text
	}
	if maxLength <= 3 {
		return string([]rune(text)[:maxLength])
	}
	return string([]rune(text)[:maxLength-3]) + "..."
}
