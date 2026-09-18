package discord

import (
	"strings"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
)

func formatTimestamp(timestamp string) string {
	return textutil.FormatTimestamp(timestamp)
}

const (
	// Default colors for Discord embeds.
	defaultRansomwareColor = 0xff0000
	defaultRSSColor        = 0x0099ff
	defaultGovernmentColor = 0xffa500

	discordEmbedTitleLimit       = 256
	discordEmbedDescriptionLimit = 2048
	discordRSSSummaryLimit       = 600
	discordEmbedFieldLimit       = 25
	discordEmbedFieldValueLimit  = 1024
	discordEmbedTotalTextLimit   = 6000
	discordEmbedFooterTextLimit  = 2048
	discordEmbedAuthorNameLimit  = 256
	discordRansomwareSourceLabel = "Ransomware.live API"
	discordRansomwareAlertLabel  = "Ransomware Alert"
)

// formatRansomwareEmbed creates a Discord embed for a ransomware entry.
func formatRansomwareEmbed(entry model.RansomwareEntry, formatConfig *notifyfmt.FormatOptions) *MessageEmbed {
	formatConfig = discordFormatConfigOrDefault(formatConfig)
	titlePrefix := discordLabel("🚨", "Ransomware Alert:", discordShowIcons(formatConfig)) + " "
	alertLabel := discordRansomwareLabel(formatConfig, "ransomware_alert", discordRansomwareAlertLabel)
	titlePrefix = strings.Replace(titlePrefix, "Ransomware Alert:", alertLabel+":", 1)
	titleBudget := discordEmbedTitleLimit - len([]rune(titlePrefix))
	titleGroup := strings.TrimSpace(entry.Group)
	if titleGroup == "" {
		titleGroup = "Unknown group"
	}
	embed := &MessageEmbed{
		Title: titlePrefix + discordNaturalText(titleGroup, titleBudget),
		Color: discordColorPointer(discordConfiguredColor(formatConfig, "ransomware")),
		Footer: &MessageEmbedFooter{
			Text: discordRansomwareLabel(formatConfig, "ransomware_source", discordRansomwareSourceLabel),
		},
	}
	if !entry.Discovered.IsZero() {
		embed.Timestamp = entry.Discovered.Format(time.RFC3339)
	}

	// Titles are never links (operator decision); the website/darknet URL
	// must still be visible as its own element. It is
	// absent from formatfields.DefaultDiscordFieldOrder, so a default
	// configuration would otherwise show the URL nowhere at all once
	// embed.URL stops carrying it. Insert it before the configured loop
	// (so it gets budget priority under discordEmbedTotalTextLimit) only
	// when there is a URL to show and when "website"/"url" is not already
	// going to be rendered by the operator's own FieldOrder, so neither a
	// duplicate field nor a placeholder row nobody asked for can appear.
	if strings.TrimSpace(entry.WebsiteURL) != "" &&
		!discordRansomwareFieldOrderHasWebsite(formatConfig.FieldOrder) {
		if field := createRansomwareField(formatfields.FieldWebsite, entry, formatConfig); field != nil {
			if len(embed.Fields) < discordEmbedFieldLimit && discordEmbedCanAddField(embed, field) {
				embed.Fields = append(embed.Fields, field)
			}
		}
	}

	// Add fields based on the format configuration
	for _, fieldName := range formatConfig.FieldOrder {
		canonicalFieldName, ok := formatfields.Normalize(fieldName)
		if !ok {
			continue
		}
		// Create the field for the current entry
		field := createRansomwareField(canonicalFieldName, entry, formatConfig)
		if field != nil {
			if len(embed.Fields) >= discordEmbedFieldLimit {
				break
			}
			if !discordEmbedCanAddField(embed, field) {
				break
			}
			embed.Fields = append(embed.Fields, field)
		}
	}

	return embed
}

