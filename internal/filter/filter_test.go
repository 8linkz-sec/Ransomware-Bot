package filter

import (
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/keywordmode"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

// --- MatchesAPIEntry tests ---

func TestMatchesAPIEntry_NilFilters(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Country: "US"}
	if !MatchesAPIEntry(nil, entry) {
		t.Error("nil filters should accept everything")
	}
}

func TestMatchesAPIEntry_EmptyFilters(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Country: "US"}
	if !MatchesAPIEntry(&Rules{}, entry) {
		t.Error("empty filters should accept everything")
	}
}

func TestMatchesAPIEntry_IncludeGroups(t *testing.T) {
	f := &Rules{IncludeGroups: []string{"LockBit", "BlackCat"}}

	if !MatchesAPIEntry(f, api.RansomwareEntry{Group: "LockBit"}) {
		t.Error("should match included group")
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Group: "Conti"}) {
		t.Error("should reject non-included group")
	}
}

func TestMatchesAPIEntry_ExcludeGroups(t *testing.T) {
	f := &Rules{ExcludeGroups: []string{"TestGroup"}}

	if !MatchesAPIEntry(f, api.RansomwareEntry{Group: "LockBit"}) {
		t.Error("should accept non-excluded group")
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Group: "TestGroup"}) {
		t.Error("should reject excluded group")
	}
}

func TestMatchesAPIEntry_GroupsCaseInsensitive(t *testing.T) {
	include := &Rules{IncludeGroups: []string{" lockbit "}}
	if !MatchesAPIEntry(include, api.RansomwareEntry{Group: "LockBit"}) {
		t.Error("include_groups should match group case-insensitively with whitespace trimmed")
	}

	exclude := &Rules{ExcludeGroups: []string{"lockbit"}}
	if MatchesAPIEntry(exclude, api.RansomwareEntry{Group: "LockBit"}) {
		t.Error("exclude_groups should reject group case-insensitively")
	}
}

func TestMatchesAPIEntry_IncludeCountriesCaseInsensitive(t *testing.T) {
	f := &Rules{IncludeCountries: []string{"DE", "AT"}}

	if !MatchesAPIEntry(f, api.RansomwareEntry{Country: "de"}) {
		t.Error("country match should be case-insensitive")
	}
	if !MatchesAPIEntry(f, api.RansomwareEntry{Country: "DE"}) {
		t.Error("should match exact case")
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Country: "US"}) {
		t.Error("should reject non-included country")
	}
}

func TestMatchesAPIEntry_ExcludeCountries(t *testing.T) {
	f := &Rules{ExcludeCountries: []string{"CN"}}

	if !MatchesAPIEntry(f, api.RansomwareEntry{Country: "US"}) {
		t.Error("should accept non-excluded country")
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Country: "cn"}) {
		t.Error("should reject excluded country case-insensitively")
	}
}

func TestMatchesAPIEntry_IncludeActivities(t *testing.T) {
	f := &Rules{IncludeActivities: []string{"published"}}

	if !MatchesAPIEntry(f, api.RansomwareEntry{Activity: "published"}) {
		t.Error("should match included activity")
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Activity: "removed"}) {
		t.Error("should reject non-included activity")
	}
}

func TestMatchesAPIEntry_ActivitiesCaseInsensitive(t *testing.T) {
	include := &Rules{IncludeActivities: []string{"published"}}
	if !MatchesAPIEntry(include, api.RansomwareEntry{Activity: "Published"}) {
		t.Error("include_activities should match activity case-insensitively")
	}

	exclude := &Rules{ExcludeActivities: []string{" leaked "}}
	if MatchesAPIEntry(exclude, api.RansomwareEntry{Activity: "Leaked"}) {
		t.Error("exclude_activities should reject activity case-insensitively with whitespace trimmed")
	}
}

func TestMatchesAPIEntry_KeywordsLiteral(t *testing.T) {
	f := &Rules{IncludeKeywords: []string{"hospital"}}

	if !MatchesAPIEntry(f, api.RansomwareEntry{Victim: "City Hospital"}) {
		t.Error("should match keyword in Victim (case-insensitive)")
	}
	if !MatchesAPIEntry(f, api.RansomwareEntry{Description: "attacked a hospital"}) {
		t.Error("should match keyword in Description")
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Victim: "Acme Corp"}) {
		t.Error("should reject when keyword not found")
	}
}

