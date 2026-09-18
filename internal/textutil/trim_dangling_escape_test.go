package textutil

import "testing"

func TestTrimDanglingEscape(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "empty", text: "", want: ""},
		{name: "no backslash", text: "plain text", want: "plain text"},
		{name: "even trailing run", text: `abc\\`, want: `abc\\`},
		{name: "odd trailing run", text: `abc\`, want: "abc"},
		{name: "odd trailing run of three", text: `abc\\\`, want: `abc\\`},
		{name: "only backslashes odd", text: `\`, want: ""},
		{name: "only backslashes even", text: `\\`, want: `\\`},
		{name: "odd run before text marker", text: `ab\...`, want: "ab..."},
		{name: "even run before text marker", text: `ab\\...`, want: `ab\\...`},
		{name: "odd run before description marker", text: `ab\... [truncated]`, want: "ab... [truncated]"},
		{name: "even run before description marker", text: `ab\\... [truncated]`, want: `ab\\... [truncated]`},
		{name: "odd run before infix marker", text: `abc\...zzz`, want: "abc...zzz"},
		{name: "even run before infix marker", text: `abc\\...zzz`, want: `abc\\...zzz`},
		{name: "orphan close-bracket in a middle-truncated tail is trimmed", text: `xxx...]zzz`, want: `xxx...zzz`},
		{name: "orphan open-bracket in a middle-truncated tail is trimmed", text: `xxx...[zzz`, want: `xxx...zzz`},
		{name: "intact escaped bracket pair at the tail boundary is left alone", text: `xxx...\]zzz`, want: `xxx...\]zzz`},
		{name: "orphan bracket immediately followed by more escaped content", text: `xxx...]\[abc\]`, want: `xxx...\[abc\]`},
		{name: "middle-truncated tail ending in an even run", text: `xxx...zz\\`, want: `xxx...zz\\`},
		// The orphan-bracket check has to run after EVERY marker occurrence,
		// not only after the last one: TruncateMiddle inserts its marker in the
		// middle, and the feed-supplied tail can carry a literal "..." of its
		// own after the cut, which would make the inserted marker a non-final
		// occurrence. An implementation that only looks past the last marker
		// leaves the real orphan in place here.
		{name: "orphan bracket after a non-final marker is trimmed", text: `xxx...]yyy...zzz`, want: `xxx...yyy...zzz`},
		// Exactly ONE bracket is ever orphaned per cut (only the escape pair
		// straddling the boundary loses its backslash), so the trim must drop
		// one character, never loop. A second bracket immediately after it was
		// never orphaned and is real content.
		{name: "only the first bracket after a marker is trimmed", text: `xxx...[]zzz`, want: `xxx...]zzz`},
		// The guard admits "[" and "]" only. escapeDiscordMarkdownText escapes
		// backslash, "[" and "]" and nothing else, so any other first character
		// after a marker -- "(" included, despite its role in the masked-link
		// form "](" -- is unescaped feed content that must survive verbatim.
		{name: "non-bracket first character after a marker is left alone", text: `xxx...(zzz`, want: `xxx...(zzz`},

		// The feed controls the text, so it can put a literal "..." in front of
		// the cut. An implementation that looks at only ONE marker position
		// trims the wrong place and leaves the real dangling backslash behind.
		{name: "feed marker before an unmarked cut", text: `a...b\`, want: `a...b`},
		{name: "feed marker before the inserted infix marker", text: `x...y\...zzz`, want: `x...y...zzz`},
		{name: "feed marker at the end after the inserted infix marker", text: `ab\...cd...`, want: `ab...cd...`},
		{name: "feed marker before the description marker", text: `a...b\... [truncated]`, want: `a...b... [truncated]`},
		{name: "even run before a feed marker stays", text: `a\\...b`, want: `a\\...b`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TrimDanglingEscape(tt.text); got != tt.want {
				t.Fatalf("TrimDanglingEscape(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}

	// TruncateMiddle puts its marker in the MIDDLE, so a suffix-only
	// implementation is a guaranteed no-op on it while the dangling backslash
	// before the marker survives. The payload carries a feed-supplied "..." of
	// its own, so an implementation that keys off the FIRST or the LAST marker
	// occurrence trims the wrong position and fails here. The marker offset is
	// computed from TruncateMiddle's own arithmetic instead of being searched
	// for, so the assertion cannot be fooled by the payload's markers.
	t.Run("truncate middle results", func(t *testing.T) {
		escaped := ""
		for _, r := range "xxxx...xxxxxxxxxxxxx[y]zzzz...zzzzzzzzzzzzzz" {
			switch r {
			case '[':
				escaped += `\[`
			case ']':
				escaped += `\]`
			default:
				escaped += string(r)
			}
		}
		runeCount := len([]rune(escaped))
		for budget := 4; budget <= runeCount; budget++ {
			got := TrimDanglingEscape(TruncateMiddle(escaped, budget))
			head := got
			if budget < runeCount {
				// TruncateMiddle emits PrefixRunes(text, (budget-3)/2) + "..." + suffix.
				// After the trim the head is at most that long, so take everything
				// before the marker at that offset.
				headBudget := (budget - 3) / 2
				runes := []rune(got)
				if headBudget > len(runes) {
					headBudget = len(runes)
				}
				head = string(runes[:headBudget])
			}
			if endsOnLiveEscapeForTest(head) {
				t.Fatalf("TrimDanglingEscape(TruncateMiddle(_, %d)) = %q keeps a dangling backslash before the marker",
					budget, got)
			}
			if endsOnLiveEscapeForTest(got) {
				t.Fatalf("TrimDanglingEscape(TruncateMiddle(_, %d)) = %q ends on a dangling backslash", budget, got)
			}
		}
	})
}

// TestTrimDanglingEscapeDoesNotTouchDescriptionMarkerBracket pins the load-bearing
// detail descriptionTruncationMarker's own doc comment names: the marker's "["
// is shielded from the new orphan-bracket check by the SPACE right after the
// three dots, not by any special-casing in TrimDanglingEscape itself -- the
// check only ever fires on a bracket that is the very first character
// immediately after "...", with nothing in between. If a future edit ever
// closed that gap (e.g. changed the marker to "...[truncated]"), this test
// would start failing where nothing today catches it.
func TestTrimDanglingEscapeDoesNotTouchDescriptionMarkerBracket(t *testing.T) {
	text := "abc" + descriptionTruncationMarker
	if got := TrimDanglingEscape(text); got != text {
		t.Fatalf("TrimDanglingEscape(%q) = %q, want unchanged %q -- descriptionTruncationMarker's own bracket must survive",
			text, got, text)
	}
}

// endsOnLiveEscapeForTest scans with the "a backslash consumes the next
// character" rule and reports whether the scan ends on a backslash that has
// nothing left to consume.
func endsOnLiveEscapeForTest(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' {
			continue
		}
		if i+1 >= len(text) {
			return true
		}
		i++
	}
	return false
}
