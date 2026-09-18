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
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/keywordmode"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
)

// Rules defines per-webhook include/exclude filter rules independent of any
// configuration file representation.
type Rules struct {
	IncludeGroups     []string
	IncludeCountries  []string
	IncludeActivities []string
	IncludeKeywords   []string
	IncludeCategories []string
	IncludeFields     map[string][]string

	ExcludeGroups     []string
	ExcludeCountries  []string
	ExcludeActivities []string
	ExcludeKeywords   []string
	ExcludeCategories []string
	ExcludeFields     map[string][]string

	KeywordMatchMode string
}

var apiFieldExtractors = map[string]func(model.RansomwareEntry) []string{
	"id":          func(entry model.RansomwareEntry) []string { return []string{entry.ID} },
	"group":       func(entry model.RansomwareEntry) []string { return []string{entry.Group} },
	"victim":      func(entry model.RansomwareEntry) []string { return []string{entry.Victim} },
	"country":     func(entry model.RansomwareEntry) []string { return []string{entry.Country} },
	"activity":    func(entry model.RansomwareEntry) []string { return []string{entry.Activity} },
	"attack_date": func(entry model.RansomwareEntry) []string { return []string{entry.AttackDate} },
	"claim_url":   func(entry model.RansomwareEntry) []string { return []string{entry.ClaimURL} },
	"website":     func(entry model.RansomwareEntry) []string { return []string{entry.WebsiteURL} },
	"description": func(entry model.RansomwareEntry) []string { return []string{entry.Description} },
	"screenshot":  func(entry model.RansomwareEntry) []string { return []string{entry.Screenshot} },
	"published":   func(entry model.RansomwareEntry) []string { return []string{formatFilterTime(entry.Published)} },
	"discovered":  func(entry model.RansomwareEntry) []string { return []string{formatFilterTime(entry.Discovered)} },
}

var rssFieldExtractors = map[string]func(model.RSSEntry) []string{
	"title":       func(entry model.RSSEntry) []string { return []string{entry.Title} },
	"link":        func(entry model.RSSEntry) []string { return []string{entry.Link} },
	"description": func(entry model.RSSEntry) []string { return []string{entry.Description} },
	"published":   func(entry model.RSSEntry) []string { return []string{formatFilterTime(entry.Published)} },
	"author":      func(entry model.RSSEntry) []string { return []string{entry.Author} },
	"category":    func(entry model.RSSEntry) []string { return entry.Categories },
	"categories":  func(entry model.RSSEntry) []string { return entry.Categories },
	"guid":        func(entry model.RSSEntry) []string { return []string{entry.GUID} },
	"feed_title":  func(entry model.RSSEntry) []string { return []string{entry.FeedTitle} },
	"feed_url":    func(entry model.RSSEntry) []string { return []string{entry.FeedURL} },
}

// searchTextFieldSeparator joins the fields keyword matching searches
// (apiKeywordSearchText, MatchesRSSEntry) so a multi-word keyword can never
// match purely because it spans the boundary between two fields -- in
// literal mode, exactly: a literal space, \s and \s+ can no longer bridge
// the seam. Regex mode's guarantee is narrower and, in one direction, does
// not hold at all: Go's regexp package decodes the invalid separator byte
// as the Unicode replacement character U+FFFD when it reads the string, and
// U+FFFD IS a non-space character, so a pattern built from \S, [^ ], [^\s],
// [[:^space:]] or an explicit \x{FFFD} still bridges the seam -- something
// a class like \S was previously guaranteed safe against, since the old
// space join gave it an actual space to reject. An escape for the literal
// byte (\xff) does NOT close this: regexp never sees the raw byte, only the
// character it decoded from it, so \xff aimed at the separator matches
// nothing. This is a known, accepted residual, not a design goal here --
// closing it would mean matching each field independently instead of
// joining them at all, a much larger change to a core delivery path. See
// MatchesAPIEntry's keyword comment for the concrete operator-facing
// consequence.
//
// It is the single byte 0xFF, which UTF-8 never assigns to any Unicode code
// point (RFC 3629 reserves 0xFE and 0xFF so UTF-8 text is always
// distinguishable from a UTF-16 byte-order mark): every keyword reaches this
// package through config validation (internal/config, via encoding/json,
// which substitutes the Unicode replacement character for any invalid byte
// rather than passing it through -- confirmed directly: json.Unmarshal on a
// string containing a raw 0xFF byte yields the replacement character, never
// 0xFF), and every entry field reaches it either the same way (the API
// client, also via encoding/json) or through the RSS parser, which keeps
// the raw byte 0xFF out by a different mechanism: encoding/xml rejects the
// entire feed outright on any invalid UTF-8 byte such as 0xFF --
// unconditionally, independent of Decoder.Strict -- and a feed that instead
// declares a legacy charset (windows-1252, x-user-defined, ...) has that
// byte TRANSCODED to a different Unicode code point (0xFF becomes U+00FF
// under windows-1252, U+F7FF under x-user-defined), never substituted with
// U+FFFD. Either mechanism keeps the raw byte 0xFF out of entry field text,
// so a configured keyword can never contain 0xFF, and this separator can
// never collide with one.
//
// strings.ToLower, used for case-insensitive literal matching, would corrupt
// a raw 0xFF byte the same way (turning it into the valid, keyword-reachable
// replacement character) if applied to the whole joined string at once --
// see lowerPreservingFieldSeparator, which case-folds each field
// independently for exactly this reason.
const searchTextFieldSeparator = "\xff"