func TestMatchesAPIEntry_BlankKeywordFiltersDoNotMatchEverything(t *testing.T) {
	entry := api.RansomwareEntry{Victim: "City Hospital"}

	if MatchesAPIEntry(&Rules{IncludeKeywords: []string{""}}, entry) {
		t.Error("blank include keyword should not match every API entry")
	}
	if MatchesAPIEntry(&Rules{IncludeKeywords: []string{" "}}, entry) {
		t.Error("whitespace include keyword should not match every API entry")
	}
	if !MatchesAPIEntry(&Rules{ExcludeKeywords: []string{""}}, entry) {
		t.Error("blank exclude keyword should not reject every API entry")
	}
	if !MatchesAPIEntry(&Rules{ExcludeKeywords: []string{" "}}, entry) {
		t.Error("whitespace exclude keyword should not reject every API entry")
	}
}

func TestMatchesAPIEntry_KeywordFiltersTrimWhitespace(t *testing.T) {
	if !MatchesAPIEntry(&Rules{IncludeKeywords: []string{" hospital "}}, api.RansomwareEntry{Victim: "City Hospital"}) {
		t.Error("include keyword should match after trimming surrounding whitespace")
	}
	if MatchesAPIEntry(&Rules{ExcludeKeywords: []string{" test "}}, api.RansomwareEntry{Victim: "Test Company"}) {
		t.Error("exclude keyword should reject after trimming surrounding whitespace")
	}
}

func TestMatchesAPIEntry_KeywordsIncludeVisibleAlertFields(t *testing.T) {
	tests := []struct {
		name    string
		keyword string
		entry   api.RansomwareEntry
	}{
		{
			name:    "display title",
			keyword: "unknown victim",
			entry:   api.RansomwareEntry{Group: "LockBit"},
		},
		{
			name:    "group",
			keyword: "lockbit",
			entry:   api.RansomwareEntry{Group: "LockBit"},
		},
		{
			name:    "activity",
			keyword: "data leak",
			entry:   api.RansomwareEntry{Activity: "Data Leak"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Rules{IncludeKeywords: []string{tt.keyword}}
			if !MatchesAPIEntry(f, tt.entry) {
				t.Fatalf("keyword %q should match visible API alert fields for %+v", tt.keyword, tt.entry)
			}
		})
	}
}

func TestMatchesAPIEntry_KeywordsRegex(t *testing.T) {
	f := &Rules{
		IncludeKeywords:  []string{"(?i)health(care)?"},
		KeywordMatchMode: keywordmode.Regex,
	}

	if !MatchesAPIEntry(f, api.RansomwareEntry{Victim: "Healthcare Inc"}) {
		t.Error("should match regex in Victim")
	}
	if !MatchesAPIEntry(f, api.RansomwareEntry{Description: "Health sector"}) {
		t.Error("should match regex in Description")
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Victim: "Finance Corp"}) {
		t.Error("should reject when regex doesn't match")
	}
}

func TestMatchesAPIEntry_KeywordsRegexUsesOriginalCase(t *testing.T) {
	f := &Rules{
		IncludeKeywords:  []string{"ACME"},
		KeywordMatchMode: keywordmode.Regex,
	}

	if !MatchesAPIEntry(f, api.RansomwareEntry{Victim: "ACME Corp"}) {
		t.Error("regex mode should match original uppercase text")
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Victim: "acme corp"}) {
		t.Error("regex mode should not match lowercased text unless the pattern requests it")
	}
}

// Settled behaviour (was the cross-field seam defect): the reproduction
// case from the operator's decision. Victim="ShieldCorp" and
// Description="disclosed internal documents" -- neither field alone
// contains the phrase "shieldcorp disclosed", so the keyword must not match
// even though it used to, by spanning the space that joined the two fields.
func TestMatchesAPIEntryKeywordDoesNotSpanFieldBoundary(t *testing.T) {
	f := &Rules{IncludeKeywords: []string{"shieldcorp disclosed"}}
	entry := api.RansomwareEntry{Victim: "ShieldCorp", Description: "disclosed internal documents"}
	if MatchesAPIEntry(f, entry) {
		t.Fatal("keyword \"shieldcorp disclosed\" must not match Victim=\"ShieldCorp\" + " +
			"Description=\"disclosed...\" -- neither field alone contains the phrase")
	}
}

