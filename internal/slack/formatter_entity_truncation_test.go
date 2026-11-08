package slack

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSlackTextTruncationDoesNotSplitEscapedEntities(t *testing.T) {
	tests := []struct {
		name   string
		render func(string, int) string
		value  string
		limit  int
		want   string
	}{
		{
			name:   "natural ampersand at prefix cut",
			render: slackNaturalText,
			value:  "ab&continued",
			limit:  9,
			want:   testBidiIsolateOpen + "ab..." + testBidiIsolateClose,
		},
		{
			name:   "natural less-than at prefix cut",
			render: slackNaturalText,
			value:  "ab<continued",
			limit:  9,
			want:   testBidiIsolateOpen + "ab..." + testBidiIsolateClose,
		},
		{
			name:   "natural tiny limit",
			render: slackNaturalText,
			value:  "x&tail",
			limit:  2,
			want:   "x",
		},
		{
			name:   "natural complete entity before prefix cut",
			render: slackNaturalText,
			value:  "&abcdefghij",
			limit:  11,
			want:   testBidiIsolateOpen + "&amp;a..." + testBidiIsolateClose,
		},
		{
			name:   "description ampersand at prefix cut",
			render: slackDescriptionText,
			value:  "ab&continued beyond the description budget",
			limit:  22,
			want:   testBidiIsolateOpen + "ab... [truncated]" + testBidiIsolateClose,
		},
		{
			name:   "description tiny limit",
			render: slackDescriptionText,
			value:  "x>tail",
			limit:  2,
			want:   "x",
		},
		{
			name:   "URL ampersand at head cut",
			render: slackURLText,
			value:  "&abcdefghij",
			limit:  10,
			want:   "\u2066...hij" + testBidiIsolateClose,
		},
		{
			name:   "URL ampersand at tail cut",
			render: slackURLText,
			value:  "abcdefghij&",
			limit:  10,
			want:   "\u2066ab..." + testBidiIsolateClose,
		},
		{
			name:   "URL less-than at tail cut keeps following text",
			render: slackURLText,
			value:  "abcdefghij<xy",
			limit:  10,
			want:   "\u2066ab...xy" + testBidiIsolateClose,
		},
		{
			name:   "URL greater-than at tail cut",
			render: slackURLText,
			value:  "abcdefghij>",
			limit:  10,
			want:   "\u2066ab..." + testBidiIsolateClose,
		},
		{
			name:   "URL tiny limit",
			render: slackURLText,
			value:  "x<tail",
			limit:  2,
			want:   "x",
		},
		{
			name:   "untruncated text remains identical",
			render: slackNaturalText,
			value:  "A & B < C > D",
			limit:  80,
			want:   testBidiIsolateOpen + "A &amp; B &lt; C &gt; D" + testBidiIsolateClose,
		},
		{
			name:   "truncated plain text remains identical",
			render: slackNaturalText,
			value:  "abcdefghij",
			limit:  8,
			want:   testBidiIsolateOpen + "abc..." + testBidiIsolateClose,
		},
		{
			name:   "truncated plain description remains identical",
			render: slackDescriptionText,
			value:  "abcdefghijklmnopqrstuvwxyz",
			limit:  22,
			want:   testBidiIsolateOpen + "abcde... [truncated]" + testBidiIsolateClose,
		},
		{
			name:   "truncated plain URL remains identical",
			render: slackURLText,
			value:  "abcdefghij",
			limit:  8,
			want:   "\u2066a...ij" + testBidiIsolateClose,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.render(tt.value, tt.limit); got != tt.want {
				t.Fatalf("render(%q, %d) = %q, want %q", tt.value, tt.limit, got, tt.want)
			}
		})
	}
}

func TestSlackEntityTruncationEveryBoundaryKeepsCompleteEntitiesAndLimit(t *testing.T) {
	for _, render := range []struct {
		name string
		fn   func(string, int) string
	}{
		{"natural", slackNaturalText},
		{"description", slackDescriptionText},
		{"URL", slackURLText},
	} {
		for _, value := range []string{"&", "<", ">", "a&b<c>d", "&<>a&<>z", "long text before & and after <tag>"} {
			for limit := 0; limit <= 60; limit++ {
				got := render.fn(value, limit)
				assertSlackEntityWithinBudget(t, render.name, value, limit, got)
			}
		}
	}
}

func assertSlackEntityWithinBudget(t *testing.T, name, value string, limit int, got string) {
	t.Helper()
	if utf8.RuneCountInString(got) > limit {
		t.Fatalf("%s(%q, %d) exceeded limit: %q", name, value, limit, got)
	}
	assertSlackCompleteEntities(t, name, value, limit, got)
}

func assertSlackCompleteEntities(t *testing.T, name, value string, limit int, got string) {
	t.Helper()
	for remaining := got; remaining != ""; {
		index := strings.IndexByte(remaining, '&')
		if index < 0 {
			break
		}
		remaining = remaining[index:]
		matched := false
		for _, entity := range []string{"&amp;", "&lt;", "&gt;"} {
			if strings.HasPrefix(remaining, entity) {
				remaining = remaining[len(entity):]
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("%s(%q, %d) left partial Slack entity: %q", name, value, limit, got)
		}
	}
}