// discordRansomwareFieldOrderHasWebsite reports whether the operator's
// FieldOrder already renders the website/url field, so
// formatRansomwareEmbed's unconditional fallback (see above) does not add a
// second one.
func discordRansomwareFieldOrderHasWebsite(fieldOrder []string) bool {
	for _, name := range fieldOrder {
		if canonical, ok := formatfields.Normalize(name); ok && canonical == formatfields.FieldWebsite {
			return true
		}
	}
	return false
}

func discordEmbedTextLength(embed *MessageEmbed) int {
	if embed == nil {
		return 0
	}
	total := len([]rune(embed.Title)) + len([]rune(embed.Description))
	if embed.Footer != nil {
		total += len([]rune(embed.Footer.Text))
	}
	if embed.Author != nil {
		total += len([]rune(embed.Author.Name))
	}
	for _, field := range embed.Fields {
		total += len([]rune(field.Name)) + len([]rune(field.Value))
	}
	return total
}

func discordEmbedCanAddField(embed *MessageEmbed, field *MessageEmbedField) bool {
	if field == nil {
		return true
	}
	return discordEmbedTextLength(embed)+len([]rune(field.Name))+len([]rune(field.Value)) <= discordEmbedTotalTextLimit
}

func discordTrimEmbedToTotalLimit(embed *MessageEmbed) {
	for discordEmbedTextLength(embed) > discordEmbedTotalTextLimit && len(embed.Fields) > 0 {
		embed.Fields = embed.Fields[:len(embed.Fields)-1]
	}
}

func discordRansomwareLabel(formatConfig *notifyfmt.FormatOptions, key, fallback string) string {
	if formatConfig == nil {
		return fallback
	}
	return notifyfmt.FormatLabel(
		[]string{key},
		fallback,
		formatConfig.Discord.FieldLabels,
		formatConfig.FieldLabels,
	)
}

func discordRSSLabel(formatConfig *notifyfmt.FormatOptions, key, fallback string) string {
	if formatConfig == nil {
		return fallback
	}
	return notifyfmt.FormatLabel(
		discordRSSLabelKeys(key),
		fallback,
		formatConfig.Discord.FieldLabels,
		formatConfig.RSS.FieldLabels,
		formatConfig.FieldLabels,
	)
}

func discordRSSLabelKeys(key string) []string {
	switch key {
	case formatfields.RSSFieldFeedTitle:
		return []string{formatfields.RSSFieldFeedTitle, "source"}
	default:
		return []string{key}
	}
}

func discordFormatConfigOrDefault(formatConfig *notifyfmt.FormatOptions) *notifyfmt.FormatOptions {
	if formatConfig != nil {
		return formatConfig
	}
	defaultCfg := notifyfmt.DefaultFormatOptions()
	return &defaultCfg
}

type discordRansomwareValueMode int

const (
	discordNaturalValue discordRansomwareValueMode = iota
	discordURLValue
)

type discordRansomwareFieldSpec struct {
	icon     string
	inline   bool
	mode     discordRansomwareValueMode
	maxChars func(*notifyfmt.FormatOptions) int
}

var discordRansomwareFieldSpecs = map[string]discordRansomwareFieldSpec{
	formatfields.FieldID: {
		icon:   "🆔",
		inline: true,
	},
	formatfields.FieldCountry: {
		icon:   "🌍",
		inline: true,
	},
	formatfields.FieldVictim: {
		icon:   "🎯",
		inline: true,
	},
	formatfields.FieldGroup: {
		icon:   "💀",
		inline: true,
	},
	formatfields.FieldActivity: {
		icon:   "📊",
		inline: true,
	},
	formatfields.FieldAttackDate: {
		icon:   "⚔️",
		inline: true,
	},
	formatfields.FieldDiscovered: {
		icon:   "🔍",
		inline: true,
	},
	formatfields.FieldPublished: {
		icon:   "📣",
		inline: true,
	},
	formatfields.FieldPostURL: {
		icon: "🔗",
		mode: discordURLValue,
	},
	formatfields.FieldWebsite: {
		icon: "🌐",
		mode: discordURLValue,
	},
	formatfields.FieldDescription: {
		icon:     "📝",
		maxChars: discordDescriptionMaxChars,
	},
	formatfields.FieldScreenshot: {
		icon: "📸",
		mode: discordURLValue,
	},
}