// The exclude-list mirror: a keyword that used to accidentally suppress an
// alert by spanning the Victim/Description boundary must no longer do so.
func TestMatchesAPIEntryExcludeKeywordDoesNotSpanFieldBoundary(t *testing.T) {
	f := &Rules{ExcludeKeywords: []string{"shieldcorp disclosed"}}
	entry := api.RansomwareEntry{Victim: "ShieldCorp", Description: "disclosed internal documents"}
	if !MatchesAPIEntry(f, entry) {
		t.Fatal("exclude keyword \"shieldcorp disclosed\" must not suppress Victim=\"ShieldCorp\" + " +
			"Description=\"disclosed...\" -- neither field alone contains the phrase")
	}
}

// A genuine multi-word phrase contained entirely within one field must
// still match: the fix changes cross-field adjacency, not in-field
// substring matching.
func TestMatchesAPIEntryKeywordStillMatchesPhraseWithinSingleField(t *testing.T) {
	f := &Rules{IncludeKeywords: []string{"shieldcorp disclosed"}}
	entry := api.RansomwareEntry{Description: "shieldcorp disclosed internal documents"}
	if !MatchesAPIEntry(f, entry) {
		t.Fatal("keyword \"shieldcorp disclosed\" should still match when the whole phrase is inside Description")
	}
}

// Regex mode mirror of the seam fix: a literal space inside a regex
// pattern used to match the join separator between fields; it must not
// match the searchTextFieldSeparator byte that replaced it.
func TestMatchesAPIEntryRegexKeywordDoesNotSpanFieldBoundary(t *testing.T) {
	f := &Rules{
		IncludeKeywords:  []string{"shieldcorp disclosed"},
		KeywordMatchMode: keywordmode.Regex,
	}
	entry := api.RansomwareEntry{Victim: "shieldcorp", Description: "disclosed internal documents"}
	if MatchesAPIEntry(f, entry) {
		t.Fatal("regex keyword \"shieldcorp disclosed\" must not match across the Victim/Description boundary")
	}
}

// Regex mode: a genuine multi-word phrase inside one field still matches.
func TestMatchesAPIEntryRegexKeywordStillMatchesPhraseWithinSingleField(t *testing.T) {
	f := &Rules{
		IncludeKeywords:  []string{"shieldcorp disclosed"},
		KeywordMatchMode: keywordmode.Regex,
	}
	entry := api.RansomwareEntry{Description: "shieldcorp disclosed internal documents"}
	if !MatchesAPIEntry(f, entry) {
		t.Fatal("regex keyword \"shieldcorp disclosed\" should still match when the whole phrase is inside Description")
	}
}

// An empty field sitting between two non-empty ones (Description="" between
// Victim and Group) used to leave two consecutive join spaces, which a
// two-space keyword could bridge on. The fixed join always inserts exactly
// one separator per gap regardless of the emptied field's content, so a
// two-space keyword must not bridge it either.
func TestMatchesAPIEntryKeywordDoesNotSpanEmptyFieldBetweenTwoNonEmptyFields(t *testing.T) {
	f := &Rules{IncludeKeywords: []string{"shieldcorp  disclosed"}} // two spaces
	entry := api.RansomwareEntry{Victim: "ShieldCorp", Description: "", Group: "disclosed"}
	if MatchesAPIEntry(f, entry) {
		t.Fatal("a two-space keyword must not bridge Victim -> (empty Description) -> Group")
	}
}

// Mirrors TestMatchesAPIEntryKeywordDoesNotSpanEmptyFieldBetweenTwoNonEmptyFields
// for the RSS whitespace-padding case, but stated at the API surface for
// completeness: a field ending in whitespace immediately before the join,
// and the next field starting with whitespace, must not recreate a
// bridgeable run of space characters.
func TestMatchesAPIEntryKeywordDoesNotSpanFieldBoundaryWithWhitespacePaddedFields(t *testing.T) {
	f := &Rules{IncludeKeywords: []string{"shieldcorp   disclosed"}} // three spaces
	entry := api.RansomwareEntry{Victim: "ShieldCorp ", Description: " disclosed internal documents"}
	if MatchesAPIEntry(f, entry) {
		t.Fatal("a three-space keyword must not bridge Victim's trailing space + the join + " +
			"Description's leading space")
	}
}

