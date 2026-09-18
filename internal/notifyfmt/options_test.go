package notifyfmt

import (
	"reflect"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
)

func TestDefaultFormatOptions(t *testing.T) {
	opts := DefaultFormatOptions()

	if !opts.ShowUnicodeFlags {
		t.Fatal("ShowUnicodeFlags = false, want true")
	}
	if opts.EmptyFieldText != "N/A" {
		t.Fatalf("EmptyFieldText = %q, want N/A", opts.EmptyFieldText)
	}
	if opts.DisplayLocale != "en" {
		t.Fatalf("DisplayLocale = %q, want en", opts.DisplayLocale)
	}
	if opts.TimestampFormat != "2006-01-02 15:04:05 MST" {
		t.Fatalf("TimestampFormat = %q", opts.TimestampFormat)
	}
	if opts.DisplayTimezone != "UTC" {
		t.Fatalf("DisplayTimezone = %q, want UTC", opts.DisplayTimezone)
	}
	if !reflect.DeepEqual(opts.FieldOrder, formatfields.DefaultDiscordFieldOrder()) {
		t.Fatalf("FieldOrder = %#v, want default Discord field order", opts.FieldOrder)
	}

	if !opts.Discord.ShowIcons {
		t.Fatal("Discord.ShowIcons = false, want true")
	}
	if opts.Discord.RansomwareColor != "#ff0000" ||
		opts.Discord.RSSColor != "#0099ff" ||
		opts.Discord.GovernmentColor != "#ffa500" {
		t.Fatalf("Discord colors = %q/%q/%q", opts.Discord.RansomwareColor, opts.Discord.RSSColor, opts.Discord.GovernmentColor)
	}
	if opts.Discord.DescriptionMaxChars != DefaultDescriptionMaxChars {
		t.Fatalf("Discord.DescriptionMaxChars = %d, want %d", opts.Discord.DescriptionMaxChars, DefaultDescriptionMaxChars)
	}

	if opts.Slack.TitleText != DefaultSlackTitleText {
		t.Fatalf("Slack.TitleText = %q, want %q", opts.Slack.TitleText, DefaultSlackTitleText)
	}
	if opts.Slack.RSSText != DefaultSlackRSSText {
		t.Fatalf("Slack.RSSText = %q, want %q", opts.Slack.RSSText, DefaultSlackRSSText)
	}
	if !reflect.DeepEqual(opts.Slack.FieldOrder, formatfields.DefaultSlackFieldOrder()) {
		t.Fatalf("Slack.FieldOrder = %#v, want default Slack field order", opts.Slack.FieldOrder)
	}
	if opts.Slack.DescriptionMaxChars != DefaultDescriptionMaxChars {
		t.Fatalf("Slack.DescriptionMaxChars = %d, want %d", opts.Slack.DescriptionMaxChars, DefaultDescriptionMaxChars)
	}

	if opts.RSS.TitleText != DefaultRSSTitleText {
		t.Fatalf("RSS.TitleText = %q, want %q", opts.RSS.TitleText, DefaultRSSTitleText)
	}
	if !reflect.DeepEqual(opts.RSS.FieldOrder, formatfields.DefaultRSSFieldOrder()) {
		t.Fatalf("RSS.FieldOrder = %#v, want default RSS field order", opts.RSS.FieldOrder)
	}
}

func TestFormatLabel(t *testing.T) {
	tests := []struct {
		name      string
		keys      []string
		fallback  string
		labelMaps []map[string]string
		want      string
	}{
		{
			name:      "no keys returns fallback",
			keys:      nil,
			fallback:  "Group",
			labelMaps: []map[string]string{{"group": "Gruppe"}},
			want:      "Group",
		},
		{
			name:      "blank keys return fallback",
			keys:      []string{"   ", "___"},
			fallback:  "Group",
			labelMaps: []map[string]string{{"group": "Gruppe"}},
			want:      "Group",
		},
		{
			name:      "exact match",
			keys:      []string{"group"},
			fallback:  "Group",
			labelMaps: []map[string]string{{"group": "Gruppe"}},
			want:      "Gruppe",
		},
		{
			name:      "normalized config key matches lookup key",
			keys:      []string{"feed_title"},
			fallback:  "Source",
			labelMaps: []map[string]string{{"Feed Title": "Quelle"}},
			want:      "Quelle",
		},
		{
			name:      "normalized lookup key matches config key",
			keys:      []string{"Feed-Title"},
			fallback:  "Source",
			labelMaps: []map[string]string{{"feed_title": "Quelle"}},
			want:      "Quelle",
		},
		{
			name:      "blank label value is ignored",
			keys:      []string{"group"},
			fallback:  "Group",
			labelMaps: []map[string]string{{"group": "   "}},
			want:      "Group",
		},
		{
			name:      "first matching map wins",
			keys:      []string{"group"},
			fallback:  "Group",
			labelMaps: []map[string]string{{"group": "First"}, {"group": "Second"}},
			want:      "First",
		},
		{
			name:      "nil and empty maps are skipped",
			keys:      []string{"group"},
			fallback:  "Group",
			labelMaps: []map[string]string{nil, {}, {"group": "Third"}},
			want:      "Third",
		},
		{
			name:      "no match returns fallback",
			keys:      []string{"group"},
			fallback:  "Group",
			labelMaps: []map[string]string{{"victim": "Opfer"}},
			want:      "Group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatLabel(tt.keys, tt.fallback, tt.labelMaps...); got != tt.want {
				t.Fatalf("FormatLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeLabelKey(t *testing.T) {
	tests := map[string]string{
		"Feed Title":    "feed_title",
		" feed-title ":  "feed_title",
		"feed.title":    "feed_title",
		"Feed -- Title": "feed_title",
		"__group__":     "group",
		"   ":           "",
	}

	for input, want := range tests {
		if got := normalizeLabelKey(input); got != want {
			t.Fatalf("normalizeLabelKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLabelKeyCandidates(t *testing.T) {
	if got := labelKeyCandidates("group"); !reflect.DeepEqual(got, []string{"group"}) {
		t.Fatalf("labelKeyCandidates(group) = %#v, want [group]", got)
	}
	if got := labelKeyCandidates(" Feed Title "); !reflect.DeepEqual(got, []string{"Feed Title", "feed_title"}) {
		t.Fatalf("labelKeyCandidates( Feed Title ) = %#v, want trimmed and normalized candidates", got)
	}
	if got := labelKeyCandidates("   "); got != nil {
		t.Fatalf("labelKeyCandidates(blank) = %#v, want nil", got)
	}
	if got := labelKeyCandidates("__"); got != nil {
		t.Fatalf("labelKeyCandidates(__) = %#v, want nil for keys that normalize to empty", got)
	}
}