// createRansomwareField creates a Discord embed field for a specific ransomware entry field.
// When ShowEmptyFields is enabled, missing values are rendered with EmptyFieldText as placeholder.
func createRansomwareField(
	fieldName string,
	entry model.RansomwareEntry,
	formatConfig *notifyfmt.FormatOptions,
) *MessageEmbedField {
	formatConfig = discordFormatConfigOrDefault(formatConfig)
	canonicalFieldName, ok := formatfields.Normalize(fieldName)
	if !ok {
		return nil
	}
	spec, ok := discordRansomwareFieldSpecs[canonicalFieldName]
	if !ok {
		return nil
	}

	placeholder := formatConfig.EmptyFieldText
	if placeholder == "" {
		placeholder = "N/A"
	}
	sharedField, ok := formatfields.RansomwareFieldValueForDisplay(
		canonicalFieldName,
		entry,
		formatConfig.ShowUnicodeFlags,
		formatConfig.DisplayLocale,
		formatConfig.TimestampFormat,
		formatConfig.DisplayTimezone,
	)
	if !ok {
		return nil
	}
	value := strings.TrimSpace(sharedField.Value)
	if value == "" {
		if !formatConfig.ShowEmptyFields {
			return nil
		}
		value = placeholder
	}

	maxChars := discordEmbedFieldValueLimit
	if spec.maxChars != nil {
		maxChars = spec.maxChars(formatConfig)
	}
	renderedValue := discordRichOrPlaceholder(value, placeholder, maxChars)
	if spec.mode == discordURLValue {
		renderedValue = discordURLOrPlaceholder(value, placeholder, maxChars)
	}
	if canonicalFieldName == formatfields.FieldDescription && value != placeholder {
		renderedValue = discordDescriptionText(value, maxChars)
	}

	return &MessageEmbedField{
		Name: discordLabel(
			spec.icon,
			discordRansomwareLabel(formatConfig, sharedField.Canonical, sharedField.Label),
			discordShowIcons(formatConfig),
		),
		Value:  renderedValue,
		Inline: spec.inline,
	}
}

// formatRSSEmbed creates a Discord embed for an RSS entry.
func formatRSSEmbed(entry model.RSSEntry, feedType string, formatConfig *notifyfmt.FormatOptions) *MessageEmbed {
	showEmpty := formatConfig != nil && formatConfig.ShowEmptyFields
	placeholder := "N/A"
	if formatConfig != nil && formatConfig.EmptyFieldText != "" {
		placeholder = formatConfig.EmptyFieldText
	}
	view := notifyfmt.BuildRSSView(entry, notifyfmt.RSSViewOptions{
		TitleFallback:    discordRSSTitleFallback(formatConfig),
		TimestampFormat:  discordTimestampFormat(formatConfig),
		DisplayTimezone:  discordDisplayTimezone(formatConfig),
		CategoryLimit:    5,
		CategoryMaxChars: discordEmbedFieldValueLimit - 2,
	})

	embed := &MessageEmbed{
		Color: discordColorPointer(rssEmbedColor(feedType, formatConfig)),
	}
	for _, fieldName := range discordRSSFieldOrder(formatConfig) {
		switch fieldName {
		case formatfields.RSSFieldTitle:
			embed.Title = discordNaturalText(view.Title, discordEmbedTitleLimit)
		case formatfields.RSSFieldDescription:
			setDiscordRSSDescription(embed, view, formatConfig, showEmpty, placeholder)
		case formatfields.RSSFieldLink:
			appendDiscordRSSLink(embed, view, formatConfig, showEmpty, placeholder)
		case formatfields.RSSFieldAuthor:
			if discordRSSShowAuthor(formatConfig) {
				setDiscordRSSAuthor(embed, view, showEmpty, placeholder)
			}
		case formatfields.RSSFieldCategories:
			appendDiscordRSSCategories(embed, view, formatConfig, showEmpty, placeholder)
		case formatfields.RSSFieldPublished:
			appendDiscordRSSPublished(embed, view, formatConfig, showEmpty, placeholder)
		case formatfields.RSSFieldFeedTitle:
			setDiscordRSSFooter(embed, view, showEmpty, placeholder)
		case formatfields.RSSFieldFeedURL:
			appendDiscordRSSURLField(embed, "Feed URL", view.FeedURL, "📡", formatConfig, showEmpty, placeholder)
		}
	}

	discordTrimEmbedToTotalLimit(embed)
	return embed
}