// A keyword literally equal to the separator byte cannot be produced by
// config loading (every keyword passes through encoding/json, which never
// yields a raw 0xFF byte -- see searchTextFieldSeparator's doc comment) but
// a caller that builds filter.Rules directly, bypassing config validation,
// technically could. Even then it must not match: strings.ToLower on a
// standalone invalid byte turns it into the (valid, three-byte) Unicode
// replacement character, which is not the same bytes as the separator that
// lowerPreservingFieldSeparator leaves untouched in the search text.
func TestKeywordEqualToSeparatorByteDoesNotMatchAnything(t *testing.T) {
	f := &Rules{IncludeKeywords: []string{searchTextFieldSeparator}}
	entry := api.RansomwareEntry{Victim: "ShieldCorp", Description: "disclosed internal documents"}
	if MatchesAPIEntry(f, entry) {
		t.Fatal("a keyword equal to the raw separator byte must not match, including at the " +
			"field boundary it sits at internally")
	}
}

// Regex \b word-boundary behaviour around the new separator is unchanged
// from around the old space: the separator byte is not a word character
// (Go's regexp decodes an invalid byte as the Unicode replacement
// character, itself non-word), so \b at the edge of a field still finds a
// boundary there, exactly as it did next to a space.
func TestMatchesAPIEntryRegexWordBoundaryStillWorksNextToSeparator(t *testing.T) {
	f := &Rules{
		IncludeKeywords:  []string{`\bshieldcorp\b`},
		KeywordMatchMode: keywordmode.Regex,
	}
	entry := api.RansomwareEntry{Victim: "shieldcorp", Description: "disclosed internal documents"}
	if !MatchesAPIEntry(f, entry) {
		t.Fatal(`\bshieldcorp\b should still match the whole Victim field next to the field separator`)
	}
}

// The concrete regression named in the documentation fix: an exclude_keywords
// regex built on \S wrongly suppresses a ransomware alert that should have
// been delivered. This entry was delivered before the field-separator fix
// (the old space join let "shieldcorp disclosed" bridge Victim/Description)
// and, with a plain space keyword, is correctly delivered again after it --
// but \S is a class that was previously SAFE (a literal space is not \S,
// so it could never bridge the old join) and now bridges instead, because
// Go's regexp package decodes the invalid separator byte as U+FFFD while
// matching, and U+FFFD is itself a non-space character. This is the
// accepted residual documented on searchTextFieldSeparator, pinned here so
// a future change cannot move it silently.
func TestMatchesAPIEntryRegexExcludeKeywordNonSpaceClassWronglySuppressesAlert(t *testing.T) {
	f := &Rules{
		ExcludeKeywords:  []string{`(?i)shieldcorp\Sdisclosed`},
		KeywordMatchMode: keywordmode.Regex,
	}
	entry := api.RansomwareEntry{Victim: "ShieldCorp", Description: "disclosed internal documents"}
	if MatchesAPIEntry(f, entry) {
		t.Fatal("accepted residual: exclude regex \\S bridges the field boundary via the decoded " +
			"replacement character and wrongly suppresses this alert")
	}
}

// Broader pin of the same residual across every non-space character class
// the review measured, in both the include and exclude directions, plus the
// one sub-claim that goes the other way: \xff (the literal separator BYTE)
// does not match, because regexp never sees that raw byte -- only the
// character (U+FFFD) it decoded from it.
func TestMatchesAPIEntryRegexNonSpaceClassResidualAcrossFieldBoundary(t *testing.T) {
	entry := api.RansomwareEntry{Victim: "ShieldCorp", Description: "disclosed internal documents"}

	bridging := []string{
		`(?i)shieldcorp\Sdisclosed`,
		`(?i)shieldcorp[^ ]disclosed`,
		`(?i)shieldcorp[^\s]disclosed`,
		`(?i)shieldcorp[[:^space:]]disclosed`,
		`(?i)shieldcorp\x{FFFD}disclosed`,
	}
	for _, pattern := range bridging {
		t.Run(pattern, func(t *testing.T) {
			include := &Rules{IncludeKeywords: []string{pattern}, KeywordMatchMode: keywordmode.Regex}
			if !MatchesAPIEntry(include, entry) {
				t.Fatalf("include regex %q is expected to still bridge the field boundary (accepted residual)", pattern)
			}

			exclude := &Rules{ExcludeKeywords: []string{pattern}, KeywordMatchMode: keywordmode.Regex}
			if MatchesAPIEntry(exclude, entry) {
				t.Fatalf("exclude regex %q is expected to still bridge the field boundary and wrongly suppress this entry (accepted residual)", pattern)
			}
		})
	}

	nonBridging := &Rules{IncludeKeywords: []string{`(?i)shieldcorp\xffdisclosed`}, KeywordMatchMode: keywordmode.Regex}
	if MatchesAPIEntry(nonBridging, entry) {
		t.Fatal(`regex \xff must not match the separator -- regexp decodes it to U+FFFD before ` +
			`matching and never sees the raw byte`)
	}
}

