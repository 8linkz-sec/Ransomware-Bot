// Package filter provides per-webhook content filtering for API and RSS entries.
//
// Filter Logic:
//   - nil filters → accept everything (backward-compatible)
//   - Include rules (whitelist): if set, entry MUST match at least one value
//   - Exclude rules (blacklist): if matched, entry is dropped
//   - Field groups are AND-combined; values within a group are OR-combined
//   - Exclude is evaluated after include (exclude wins on conflict)
package filter

import (
	"regexp"
	"strings"
	"sync"

	"Ransomware-Bot/internal/api"
	"Ransomware-Bot/internal/config"
	"Ransomware-Bot/internal/rss"

	log "github.com/sirupsen/logrus"
)

// regexCache caches compiled regex patterns to avoid recompilation on every match
var regexCache sync.Map

// MatchesAPIEntry returns true if the API entry passes the webhook's filter rules.
// Returns true when filters is nil (no filtering configured).
func MatchesAPIEntry(filters *config.WebhookFilters, entry api.RansomwareEntry) bool {
	if filters == nil {
		return true
	}

	// Check group filter
	if !matchesField(filters.IncludeGroups, filters.ExcludeGroups, entry.Group, true) {
		return false
	}

	// Check country filter (case-insensitive)
	if !matchesField(filters.IncludeCountries, filters.ExcludeCountries, entry.Country, false) {
		return false
	}

	// Check activity filter
	if !matchesField(filters.IncludeActivities, filters.ExcludeActivities, entry.Activity, true) {
		return false
	}

	// Check keyword filter against Victim + Description
	searchText := entry.Victim + " " + entry.Description
	return matchesKeywords(filters.IncludeKeywords, filters.ExcludeKeywords, searchText, filters.KeywordMatchMode)
}

// MatchesRSSEntry returns true if the RSS entry passes the webhook's filter rules.
// Returns true when filters is nil (no filtering configured).
func MatchesRSSEntry(filters *config.WebhookFilters, entry rss.Entry) bool {
	if filters == nil {
		return true
	}

	// Check category filter (OR across entry categories vs filter values)
	if !matchesCategories(filters.IncludeCategories, filters.ExcludeCategories, entry.Categories) {
		return false
	}

	// Check keyword filter against Title + Description
	searchText := entry.Title + " " + entry.Description
	return matchesKeywords(filters.IncludeKeywords, filters.ExcludeKeywords, searchText, filters.KeywordMatchMode)
}

// matchesField checks a single-value field against include/exclude lists.
// caseSensitive controls whether comparison is exact or case-insensitive.
func matchesField(include, exclude []string, value string, caseSensitive bool) bool {
	// Include check: value must be in the include list
	if len(include) > 0 {
		found := false
		for _, inc := range include {
			if fieldEquals(inc, value, caseSensitive) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Exclude check: value must NOT be in the exclude list
	for _, exc := range exclude {
		if fieldEquals(exc, value, caseSensitive) {
			return false
		}
	}

	return true
}

// fieldEquals compares two strings, optionally case-insensitive.
func fieldEquals(a, b string, caseSensitive bool) bool {
	if caseSensitive {
		return a == b
	}
	return strings.EqualFold(a, b)
}

// matchesKeywords checks text against include/exclude keyword lists.
// mode is "literal" (default) or "regex".
func matchesKeywords(include, exclude []string, text, mode string) bool {
	useRegex := strings.EqualFold(mode, "regex")
	lowerText := strings.ToLower(text)

	// Include check: text must match at least one keyword
	if len(include) > 0 {
		found := false
		for _, kw := range include {
			if keywordMatch(kw, text, lowerText, useRegex) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Exclude check: text must NOT match any keyword
	for _, kw := range exclude {
		if keywordMatch(kw, text, lowerText, useRegex) {
			return false
		}
	}

	return true
}

// keywordMatch checks if a single keyword matches the text.
// Note: literal mode is case-insensitive; regex mode is case-sensitive by default
// (use (?i) prefix in regex patterns for case-insensitive matching).
func keywordMatch(keyword, text, lowerText string, useRegex bool) bool {
	if useRegex {
		re, err := getCompiledRegex(keyword)
		if err != nil {
			log.WithFields(log.Fields{
				"pattern": keyword,
				"error":   err,
			}).Warn("Invalid regex pattern in filter, skipping")
			return false
		}
		return re.MatchString(text)
	}
	// Literal: case-insensitive substring match
	return strings.Contains(lowerText, strings.ToLower(keyword))
}

// getCompiledRegex returns a cached compiled regex, compiling and caching on first use.
func getCompiledRegex(pattern string) (*regexp.Regexp, error) {
	if cached, ok := regexCache.Load(pattern); ok {
		if re, ok := cached.(*regexp.Regexp); ok {
			return re, nil
		}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexCache.Store(pattern, re)
	return re, nil
}

// matchesCategories checks entry categories against include/exclude category lists.
// A match means at least one entry category appears in the filter list (OR logic).
func matchesCategories(include, exclude []string, entryCategories []string) bool {
	// Include check: at least one entry category must be in the include list
	if len(include) > 0 {
		found := false
		for _, cat := range entryCategories {
			for _, inc := range include {
				if strings.EqualFold(cat, inc) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}

	// Exclude check: no entry category must be in the exclude list
	for _, cat := range entryCategories {
		for _, exc := range exclude {
			if strings.EqualFold(cat, exc) {
				return false
			}
		}
	}

	return true
}
