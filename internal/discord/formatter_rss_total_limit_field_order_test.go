package discord

import (
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
)

// discordTrimEmbedToTotalLimit: appendDiscordRSSField's per-field
// discordEmbedCanAddField check (L556) is
// NOT the only guard of Discord's 6000-rune embed total -- Title, Description
// and Footer.Text are all set directly (bypassing the per-field check
// entirely), so the final discordTrimEmbedToTotalLimit sweep at the end of
// formatRSSEmbed (L322) is load-bearing whenever rss.field_order puts the
// field sinks (link/categories/published/feed_url) BEFORE title/description/
// author/feed_title: the per-field check then sees an almost-empty embed and
// admits every field, and only the closing sweep brings the total back under
// 6000. validateRSSFieldOrder accepts this ordering. Every pre-existing
// total-limit test uses the DEFAULT field order, where description/footer/
// author are already set before the fields are appended, so the per-field
// check alone happens to suffice there and this sweep is never exercised.
func TestFormatRSSEmbedTotalLimitFieldsFirstOrderStaysUnderCap(t *testing.T) {
	placeholder := strings.Repeat("x", 2048)
	formatCfg := &notifyfmt.FormatOptions{
		ShowEmptyFields: true,
		EmptyFieldText:  placeholder,
		RSS: notifyfmt.RSSFormatOptions{
			ShowAuthor: true,
			FieldOrder: []string{
				formatfields.RSSFieldLink,
				formatfields.RSSFieldCategories,
				formatfields.RSSFieldPublished,
				formatfields.RSSFieldFeedURL,
				formatfields.RSSFieldTitle,
				formatfields.RSSFieldDescription,
				formatfields.RSSFieldAuthor,
				formatfields.RSSFieldFeedTitle,
			},
		},
	}
	entry := model.RSSEntry{} // every field empty -> every sink renders the oversized placeholder

	embed := formatRSSEmbed(entry, "general", formatCfg)

	if got := discordEmbedTextLength(embed); got > discordEmbedTotalTextLimit {
		t.Fatalf("embed total text length = %d, want <= %d (fields-first rss.field_order with an oversized empty_field_text)", got, discordEmbedTotalTextLimit)
	}
}