func PreviewRansomwareEntry(entry model.RansomwareEntry, formatConfig *notifyfmt.FormatOptions) map[string]any {
	return discordEmbedPreview("ransomware", formatRansomwareEmbed(entry, formatConfig))
}

func PreviewRSSEntry(entry model.RSSEntry, feedType string, formatConfig *notifyfmt.FormatOptions) map[string]any {
	return discordEmbedPreview("rss", formatRSSEmbed(entry, feedType, formatConfig))
}

func discordEmbedPreview(kind string, embed *MessageEmbed) map[string]any {
	fields := make([]map[string]any, 0, len(embed.Fields))
	for _, field := range embed.Fields {
		fields = append(fields, map[string]any{
			"name":   field.Name,
			"value":  field.Value,
			"inline": field.Inline,
		})
	}

	preview := map[string]any{
		"platform": "discord",
		"kind":     kind,
		"title":    embed.Title,
		"fields":   fields,
	}
	if embed.Description != "" {
		preview["description"] = embed.Description
	}
	// Titles are never links: formatRansomwareEmbed and formatRSSEmbed
	// never set embed.URL any more, so this branch is
	// permanently unreachable through either production entry point. The
	// "url" preview key is gone for good; the same value is always in
	// "fields" now (see TestPreviewRansomwareEntryExposesEmbedMetadata /
	// TestPreviewRSSEntryExposesDescriptionAndAuthor).
	if embed.Timestamp != "" {
		preview["timestamp"] = embed.Timestamp
	}
	if embed.Footer != nil && embed.Footer.Text != "" {
		preview["footer"] = embed.Footer.Text
	}
	if embed.Author != nil && embed.Author.Name != "" {
		preview["author"] = embed.Author.Name
	}
	return preview
}

