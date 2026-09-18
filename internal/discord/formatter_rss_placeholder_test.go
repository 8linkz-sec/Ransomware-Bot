package discord

import (
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
)

// The RSS embed's PLACEHOLDER
// branches (setDiscordRSSDescription, setDiscordRSSAuthor,
// appendDiscordRSSPublished, appendDiscordRSSURLField) used to assign the
// operator-configured empty_field_text verbatim, unlike their sibling VALUE
// branch which always truncates to the matching Discord/embed limit. An
// oversized empty_field_text therefore exceeded Discord's own field/author/
// description caps and Discord rejected the whole webhook (400) -- nothing
// was delivered.

func TestSetDiscordRSSDescriptionPlaceholderIsTruncated(t *testing.T) {
	placeholder := strings.Repeat("x", discordRSSSummaryLimit+500)
	embed := &MessageEmbed{}

	setDiscordRSSDescription(embed, notifyfmt.RSSView{}, nil, true, placeholder)

	if got := len([]rune(embed.Description)); got > discordRSSSummaryLimit {
		t.Fatalf("description placeholder length = %d, want <= %d (the value branch's own limit)", got, discordRSSSummaryLimit)
	}
}

func TestSetDiscordRSSDescriptionPlaceholderAtLimitIsUnchanged(t *testing.T) {
	placeholder := strings.Repeat("x", discordRSSSummaryLimit)
	embed := &MessageEmbed{}

	setDiscordRSSDescription(embed, notifyfmt.RSSView{}, nil, true, placeholder)

	if embed.Description != placeholder {
		t.Fatalf("description exactly at the limit was altered: len=%d, want unchanged len=%d", len([]rune(embed.Description)), discordRSSSummaryLimit)
	}
}

func TestSetDiscordRSSAuthorPlaceholderIsTruncated(t *testing.T) {
	placeholder := strings.Repeat("y", discordEmbedAuthorNameLimit+500)
	embed := &MessageEmbed{}

	setDiscordRSSAuthor(embed, notifyfmt.RSSView{}, true, placeholder)

	if embed.Author == nil {
		t.Fatal("embed.Author = nil, want a placeholder author")
	}
	if got := len([]rune(embed.Author.Name)); got > discordEmbedAuthorNameLimit {
		t.Fatalf("author placeholder length = %d, want <= %d (Discord's own author.name limit)", got, discordEmbedAuthorNameLimit)
	}
}

func TestAppendDiscordRSSPublishedPlaceholderIsTruncated(t *testing.T) {
	placeholder := strings.Repeat("z", discordEmbedFieldValueLimit+500)
	embed := &MessageEmbed{}

	appendDiscordRSSPublished(embed, notifyfmt.RSSView{}, nil, true, placeholder)

	field := findDiscordFieldByNamePart(embed, "Published")
	if field == nil {
		t.Fatal("expected a Published field")
	}
	if got := len([]rune(field.Value)); got > discordEmbedFieldValueLimit {
		t.Fatalf("published placeholder field length = %d, want <= %d (Discord's own field.value limit)", got, discordEmbedFieldValueLimit)
	}
}

func TestAppendDiscordRSSURLFieldPlaceholderIsTruncated(t *testing.T) {
	placeholder := strings.Repeat("w", discordEmbedFieldValueLimit+500)

	// appendDiscordRSSURLField backs both the "Link" fallback field and the
	// "Feed URL" field -- two sub-cases, same function, same bug.
	for _, label := range []string{"Link", "Feed URL"} {
		t.Run(label, func(t *testing.T) {
			embed := &MessageEmbed{}

			appendDiscordRSSURLField(embed, label, "", "🔗", nil, true, placeholder)

			field := findDiscordFieldByNamePart(embed, label)
			if field == nil {
				t.Fatalf("expected a %s field", label)
			}
			if got := len([]rune(field.Value)); got > discordEmbedFieldValueLimit {
				t.Fatalf("%s placeholder field length = %d, want <= %d (Discord's own field.value limit)", label, got, discordEmbedFieldValueLimit)
			}
		})
	}
}

// appendDiscordRSSLink's own showEmpty fallback
// branch (reached when view.Link == "" and view.FeedURL does not resolve to a
// safe embed URL) calls appendDiscordRSSField with the raw placeholder
// directly, bypassing appendDiscordRSSURLField (and its truncation) entirely
// -- a fifth call site the fix above's four named branches did not cover.

func TestAppendDiscordRSSLinkFallbackPlaceholderIsTruncated(t *testing.T) {
	placeholder := strings.Repeat("v", discordEmbedFieldValueLimit+500)
	embed := &MessageEmbed{}

	appendDiscordRSSLink(embed, notifyfmt.RSSView{}, nil, true, placeholder)

	field := findDiscordFieldByNamePart(embed, "Link")
	if field == nil {
		t.Fatal("expected a Link field")
	}
	if got := len([]rune(field.Value)); got > discordEmbedFieldValueLimit {
		t.Fatalf("link fallback placeholder field length = %d, want <= %d (Discord's own field.value limit)", got, discordEmbedFieldValueLimit)
	}
}

func TestAppendDiscordRSSLinkFallbackPlaceholderShortValueUnchanged(t *testing.T) {
	embed := &MessageEmbed{}

	appendDiscordRSSLink(embed, notifyfmt.RSSView{}, nil, true, "N/A")

	field := findDiscordFieldByNamePart(embed, "Link")
	if field == nil {
		t.Fatal("expected a Link field")
	}
	if field.Value != "N/A" {
		t.Fatalf("link fallback field value = %q, want unchanged placeholder %q", field.Value, "N/A")
	}
}

func findDiscordFieldByNamePart(embed *MessageEmbed, namePart string) *MessageEmbedField {
	for _, field := range embed.Fields {
		if strings.Contains(field.Name, namePart) {
			return field
		}
	}
	return nil
}