// include_fields groups are documented (package doc) as AND-combined: an
// entry must satisfy every group, not just one. An entry matching only the
// "group" rule but not the "country" rule must be rejected.
func TestMatchesAPIEntry_IncludeFieldsMultipleGroupsAreANDCombined(t *testing.T) {
	f := &Rules{IncludeFields: map[string][]string{
		"group":   {"lockbit"},
		"country": {"US"},
	}}
	entry := api.RansomwareEntry{Group: "LockBit", Country: "DE"}
	if MatchesAPIEntry(f, entry) {
		t.Fatal("entry matching only one of two include_fields groups (group but not country) " +
			"must be rejected -- groups are AND-combined, not OR-combined")
	}
}

// The keyword search field set is deliberately narrow (title, victim,
// description, group, activity -- see MatchesAPIEntry's keyword comment);
// entry.Country must not be part of it. Silently adding it would make an
// exclude_keywords value like "france" start suppressing every French
// victim's alert by country alone, with no other test catching it.
func TestMatchesAPIEntryKeywordSearchDoesNotIncludeCountry(t *testing.T) {
	f := &Rules{ExcludeKeywords: []string{"france"}}
	entry := api.RansomwareEntry{Country: "France", Victim: "Acme Corp", Description: "data leak"}
	if !MatchesAPIEntry(f, entry) {
		t.Fatal("keyword search must not include entry.Country -- exclude_keywords \"france\" " +
			"must not suppress a French victim by country alone")
	}
}

func TestMatchesAPIEntry_ExcludeKeywords(t *testing.T) {
	f := &Rules{ExcludeKeywords: []string{"test", "sandbox"}}

	if !MatchesAPIEntry(f, api.RansomwareEntry{Victim: "Real Corp"}) {
		t.Error("should accept when no exclude keyword matches")
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Victim: "Test Company"}) {
		t.Error("should reject when exclude keyword matches")
	}
}

func TestMatchesAPIEntry_CombinedFilters(t *testing.T) {
	f := &Rules{
		IncludeCountries: []string{"DE", "AT", "CH"},
		ExcludeGroups:    []string{"TestGroup"},
		IncludeKeywords:  []string{"hospital"},
	}

	// Matches all criteria
	if !MatchesAPIEntry(f, api.RansomwareEntry{
		Group:   "LockBit",
		Country: "DE",
		Victim:  "Berlin Hospital",
	}) {
		t.Error("should match when all criteria pass")
	}

	// Wrong country
	if MatchesAPIEntry(f, api.RansomwareEntry{
		Group:   "LockBit",
		Country: "US",
		Victim:  "US Hospital",
	}) {
		t.Error("should reject when country doesn't match include")
	}

	// Excluded group
	if MatchesAPIEntry(f, api.RansomwareEntry{
		Group:   "TestGroup",
		Country: "DE",
		Victim:  "Hospital DE",
	}) {
		t.Error("should reject when group is excluded")
	}

	// Missing keyword
	if MatchesAPIEntry(f, api.RansomwareEntry{
		Group:   "LockBit",
		Country: "DE",
		Victim:  "Acme Corp",
	}) {
		t.Error("should reject when keyword doesn't match")
	}
}

// --- MatchesRSSEntry tests ---

func TestMatchesRSSEntry_NilFilters(t *testing.T) {
	entry := rss.Entry{Title: "Alert"}
	if !MatchesRSSEntry(nil, entry) {
		t.Error("nil filters should accept everything")
	}
}

func TestMatchesRSSEntry_IncludeCategories(t *testing.T) {
	f := &Rules{IncludeCategories: []string{"security", "malware"}}

	if !MatchesRSSEntry(f, rss.Entry{Categories: []string{"security", "news"}}) {
		t.Error("should match when entry has an included category")
	}
	if MatchesRSSEntry(f, rss.Entry{Categories: []string{"sports"}}) {
		t.Error("should reject when no category matches")
	}
	if MatchesRSSEntry(f, rss.Entry{Categories: []string{}}) {
		t.Error("should reject when entry has no categories and include is set")
	}
}