func discordRSSFieldOrder(formatConfig *notifyfmt.FormatOptions) []string {
	fields := formatfields.DefaultRSSFieldOrder()
	if formatConfig != nil && len(formatConfig.RSS.FieldOrder) > 0 {
		fields = formatConfig.RSS.FieldOrder
	}
	normalized := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		canonical, ok := formatfields.NormalizeRSS(field)
		if !ok {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	if len(normalized) == 0 {
		return formatfields.DefaultRSSFieldOrder()
	}
	return normalized
}

func discordRSSTitleFallback(formatConfig *notifyfmt.FormatOptions) string {
	if formatConfig != nil &&
		formatConfig.RSS.TitleText != "" &&
		formatConfig.RSS.TitleText != notifyfmt.DefaultRSSTitleText {
		return formatConfig.RSS.TitleText
	}
	return discordRSSLabel(formatConfig, "untitled_rss_item", "Untitled RSS item")
}

func discordTimestampFormat(formatConfig *notifyfmt.FormatOptions) string {
	if formatConfig == nil {
		return ""
	}
	return formatConfig.TimestampFormat
}

func discordDisplayTimezone(formatConfig *notifyfmt.FormatOptions) string {
	if formatConfig == nil {
		return ""
	}
	return formatConfig.DisplayTimezone
}

func discordRSSShowAuthor(formatConfig *notifyfmt.FormatOptions) bool {
	return formatConfig != nil && formatConfig.RSS.ShowAuthor
}

func setDiscordRSSDescription(
	embed *MessageEmbed,
	view notifyfmt.RSSView,
	formatConfig *notifyfmt.FormatOptions,
	showEmpty bool,
	placeholder string,
) {
	limit := discordRSSDescriptionMaxChars(formatConfig)
	description := discordDescriptionText(view.Description, limit)
	if description == "" && showEmpty {
		description = textutil.TruncateText(placeholder, limit)
	}
	embed.Description = description
}

func appendDiscordRSSLink(
	embed *MessageEmbed,
	view notifyfmt.RSSView,
	formatConfig *notifyfmt.FormatOptions,
	showEmpty bool,
	placeholder string,
) {
	// Titles are never links; the link is always its own field now, falling
	// back to FeedURL exactly as the embed.URL chain did before.
	// appendDiscordRSSURLField does not validate URL structure --
	// that is fine, a field is display text, not a navigation target.
	if view.Link != "" {
		appendDiscordRSSURLField(embed, "Link", view.Link, "🔗", formatConfig, showEmpty, placeholder)
		return
	}
	if view.FeedURL != "" {
		appendDiscordRSSURLField(embed, "Link", view.FeedURL, "🔗", formatConfig, showEmpty, placeholder)
		return
	}
	if showEmpty {
		appendDiscordRSSField(embed, "Link", textutil.TruncateText(placeholder, discordEmbedFieldValueLimit), "🔗", false, formatConfig)
	}
}

func setDiscordRSSAuthor(embed *MessageEmbed, view notifyfmt.RSSView, showEmpty bool, placeholder string) {
	if view.Author != "" {
		embed.Author = &MessageEmbedAuthor{
			Name: discordNaturalText(view.Author, discordEmbedAuthorNameLimit),
		}
		return
	}
	if showEmpty {
		embed.Author = &MessageEmbedAuthor{Name: textutil.TruncateText(placeholder, discordEmbedAuthorNameLimit)}
	}
}

func appendDiscordRSSCategories(
	embed *MessageEmbed,
	view notifyfmt.RSSView,
	formatConfig *notifyfmt.FormatOptions,
	showEmpty bool,
	placeholder string,
) {
	categoriesStr := view.CategoryText
	if categoriesStr == "" && showEmpty {
		categoriesStr = placeholder
	}
	if categoriesStr == "" {
		return
	}
	appendDiscordRSSField(
		embed,
		"Categories",
		discordRichOrPlaceholder(categoriesStr, placeholder, discordEmbedFieldValueLimit),
		"📂",
		false,
		formatConfig,
	)
}

func appendDiscordRSSPublished(
	embed *MessageEmbed,
	view notifyfmt.RSSView,
	formatConfig *notifyfmt.FormatOptions,
	showEmpty bool,
	placeholder string,
) {
	publishedFieldValue := view.PublishedText
	if view.Published.IsZero() {
		if !showEmpty {
			return
		}
		publishedFieldValue = textutil.TruncateText(placeholder, discordEmbedFieldValueLimit)
	} else {
		embed.Timestamp = view.Published.Format(time.RFC3339)
	}
	appendDiscordRSSField(embed, "Published", publishedFieldValue, "📅", true, formatConfig)
}

func setDiscordRSSFooter(embed *MessageEmbed, view notifyfmt.RSSView, showEmpty bool, placeholder string) {
	footerText := view.FeedTitle
	if footerText == "" && showEmpty {
		footerText = placeholder
	}
	if footerText == "" {
		return
	}
	embed.Footer = &MessageEmbedFooter{
		Text: discordNaturalText(footerText, discordEmbedFooterTextLimit),
	}
}

func appendDiscordRSSURLField(
	embed *MessageEmbed,
	label, rawURL, icon string,
	formatConfig *notifyfmt.FormatOptions,
	showEmpty bool,
	placeholder string,
) {
	if rawURL == "" && !showEmpty {
		return
	}
	value := rawURL
	if value == "" {
		value = placeholder
	}
	value = discordURLOrPlaceholder(value, placeholder, discordEmbedFieldValueLimit)
	appendDiscordRSSField(embed, label, value, icon, false, formatConfig)
}

func appendDiscordRSSField(
	embed *MessageEmbed,
	label, value, icon string,
	inline bool,
	formatConfig *notifyfmt.FormatOptions,
) {
	label = discordRSSLabel(formatConfig, label, label)
	field := &MessageEmbedField{
		Name:   discordLabel(icon, label, discordShowIcons(formatConfig)),
		Value:  value,
		Inline: inline,
	}
	if discordEmbedCanAddField(embed, field) {
		embed.Fields = append(embed.Fields, field)
	}
}

func discordRSSDescriptionMaxChars(formatConfig *notifyfmt.FormatOptions) int {
	if formatConfig == nil || formatConfig.RSS.DescriptionMaxChars <= 0 {
		return discordRSSSummaryLimit
	}
	if formatConfig.RSS.DescriptionMaxChars > discordRSSSummaryLimit {
		return discordRSSSummaryLimit
	}
	if formatConfig.RSS.DescriptionMaxChars < 3 {
		return 3
	}
	return formatConfig.RSS.DescriptionMaxChars
}

func rssEmbedColor(feedType string, formatConfig *notifyfmt.FormatOptions) int {
	switch feedType {
	case notifyfmt.FeedTypeGovernment:
		return discordConfiguredColor(formatConfig, "government")
	case notifyfmt.FeedTypeRansomware:
		return discordConfiguredColor(formatConfig, "ransomware")
	default:
		return discordConfiguredColor(formatConfig, "rss")
	}
}

func discordShowIcons(formatConfig *notifyfmt.FormatOptions) bool {
	return formatConfig == nil || formatConfig.Discord.ShowIcons
}

func discordLabel(icon, text string, showIcons bool) string {
	if !showIcons || icon == "" {
		return text
	}
	return icon + " " + text
}

func discordDescriptionMaxChars(formatConfig *notifyfmt.FormatOptions) int {
	if formatConfig == nil || formatConfig.Discord.DescriptionMaxChars <= 0 {
		return notifyfmt.DefaultDescriptionMaxChars
	}
	if formatConfig.Discord.DescriptionMaxChars > discordEmbedFieldValueLimit {
		return discordEmbedFieldValueLimit
	}
	if formatConfig.Discord.DescriptionMaxChars < 3 {
		return 3
	}
	return formatConfig.Discord.DescriptionMaxChars
}

// discordColorPointer wraps a resolved color in a non-nil *int so that
// encoding/json's "omitempty" on MessageEmbed.Color only ever omits the key
// when the caller passes nil, not when the underlying color is the Go zero
// value (0 / #000000). Every resolved color (default or operator-configured)
// is always non-nil, so this is byte-identical on the wire to the previous
// plain-int field for every non-black color; only an explicit "#000000"
// configuration now marshals "color":0 instead of being silently dropped.
func discordColorPointer(color int) *int {
	return &color
}

func discordConfiguredColor(formatConfig *notifyfmt.FormatOptions, kind string) int {
	if formatConfig == nil {
		return defaultDiscordColor(kind)
	}
	var color string
	switch kind {
	case "government":
		color = formatConfig.Discord.GovernmentColor
	case "ransomware":
		color = formatConfig.Discord.RansomwareColor
	default:
		color = formatConfig.Discord.RSSColor
	}
	if parsed, ok := parseDiscordHexColor(color); ok {
		return parsed
	}
	return defaultDiscordColor(kind)
}

func defaultDiscordColor(kind string) int {
	switch kind {
	case "government":
		return defaultGovernmentColor
	case "ransomware":
		return defaultRansomwareColor
	default:
		return defaultRSSColor
	}
}

func parseDiscordHexColor(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if len(value) != 7 || value[0] != '#' {
		return 0, false
	}
	color := 0
	for _, r := range value[1:] {
		var n int
		switch {
		case r >= '0' && r <= '9':
			n = int(r - '0')
		case r >= 'a' && r <= 'f':
			n = int(r-'a') + 10
		case r >= 'A' && r <= 'F':
			n = int(r-'A') + 10
		default:
			return 0, false
		}
		color = color*16 + n
	}
	return color, true
}

func discordRichOrPlaceholder(value, placeholder string, maxLength int) string {
	if value == placeholder {
		return textutil.TruncateText(value, maxLength)
	}
	return discordRichText(value, maxLength)
}

func discordURLOrPlaceholder(value, placeholder string, maxLength int) string {
	if value == placeholder {
		return textutil.TruncateText(value, maxLength)
	}
	return discordURLText(value, maxLength)
}

// escapeDiscordMarkdownText neutralises the Discord masked-link form
// "[label](url)" in untrusted text. Only the backslash and the two brackets are
// escaped: Discord consumes a backslash before an escapable character, so text
// that is not a link renders byte-for-byte as before, while emphasis, inline
// code and blockquotes from a feed keep rendering exactly as they do today.
// The Slack twin escapeSlackMrkdwnText makes the same trade for <url|label>.
//
// The backslash pair must come first: strings.NewReplacer matches each input
// position once and never rescans its own output, so a feed-supplied backslash
// is doubled instead of arming the very sequence being escaped.
func escapeDiscordMarkdownText(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"[", `\[`,
		"]", `\]`,
	)
	return replacer.Replace(value)
}