// regexCache caches compiled regex patterns to avoid recompilation on every match
var regexCache sync.Map

// ClearRegexCache drops compiled regex filters, typically after config reload.
func ClearRegexCache() {
	regexCache.Range(func(key, value interface{}) bool {
		regexCache.Delete(key)
		return true
	})
}

// MatchesAPIEntry returns true if the API entry passes ransomware API filter
// rules. It consumes group, country, activity, and keyword filters; category
// filters are rejected for API webhooks during config validation.
// Returns true when filters is nil (no filtering configured).
func MatchesAPIEntry(rules *Rules, entry model.RansomwareEntry) bool {
	if rules == nil {
		return true
	}

	if !matchesCaseInsensitiveField(rules.IncludeGroups, rules.ExcludeGroups, entry.Group) {
		return false
	}

	if !matchesCaseInsensitiveField(rules.IncludeCountries, rules.ExcludeCountries, entry.Country) {
		return false
	}

	if !matchesCaseInsensitiveField(rules.IncludeActivities, rules.ExcludeActivities, entry.Activity) {
		return false
	}

	if !matchesRegisteredFields(rules.IncludeFields, rules.ExcludeFields, func(field string) []string {
		extract, ok := apiFieldExtractors[normalizeFieldName(field)]
		if !ok {
			return nil
		}
		return extract(entry)
	}) {
		return false
	}

	if !hasKeywordRules(rules) {
		return true
	}

	// Check keyword filter against the title, victim, description, group and
	// activity -- not every field visible in the alert: country, id,
	// attack_date, claim_url and website are not searched here (use
	// include_fields/exclude_fields for those). The fields are joined with
	// searchTextFieldSeparator, not a space, so in literal mode a keyword
	// can never match by spanning the boundary between two of them
	// (Title="acme" and Description="leak..." no longer match the keyword
	// "acme leak"); a genuine multi-word phrase inside a single field still
	// matches exactly as before. In regex mode the same holds for the
	// natural ways to write a phrase (a literal space, \s, \s+), but NOT
	// for every pattern: a class like \S, [^ ], [^\s] or [[:^space:]] still
	// bridges the boundary -- see searchTextFieldSeparator's doc comment
	// for the mechanism and why this is a known, accepted residual rather
	// than a bug in this fix. Concretely, an exclude_keywords regex built
	// on \S can suppress an entry that ought to have been delivered.
	searchText := apiKeywordSearchText(entry)
	return matchesKeywords(rules.IncludeKeywords, rules.ExcludeKeywords, searchText, rules.KeywordMatchMode)
}

func apiKeywordSearchText(entry model.RansomwareEntry) string {
	return strings.Join([]string{
		model.DisplayRansomwareTitle(entry),
		entry.Victim,
		entry.Description,
		entry.Group,
		entry.Activity,
	}, searchTextFieldSeparator)
}

// MatchesRSSEntry returns true if the RSS entry passes RSS filter rules. It
// consumes category and keyword filters; group, country, and activity filters
// are rejected for RSS webhooks during config validation.
// Returns true when filters is nil (no filtering configured).
func MatchesRSSEntry(rules *Rules, entry model.RSSEntry) bool {
	if rules == nil {
		return true
	}

	// Check category filter (OR across entry categories vs filter values)
	if !matchesCategories(rules.IncludeCategories, rules.ExcludeCategories, entry.Categories) {
		return false
	}

	if !matchesRegisteredFields(rules.IncludeFields, rules.ExcludeFields, func(field string) []string {
		extract, ok := rssFieldExtractors[normalizeFieldName(field)]
		if !ok {
			return nil
		}
		return extract(entry)
	}) {
		return false
	}

	if !hasKeywordRules(rules) {
		return true
	}

	// Check keyword filter against Title + Description, joined with
	// searchTextFieldSeparator so in literal mode a keyword can never match
	// by spanning the boundary between the two; regex mode has a residual
	// (see MatchesAPIEntry's comment for the mechanism and why).
	searchText := entry.Title + searchTextFieldSeparator + entry.Description
	return matchesKeywords(rules.IncludeKeywords, rules.ExcludeKeywords, searchText, rules.KeywordMatchMode)
}