func TestMatchesRSSEntry_ExcludeCategories(t *testing.T) {
	f := &Rules{ExcludeCategories: []string{"spam"}}

	if !MatchesRSSEntry(f, rss.Entry{Categories: []string{"security"}}) {
		t.Error("should accept non-excluded category")
	}
	if MatchesRSSEntry(f, rss.Entry{Categories: []string{"news", "spam"}}) {
		t.Error("should reject when entry has an excluded category")
	}
}

func TestMatchesRSSEntry_Keywords(t *testing.T) {
	f := &Rules{IncludeKeywords: []string{"ransomware"}}

	if !MatchesRSSEntry(f, rss.Entry{Title: "New Ransomware Variant"}) {
		t.Error("should match keyword in Title (case-insensitive)")
	}
	if !MatchesRSSEntry(f, rss.Entry{Description: "A ransomware attack was discovered"}) {
		t.Error("should match keyword in Description")
	}
	if MatchesRSSEntry(f, rss.Entry{Title: "Weather Update"}) {
		t.Error("should reject when keyword not found")
	}
}

func TestMatchesRSSEntry_BlankKeywordFiltersDoNotMatchEverything(t *testing.T) {
	entry := rss.Entry{Title: "New Ransomware Variant"}

	if MatchesRSSEntry(&Rules{IncludeKeywords: []string{""}}, entry) {
		t.Error("blank include keyword should not match every RSS entry")
	}
	if !MatchesRSSEntry(&Rules{ExcludeKeywords: []string{""}}, entry) {
		t.Error("blank exclude keyword should not reject every RSS entry")
	}
}

func TestMatchesRSSEntry_KeywordFiltersTrimWhitespace(t *testing.T) {
	if !MatchesRSSEntry(&Rules{IncludeKeywords: []string{" ransomware "}}, rss.Entry{Title: "New Ransomware Variant"}) {
		t.Error("RSS include keyword should match after trimming surrounding whitespace")
	}
	if MatchesRSSEntry(&Rules{ExcludeKeywords: []string{" test "}}, rss.Entry{Title: "Test Feed Item"}) {
		t.Error("RSS exclude keyword should reject after trimming surrounding whitespace")
	}
}

func TestMatchesRSSEntry_CategoriesCaseInsensitive(t *testing.T) {
	f := &Rules{IncludeCategories: []string{"Security"}}

	if !MatchesRSSEntry(f, rss.Entry{Categories: []string{"security"}}) {
		t.Error("category match should be case-insensitive")
	}
}

func TestMatchesRSSEntry_CategoriesTrimWhitespace(t *testing.T) {
	include := &Rules{IncludeCategories: []string{" Malware "}}
	if !MatchesRSSEntry(include, rss.Entry{Categories: []string{"Malware "}}) {
		t.Error("include_categories should match RSS categories with surrounding whitespace trimmed")
	}

	exclude := &Rules{ExcludeCategories: []string{" Spam "}}
	if MatchesRSSEntry(exclude, rss.Entry{Categories: []string{" spam "}}) {
		t.Error("exclude_categories should reject RSS categories with surrounding whitespace trimmed")
	}
}

func TestClearRegexCacheRemovesCompiledPatterns(t *testing.T) {
	ClearRegexCache()
	t.Cleanup(ClearRegexCache)

	if _, err := getCompiledRegex("(?i)alpha"); err != nil {
		t.Fatalf("getCompiledRegex() error = %v", err)
	}
	if got := regexCacheLenForTest(); got != 1 {
		t.Fatalf("regex cache length = %d, want 1", got)
	}

	ClearRegexCache()

	if got := regexCacheLenForTest(); got != 0 {
		t.Fatalf("regex cache length after clear = %d, want 0", got)
	}
}

func TestMatchesAPIEntryWithoutKeywordRulesDoesNotUseRegexCache(t *testing.T) {
	ClearRegexCache()
	t.Cleanup(ClearRegexCache)

	f := &Rules{
		IncludeCountries: []string{"DE"},
		KeywordMatchMode: keywordmode.Regex,
	}
	entry := api.RansomwareEntry{
		Country:     "DE",
		Victim:      "Acme",
		Description: "Large description with no keyword rules",
	}

	if !MatchesAPIEntry(f, entry) {
		t.Fatal("field-only filters should still match without keyword rules")
	}
	if got := regexCacheLenForTest(); got != 0 {
		t.Fatalf("regex cache length = %d, want 0 without keyword rules", got)
	}
}