// discordNaturalText renders untrusted natural-language text into a PLAIN-TEXT
// sink (embed title, author name, footer text), where Discord would display a
// backslash instead of consuming it. It must never escape.
func discordNaturalText(value string, maxLength int) string {
	if value == "" {
		return ""
	}
	if maxLength <= 2 {
		return textutil.TruncateText(value, maxLength)
	}
	return textutil.BidiIsolate(textutil.TruncateText(value, maxLength-2))
}

// discordRichText renders untrusted natural-language text into a sink where
// Discord renders markdown (embed field value). The order is escape, then
// truncate, then isolate: escaping grows the value by up to a factor of two, so
// it has to happen before the limit is applied, and the truncation markers must
// not be escaped.
func discordRichText(value string, maxLength int) string {
	if value == "" {
		return ""
	}
	escaped := escapeDiscordMarkdownText(value)
	if maxLength <= 2 {
		return textutil.TruncateText(escaped, maxLength)
	}
	return textutil.BidiIsolate(textutil.TrimDanglingEscape(textutil.TruncateText(escaped, maxLength-2)))
}

func discordDescriptionText(value string, maxLength int) string {
	if value == "" {
		return ""
	}
	escaped := escapeDiscordMarkdownText(value)
	if maxLength <= 2 {
		return textutil.TruncateDescription(escaped, maxLength)
	}
	return textutil.BidiIsolate(textutil.TrimDanglingEscape(textutil.TruncateDescription(escaped, maxLength-2)))
}

func discordURLText(value string, maxLength int) string {
	if value == "" {
		return ""
	}
	escaped := escapeDiscordMarkdownText(value)
	if maxLength <= 2 {
		return textutil.TruncateMiddle(escaped, maxLength)
	}
	return textutil.BidiIsolateLTR(textutil.TrimDanglingEscape(textutil.TruncateMiddle(escaped, maxLength-2)))
}