func hasKeywordRules(rules *Rules) bool {
	return rules != nil && (len(rules.IncludeKeywords) > 0 || len(rules.ExcludeKeywords) > 0)
}

type ruleMatcher func(string) bool

func matchesIncludeExclude(include, exclude []string, matches ruleMatcher) bool {
	if len(include) > 0 && !matchesAnyRule(include, matches) {
		return false
	}
	return !matchesAnyRule(exclude, matches)
}

func matchesAnyRule(rules []string, matches ruleMatcher) bool {
	for _, rule := range rules {
		if matches(rule) {
			return true
		}
	}
	return false
}

func matchesCaseInsensitiveField(include, exclude []string, value string) bool {
	return matchesIncludeExclude(include, exclude, func(rule string) bool {
		return equalFoldTrimmed(rule, value)
	})
}

func matchesRegisteredFields(include, exclude map[string][]string, valuesFor func(string) []string) bool {
	for field, rules := range include {
		if len(rules) == 0 {
			continue
		}
		if !matchesAnyFieldRule(rules, valuesFor(field)) {
			return false
		}
	}
	for field, rules := range exclude {
		if matchesAnyFieldRule(rules, valuesFor(field)) {
			return false
		}
	}
	return true
}

func matchesAnyFieldRule(rules, values []string) bool {
	for _, value := range values {
		for _, rule := range rules {
			if containsFoldTrimmed(value, rule) {
				return true
			}
		}
	}
	return false
}

func equalFoldTrimmed(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

func containsFoldTrimmed(value, rule string) bool {
	rule = strings.TrimSpace(rule)
	value = strings.TrimSpace(value)
	if rule == "" || value == "" {
		return false
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(rule))
}

func normalizeFieldName(field string) string {
	return strings.ToLower(strings.TrimSpace(field))
}

func formatFilterTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

// matchesKeywords checks text against include/exclude keyword lists.
func matchesKeywords(include, exclude []string, text, mode string) bool {
	matcher := newKeywordMatcher(text, mode)
	return matchesIncludeExclude(include, exclude, matcher.matches)
}

type keywordMatcher struct {
	text      string
	lowerText string
	mode      string
}

func newKeywordMatcher(text, mode string) keywordMatcher {
	if keywordmode.IsRegex(mode) {
		return keywordMatcher{
			text: text,
			mode: keywordmode.Regex,
		}
	}
	return keywordMatcher{
		text:      text,
		lowerText: lowerPreservingFieldSeparator(text),
		mode:      keywordmode.Literal,
	}
}

// lowerPreservingFieldSeparator case-folds each field between
// searchTextFieldSeparator markers independently and rejoins them with the
// separator untouched. strings.ToLower on the whole joined string would
// instead decode the invalid 0xFF separator byte as broken UTF-8 and
// re-encode it as the valid Unicode replacement character -- a character a
// configured keyword genuinely can contain -- which would silently undo the
// guarantee that the separator can never appear inside a lower-cased
// keyword. Splitting first keeps the separator byte as-is; each field's
// content, coming from valid UTF-8, lower-cases to valid UTF-8 that can
// never contain 0xFF either.
func lowerPreservingFieldSeparator(text string) string {
	// Defensive, not a live path: both call sites (apiKeywordSearchText,
	// MatchesRSSEntry) always join at least two fields with the separator,
	// so text always contains it in production. This guards a caller that
	// might call this helper directly with an unjoined string.
	if !strings.Contains(text, searchTextFieldSeparator) {
		return strings.ToLower(text)
	}
	fields := strings.Split(text, searchTextFieldSeparator)
	for i, field := range fields {
		fields[i] = strings.ToLower(field)
	}
	return strings.Join(fields, searchTextFieldSeparator)
}

// matches checks if a single keyword matches the text.
// Note: literal mode is case-insensitive; regex mode is case-sensitive by default
// (use (?i) prefix in regex patterns for case-insensitive matching).
func (m keywordMatcher) matches(keyword string) bool {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return false
	}
	if m.mode == keywordmode.Regex {
		re, err := getCompiledRegex(keyword)
		if err != nil {
			return false
		}
		return re.MatchString(m.text)
	}
	return strings.Contains(m.lowerText, strings.ToLower(keyword))
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
	categorySet := normalizedCategorySet(entryCategories)
	return matchesIncludeExclude(include, exclude, func(rule string) bool {
		_, ok := categorySet[normalizeCategory(rule)]
		return ok
	})
}

func normalizedCategorySet(categories []string) map[string]struct{} {
	categorySet := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		category = normalizeCategory(category)
		if category != "" {
			categorySet[category] = struct{}{}
		}
	}
	return categorySet
}

func normalizeCategory(category string) string {
	return strings.ToLower(strings.TrimSpace(category))
}