func regexCacheLenForTest() int {
	count := 0
	regexCache.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

func TestMatchesIncludeExcludeRules(t *testing.T) {
	tests := []struct {
		name    string
		include []string
		exclude []string
		matches func(string) bool
		want    bool
	}{
		{
			name:    "no rules accepts",
			matches: func(string) bool { return false },
			want:    true,
		},
		{
			name:    "include must match",
			include: []string{"alpha", "beta"},
			matches: func(rule string) bool { return rule == "beta" },
			want:    true,
		},
		{
			name:    "include miss rejects",
			include: []string{"alpha"},
			matches: func(string) bool { return false },
			want:    false,
		},
		{
			name:    "exclude match rejects",
			exclude: []string{"alpha"},
			matches: func(rule string) bool { return rule == "alpha" },
			want:    false,
		},
		{
			name:    "exclude wins over include",
			include: []string{"alpha"},
			exclude: []string{"alpha"},
			matches: func(rule string) bool { return rule == "alpha" },
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesIncludeExclude(tt.include, tt.exclude, tt.matches); got != tt.want {
				t.Fatalf("matchesIncludeExclude() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Edge cases ---

func TestMatchesAPIEntry_IncludeAndExcludeSameField(t *testing.T) {
	// Include DE, but also exclude DE → exclude wins
	f := &Rules{
		IncludeCountries: []string{"DE"},
		ExcludeCountries: []string{"DE"},
	}
	if MatchesAPIEntry(f, api.RansomwareEntry{Country: "DE"}) {
		t.Error("exclude should win when same value is in both include and exclude")
	}
}

func TestMatchesAPIEntry_EmptyValueAgainstInclude(t *testing.T) {
	f := &Rules{IncludeCountries: []string{"DE"}}
	if MatchesAPIEntry(f, api.RansomwareEntry{Country: ""}) {
		t.Error("empty country should not match include filter")
	}
}

func TestKeywordMatcherInvalidRegex(t *testing.T) {
	// Invalid regex should not crash, just return false
	matcher := newKeywordMatcher("text", keywordmode.Regex)
	if matcher.matches("[invalid") {
		t.Error("invalid regex should return false")
	}
}

func TestKeywordMatcherUsesExplicitMode(t *testing.T) {
	regexMatcher := newKeywordMatcher("ACME Corp", keywordmode.Regex)
	if !regexMatcher.matches("ACME") {
		t.Fatal("regex matcher should evaluate the original-case text")
	}
	if regexMatcher.matches("acme") {
		t.Fatal("regex matcher should remain case-sensitive unless the pattern opts in")
	}

	literalMatcher := newKeywordMatcher("ACME Corp", keywordmode.Literal)
	if !literalMatcher.matches("acme") {
		t.Fatal("literal matcher should match case-insensitively")
	}
}

func TestMatchesAPIEntry_GenericFieldRegistry(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "City Hospital",
		ClaimURL:   "https://example.onion/post/123",
		WebsiteURL: "https://hospital.example",
	}

	if !MatchesAPIEntry(&Rules{
		IncludeFields: map[string][]string{
			"victim":    {"hospital"},
			"claim_url": {"post/123"},
		},
	}, entry) {
		t.Fatal("expected generic API field filters to match victim and claim_url")
	}
	if MatchesAPIEntry(&Rules{
		IncludeFields: map[string][]string{"website": {"unrelated.example"}},
	}, entry) {
		t.Fatal("expected non-matching generic API include field to reject entry")
	}
	if MatchesAPIEntry(&Rules{
		ExcludeFields: map[string][]string{"victim": {"hospital"}},
	}, entry) {
		t.Fatal("expected generic API exclude field to reject entry")
	}
}

func TestMatchesRSSEntry_GenericFieldRegistry(t *testing.T) {
	entry := rss.Entry{
		Title:       "New ransomware report",
		Link:        "https://example.test/report",
		Description: "Operational update",
		Author:      "CERT Team",
		Categories:  []string{"security", "malware"},
		FeedTitle:   "Threat Feed",
		FeedURL:     "https://feeds.example.test/rss.xml",
	}

	if !MatchesRSSEntry(&Rules{
		IncludeFields: map[string][]string{
			"feed_title": {"threat"},
			"category":   {"malware"},
		},
	}, entry) {
		t.Fatal("expected generic RSS field filters to match feed_title and category")
	}
	if MatchesRSSEntry(&Rules{
		ExcludeFields: map[string][]string{"feed_url": {"feeds.example.test"}},
	}, entry) {
		t.Fatal("expected generic RSS exclude field to reject entry")
	}
}

func TestEqualFoldTrimmedBlankNeverMatches(t *testing.T) {
	cases := []struct{ a, b string }{
		{" ", ""}, {"", " "}, {"", ""}, {"   ", "\t"},
	}
	for _, c := range cases {
		if equalFoldTrimmed(c.a, c.b) {
			t.Fatalf("equalFoldTrimmed(%q, %q) = true, want false", c.a, c.b)
		}
	}
}

// Settled behaviour (was the cross-field seam defect): Title and
// Description are joined with searchTextFieldSeparator, not a space, so a
// keyword can no longer match purely by spanning the boundary between the
// two fields. Do not "fix" this test by changing the assertion back --
// the earlier match was the bug, and RSS include keyword lists depending on
// it will stop matching this input; that is the intended, operator-approved
// change.
func TestMatchesRSSEntryKeywordDoesNotSpanFieldBoundary(t *testing.T) {
	rules := &Rules{IncludeKeywords: []string{"acme leak"}}
	entry := rss.Entry{Title: "acme", Description: "leak of internal data"}
	if MatchesRSSEntry(rules, entry) {
		t.Fatal("keyword \"acme leak\" must not match Title=\"acme\" + Description=\"leak...\" -- " +
			"neither field alone contains the phrase, so it should not match across the join")
	}
}

// The exclude-list mirror of the include-list case above: a keyword that
// used to accidentally suppress an RSS item by spanning the field boundary
// must no longer do so.
func TestMatchesRSSEntryExcludeKeywordDoesNotSpanFieldBoundary(t *testing.T) {
	rules := &Rules{ExcludeKeywords: []string{"acme leak"}}
	entry := rss.Entry{Title: "acme", Description: "leak of internal data"}
	if !MatchesRSSEntry(rules, entry) {
		t.Fatal("exclude keyword \"acme leak\" must not suppress Title=\"acme\" + " +
			"Description=\"leak...\" -- neither field alone contains the phrase")
	}
}

// A genuine multi-word phrase contained entirely within one field must
// still match: the fix changes cross-field adjacency, not in-field
// substring matching.
func TestMatchesRSSEntryKeywordStillMatchesPhraseWithinSingleField(t *testing.T) {
	rules := &Rules{IncludeKeywords: []string{"acme leak"}}
	entry := rss.Entry{Title: "breaking", Description: "the acme leak is confirmed"}
	if !MatchesRSSEntry(rules, entry) {
		t.Fatal("keyword \"acme leak\" should still match when the whole phrase is inside Description")
	}
}

// Regex mode mirror of the seam fix: a literal space inside a regex pattern
// used to match the join separator; it must not match the searchTextFieldSeparator
// byte that replaced it.
func TestMatchesRSSEntryRegexKeywordDoesNotSpanFieldBoundary(t *testing.T) {
	rules := &Rules{
		IncludeKeywords:  []string{"acme leak"},
		KeywordMatchMode: keywordmode.Regex,
	}
	entry := rss.Entry{Title: "acme", Description: "leak of internal data"}
	if MatchesRSSEntry(rules, entry) {
		t.Fatal("regex keyword \"acme leak\" must not match across the Title/Description boundary")
	}
}

// A field ending in whitespace immediately before the join, and the next
// field starting with whitespace, must not recreate a bridgeable run of
// space characters: with the old space join, Title="acme " + " " +
// " leak..." produced three consecutive spaces that a three-space keyword
// could bridge on; the fixed join inserts the non-space separator between
// the raw field values regardless of their own leading/trailing whitespace.
func TestMatchesRSSEntryKeywordDoesNotSpanFieldBoundaryWithWhitespacePaddedFields(t *testing.T) {
	rules := &Rules{IncludeKeywords: []string{"acme   leak"}} // three spaces
	entry := rss.Entry{Title: "acme ", Description: " leak of internal data"}
	if MatchesRSSEntry(rules, entry) {
		t.Fatal("a three-space keyword must not bridge Title's trailing space + the join + " +
			"Description's leading space")
	}
}
