package slack

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"
)

// slackMessage represents a Slack message with Block Kit formatting.
type slackMessage struct {
	Text   string       `json:"text"`             // Fallback text
	Blocks []slackBlock `json:"blocks,omitempty"` // Block Kit blocks
}

// slackBlock represents a Slack Block Kit block.
type slackBlock struct {
	Type      string            `json:"type"`
	Text      *slackTextObject  `json:"text,omitempty"`
	Fields    []slackTextObject `json:"fields,omitempty"`
	Elements  []slackTextObject `json:"elements,omitempty"` // For context blocks
	Accessory interface{}       `json:"accessory,omitempty"`
}

// slackTextObject represents a text object in Slack Block Kit.
type slackTextObject struct {
	Type string `json:"type"` // plain_text or mrkdwn
	Text string `json:"text"`
}

type slackButtonAccessory struct {
	Type               string          `json:"type"`
	Text               slackTextObject `json:"text"`
	URL                string          `json:"url"`
	ActionID           string          `json:"action_id,omitempty"`
	AccessibilityLabel string          `json:"accessibility_label,omitempty"`
}

const (
	slackBlockLimit        = 50
	slackHeaderTextLimit   = 150
	slackSectionTextLimit  = 3000
	slackFieldTextLimit    = 2000
	slackContextTextLimit  = 3000
	slackButtonURLLimit    = 3000
	slackSectionFieldLimit = 10
	ransomwareSourceLabel  = "Ransomware.live API"
	ransomwareAlertLabel   = "Ransomware Alert"
)

type ransomwareSlackFieldKind int

const (
	ransomwareSlackFieldCompact ransomwareSlackFieldKind = iota
	ransomwareSlackFieldFullWidth
)

type ransomwareSlackOptions struct {
	formatConfig        *notifyfmt.FormatOptions
	showEmpty           bool
	showFlags           bool
	displayLocale       string
	timestampFormat     string
	displayTimezone     string
	placeholder         string
	descriptionMaxChars int
}

type ransomwareSlackField struct {
	canonical   string
	kind        ransomwareSlackFieldKind
	label       string
	value       string
	rawMrkdwn   bool
	isURL       bool
	buttonLabel string
}

type rssSlackOptions struct {
	formatConfig        *notifyfmt.FormatOptions
	showEmpty           bool
	showAuthor          bool
	placeholder         string
	descriptionMaxChars int
	timestampFormat     string
	displayTimezone     string
}

// formatRansomwareMessage formats a ransomware entry as a Slack Block Kit message
func formatRansomwareMessage(entry model.RansomwareEntry, formatConfig *notifyfmt.FormatOptions) slackMessage {
	return slackMessage{
		Text:   ransomwareFallbackText(entry, formatConfig),
		Blocks: ransomwareSlackBlocks(entry, formatConfig),
	}
}

func ransomwareSlackBlocks(entry model.RansomwareEntry, formatConfig *notifyfmt.FormatOptions) []slackBlock {
	options := ransomwareSlackOptionsFromConfig(formatConfig)
	fieldOrder := slackRansomwareFieldOrder(formatConfig)
	blocks := make([]slackBlock, 0, 12)
	blocks = appendRansomwareHeaderBlock(blocks, entry, formatConfig)

	// Discord is the leading system (operator decision): formatRansomwareEmbed
	// unconditionally guarantees a Website field whenever entry.WebsiteURL is
	// non-empty, even when the operator's field order omits it. Slack must
	// follow. Inserted before the configured loop, mirroring Discord's
	// placement, so it has priority under the block-count cap rather than
	// risking silent loss if a long configured field order fills all
	// available blocks first.
	if strings.TrimSpace(entry.WebsiteURL) != "" && !slackRansomwareFieldOrderHasWebsite(fieldOrder) {
		if field, ok := ransomwareSlackFieldFor(formatfields.FieldWebsite, entry, options); ok {
			blocks = appendSlackBlock(blocks, field.block(options))
		}
	}

	var renderedDiscoveredField bool
	blocks, renderedDiscoveredField = appendRansomwareFieldBlocks(
		blocks,
		fieldOrder,
		entry,
		options,
	)
	return appendRansomwareContextBlock(blocks, entry, options, renderedDiscoveredField)
}

// slackRansomwareFieldOrderHasWebsite reports whether the operator's
// effective Slack field order already renders the website/url field, so
// ransomwareSlackBlocks's unconditional fallback (see above) does not add a
// second one. Mirrors discordRansomwareFieldOrderHasWebsite exactly.
func slackRansomwareFieldOrderHasWebsite(fieldOrder []string) bool {
	for _, name := range fieldOrder {
		if canonical, ok := formatfields.Normalize(name); ok && canonical == formatfields.FieldWebsite {
			return true
		}
	}
	return false
}

func appendRansomwareHeaderBlock(
	blocks []slackBlock, entry model.RansomwareEntry,
	formatConfig *notifyfmt.FormatOptions,
) []slackBlock {
	return appendSlackBlock(blocks, slackBlock{
		Type: "header",
		Text: &slackTextObject{
			Type: "plain_text",
			Text: slackHeaderText(ransomwareHeaderText(entry, formatConfig), notifyfmt.DefaultSlackTitleText),
		},
	})
}

func ransomwareSlackOptionsFromConfig(formatConfig *notifyfmt.FormatOptions) ransomwareSlackOptions {
	placeholder := slackEmptyFieldPlaceholder(formatConfig)
	return ransomwareSlackOptions{
		formatConfig:        formatConfig,
		showEmpty:           formatConfig != nil && formatConfig.ShowEmptyFields,
		showFlags:           formatConfig != nil && formatConfig.ShowUnicodeFlags,
		displayLocale:       slackDisplayLocale(formatConfig),
		timestampFormat:     slackTimestampFormat(formatConfig),
		displayTimezone:     slackDisplayTimezone(formatConfig),
		placeholder:         placeholder,
		descriptionMaxChars: slackDescriptionMaxChars(formatConfig),
	}
}

func slackDisplayLocale(formatConfig *notifyfmt.FormatOptions) string {
	if formatConfig == nil {
		return ""
	}
	return formatConfig.DisplayLocale
}

func slackTimestampFormat(formatConfig *notifyfmt.FormatOptions) string {
	if formatConfig == nil {
		return ""
	}
	return formatConfig.TimestampFormat
}

func slackDisplayTimezone(formatConfig *notifyfmt.FormatOptions) string {
	if formatConfig == nil {
		return ""
	}
	return formatConfig.DisplayTimezone
}

func slackEmptyFieldPlaceholder(formatConfig *notifyfmt.FormatOptions) string {
	placeholder := "N/A"
	if formatConfig != nil && formatConfig.EmptyFieldText != "" {
		placeholder = formatConfig.EmptyFieldText
	}
	return placeholder
}

func slackRansomwareLabel(formatConfig *notifyfmt.FormatOptions, key, fallback string) string {
	if formatConfig == nil {
		return fallback
	}
	return notifyfmt.FormatLabel(
		[]string{key},
		fallback,
		formatConfig.Slack.FieldLabels,
		formatConfig.FieldLabels,
	)
}

func slackRSSLabel(formatConfig *notifyfmt.FormatOptions, key, fallback string) string {
	if formatConfig == nil {
		return fallback
	}
	return notifyfmt.FormatLabel(
		rssLabelKeys(key),
		fallback,
		formatConfig.Slack.FieldLabels,
		formatConfig.RSS.FieldLabels,
		formatConfig.FieldLabels,
	)
}

// slackSpecialLabel resolves a single-purpose label (a button caption, not a
// ransomware entry field) the same way slackRansomwareLabel resolves a field
// label: same lookup order, same fallback. Kept as its own name for call-site
// clarity; delegates instead of duplicating the body (SonarQube duplicate
// finding, 2026-09-03).
func slackSpecialLabel(formatConfig *notifyfmt.FormatOptions, key, fallback string) string {
	return slackRansomwareLabel(formatConfig, key, fallback)
}

func rssLabelKeys(key string) []string {
	switch key {
	case formatfields.RSSFieldFeedTitle:
		return []string{formatfields.RSSFieldFeedTitle, "source"}
	default:
		return []string{key}
	}
}

func appendRansomwareFieldBlocks(
	blocks []slackBlock, fieldOrder []string,
	entry model.RansomwareEntry,
	options ransomwareSlackOptions,
) ([]slackBlock, bool) {
	compactFields := make([]slackTextObject, 0, len(fieldOrder))
	flushCompactFields := func() {
		blocks = appendSlackFieldSections(blocks, compactFields)
		compactFields = compactFields[:0]
	}

	renderedFullWidth := map[string]bool{}
	renderedDiscoveredField := false
	for _, fieldName := range fieldOrder {
		field, ok := ransomwareSlackFieldFor(fieldName, entry, options)
		if !ok {
			continue
		}
		if field.kind == ransomwareSlackFieldCompact {
			compactFields = append(compactFields, field.textObject(options.placeholder))
			if field.canonical == formatfields.FieldDiscovered {
				renderedDiscoveredField = true
			}
			continue
		}
		if renderedFullWidth[field.canonical] {
			continue
		}
		flushCompactFields()
		blocks = appendSlackBlock(blocks, field.block(options))
		renderedFullWidth[field.canonical] = true
	}
	flushCompactFields()
	return blocks, renderedDiscoveredField
}

func appendRansomwareContextBlock(
	blocks []slackBlock, entry model.RansomwareEntry,
	options ransomwareSlackOptions,
	renderedDiscoveredField bool,
) []slackBlock {
	discoveredTime := options.placeholder
	if !entry.Discovered.IsZero() {
		discoveredTime = slackDateWithDisplay(entry.Discovered, options.timestampFormat, options.displayTimezone)
	}
	sourceLabel := escapeSlackMrkdwnText(slackRansomwareLabel(options.formatConfig, "source", "Source"))
	sourceValue := escapeSlackMrkdwnText(slackRansomwareLabel(options.formatConfig, "ransomware_source", ransomwareSourceLabel))
	contextText := sourceLabel + ": " + sourceValue
	if !renderedDiscoveredField {
		discoveredLabel := escapeSlackMrkdwnText(slackRansomwareLabel(options.formatConfig, formatfields.FieldDiscovered, "Discovered"))
		contextText = fmt.Sprintf("%s: %s | %s: %s", discoveredLabel, discoveredTime, sourceLabel, sourceValue)
	}
	return appendSlackBlock(blocks, slackBlock{
		Type: "context",
		Elements: []slackTextObject{
			{
				Type: "mrkdwn",
				Text: textutil.TruncateText(contextText, slackContextTextLimit),
			},
		},
	})
}

func PreviewRansomwareEntry(entry model.RansomwareEntry, formatConfig *notifyfmt.FormatOptions) map[string]any {
	return slackMessagePreview("ransomware", formatRansomwareMessage(entry, formatConfig))
}

func slackRansomwareFieldOrder(formatConfig *notifyfmt.FormatOptions) []string {
	if formatConfig != nil {
		if len(formatConfig.Slack.FieldOrder) > 0 {
			return formatConfig.Slack.FieldOrder
		}
		if len(formatConfig.FieldOrder) > 0 {
			return formatConfig.FieldOrder
		}
	}
	return formatfields.DefaultSlackFieldOrder()
}

func canonicalSlackFieldName(fieldName string) string {
	canonical, ok := formatfields.Normalize(fieldName)
	if !ok {
		return ""
	}
	return canonical
}

func ransomwareSlackFieldFor(
	fieldName string,
	entry model.RansomwareEntry,
	options ransomwareSlackOptions,
) (ransomwareSlackField, bool) {
	canonical := canonicalSlackFieldName(fieldName)
	if canonical == "" {
		return ransomwareSlackField{}, false
	}

	sharedField, ok := formatfields.RansomwareFieldValueForDisplay(
		canonical,
		entry,
		options.showFlags,
		options.displayLocale,
		options.timestampFormat,
		options.displayTimezone,
	)
	if !ok {
		return ransomwareSlackField{}, false
	}
	field := ransomwareSlackField{
		canonical: sharedField.Canonical,
		label:     slackRansomwareLabel(options.formatConfig, sharedField.Canonical, sharedField.Label),
		value:     sharedField.Value,
	}
	switch canonical {
	case formatfields.FieldID:
		field.kind = ransomwareSlackFieldCompact
	case formatfields.FieldGroup:
		field.kind = ransomwareSlackFieldCompact
	case formatfields.FieldVictim:
		field.kind = ransomwareSlackFieldCompact
	case formatfields.FieldCountry:
		field.kind = ransomwareSlackFieldCompact
	case formatfields.FieldActivity:
		field.kind = ransomwareSlackFieldCompact
	case formatfields.FieldAttackDate, formatfields.FieldDiscovered, formatfields.FieldPublished:
		field.kind = ransomwareSlackFieldCompact
		if sharedField.HasParsedTime {
			field.value = slackDateWithDisplay(sharedField.ParsedTime, options.timestampFormat, options.displayTimezone)
			field.rawMrkdwn = true
		}
	case formatfields.FieldScreenshot:
		field.kind = ransomwareSlackFieldFullWidth
		field.isURL = true
		field.buttonLabel = slackSpecialLabel(options.formatConfig, "open_screenshot", "Open screenshot")
	case formatfields.FieldPostURL:
		field.kind = ransomwareSlackFieldFullWidth
		field.isURL = true
	case formatfields.FieldWebsite:
		field.kind = ransomwareSlackFieldFullWidth
		field.isURL = true
		field.buttonLabel = slackSpecialLabel(options.formatConfig, "open_website", "Open website")
	case formatfields.FieldDescription:
		field.kind = ransomwareSlackFieldFullWidth
		field.value = textutil.StripHTML(sharedField.Value)
	default:
		return ransomwareSlackField{}, false
	}

	// Trimmed before the empty test, matching Discord's createRansomwareField
	// (strings.TrimSpace(sharedField.Value) there): a whitespace-only value
	// must count as empty on both platforms, and a padded URL must render
	// clean so its button-target validation (slackURLButton) still succeeds.
	field.value = strings.TrimSpace(field.value)
	if field.value == "" {
		if !options.showEmpty {
			return ransomwareSlackField{}, false
		}
		field.value = options.placeholder
	}
	return field, true
}

func (field ransomwareSlackField) textObject(placeholder string) slackTextObject {
	return slackTextObject{
		Type: "mrkdwn",
		Text: slackLabeledFieldText(field.label, field.value, placeholder, field.rawMrkdwn, slackFieldTextLimit),
	}
}

func (field ransomwareSlackField) block(options ransomwareSlackOptions) slackBlock {
	text := slackLabeledText(
		field.label,
		field.value,
		options.placeholder,
		field.isURL,
		field.blockTextLimit(options),
	)
	if field.canonical == formatfields.FieldDescription && field.value != options.placeholder {
		text = slackLabeledDescriptionText(
			field.label,
			field.value,
			options.placeholder,
			field.blockTextLimit(options),
		)
	}
	result := slackBlock{
		Type: "section",
		Text: &slackTextObject{
			Type: "mrkdwn",
			Text: text,
		},
	}
	if field.buttonLabel != "" {
		if button, ok := slackURLButton(field.buttonLabel, field.value); ok {
			result.Accessory = button
		}
	}
	return result
}

func (field ransomwareSlackField) blockTextLimit(options ransomwareSlackOptions) int {
	if field.canonical == formatfields.FieldDescription && options.descriptionMaxChars > 0 {
		return options.descriptionMaxChars
	}
	return slackSectionTextLimit
}

func slackDescriptionMaxChars(formatConfig *notifyfmt.FormatOptions) int {
	if formatConfig == nil || formatConfig.Slack.DescriptionMaxChars <= 0 {
		return notifyfmt.DefaultDescriptionMaxChars
	}
	if formatConfig.Slack.DescriptionMaxChars > slackSectionTextLimit {
		return slackSectionTextLimit
	}
	return formatConfig.Slack.DescriptionMaxChars
}

// slackURLButton validates rawURL as received -- including any bidi control
// character it carries -- and only then percent-encodes those controls for
// the string actually emitted. Validating the raw string first (rather than
// the encoded one) matters: a bidi control in the userinfo component makes
// url.Parse fail, and encoding before validating would turn a URL that
// renders with no button today into a live link. The length cap is applied
// to the encoded form, which is what is actually sent.
func slackURLButton(label, rawURL string) (slackButtonAccessory, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || parsed.Host == "" {
		return slackButtonAccessory{}, false
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return slackButtonAccessory{}, false
	}
	encoded := textutil.PercentEncodeBidiControls(rawURL)
	if len([]rune(encoded)) > slackButtonURLLimit {
		return slackButtonAccessory{}, false
	}
	return slackButtonAccessory{
		Type:               "button",
		Text:               slackTextObject{Type: "plain_text", Text: textutil.TruncateText(label, 75)},
		URL:                encoded,
		ActionID:           slackButtonActionID(label),
		AccessibilityLabel: textutil.TruncateText(label, 75),
	}, true
}

func slackButtonActionID(label string) string {
	normalized := strings.ToLower(strings.TrimSpace(label))
	normalized = strings.NewReplacer(" ", "_", "-", "_").Replace(normalized)
	if normalized == "" {
		return "open_link"
	}
	return normalized
}

func ransomwareHeaderText(entry model.RansomwareEntry, formatConfig *notifyfmt.FormatOptions) string {
	if strings.TrimSpace(entry.Group) != "" || strings.TrimSpace(entry.Victim) != "" {
		alertLabel := slackRansomwareLabel(formatConfig, "ransomware_alert", ransomwareAlertLabel)
		return escapeSlackMrkdwnText(alertLabel + ": " + model.DisplayRansomwareTitle(entry))
	}
	if formatConfig != nil && formatConfig.Slack.TitleText != "" {
		return escapeSlackMrkdwnText(formatConfig.Slack.TitleText)
	}
	return escapeSlackMrkdwnText(notifyfmt.DefaultSlackTitleText)
}

func ransomwareFallbackText(entry model.RansomwareEntry, formatConfig *notifyfmt.FormatOptions) string {
	alertLabel := slackRansomwareLabel(formatConfig, "ransomware_alert", ransomwareAlertLabel)
	parts := []string{alertLabel + ": " + model.DisplayRansomwareTitle(entry)}
	appendPart := func(label, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, label+": "+value)
		}
	}

	appendPart(slackRansomwareLabel(formatConfig, formatfields.FieldGroup, "Group"), entry.Group)
	appendPart(slackRansomwareLabel(formatConfig, formatfields.FieldVictim, "Victim"), entry.Victim)
	appendPart(slackRansomwareLabel(formatConfig, formatfields.FieldCountry, "Country"), entry.Country)
	appendPart(slackRansomwareLabel(formatConfig, formatfields.FieldActivity, "Activity"), entry.Activity)
	appendPart(
		slackRansomwareLabel(formatConfig, formatfields.FieldAttackDate, "Attack Date"),
		timeutil.FormatDisplayTimestamp(entry.AttackDate, slackTimestampFormat(formatConfig), slackDisplayTimezone(formatConfig)),
	)
	if !entry.Discovered.IsZero() {
		appendPart(
			slackRansomwareLabel(formatConfig, formatfields.FieldDiscovered, "Discovered"),
			slackDateFallbackWithDisplay(entry.Discovered, slackTimestampFormat(formatConfig), slackDisplayTimezone(formatConfig)),
		)
	}
	if !entry.Published.IsZero() {
		appendPart(
			slackRansomwareLabel(formatConfig, formatfields.FieldPublished, "Published"),
			slackDateFallbackWithDisplay(entry.Published, slackTimestampFormat(formatConfig), slackDisplayTimezone(formatConfig)),
		)
	}
	appendPart(slackRansomwareLabel(formatConfig, formatfields.FieldWebsite, "Website"), entry.WebsiteURL)
	if entry.ClaimURL != "" {
		appendPart(slackRansomwareLabel(formatConfig, formatfields.FieldPostURL, "Ransom URL"), textutil.DefangURL(entry.ClaimURL))
	}
	appendPart(slackRansomwareLabel(formatConfig, formatfields.FieldScreenshot, "Screenshot"), entry.Screenshot)
	appendPart(slackRansomwareLabel(formatConfig, formatfields.FieldDescription, "Description"), textutil.StripHTML(entry.Description))
	appendPart(
		slackRansomwareLabel(formatConfig, "source", "Source"),
		slackRansomwareLabel(formatConfig, "ransomware_source", ransomwareSourceLabel),
	)

	return slackFallbackText(parts...)
}

func rssFallbackText(view notifyfmt.RSSView, formatConfig *notifyfmt.FormatOptions, feedType string) string {
	parts := []string{slackRSSHeaderText(formatConfig, feedType) + ": " + view.Title}
	appendPart := func(label, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, label+": "+value)
		}
	}

	appendPart(slackRSSLabel(formatConfig, formatfields.RSSFieldDescription, "Description"), view.Description)
	appendPart(slackRSSLabel(formatConfig, formatfields.RSSFieldLink, "Link"), view.Link)
	if formatConfig != nil && formatConfig.RSS.ShowAuthor {
		appendPart(slackRSSLabel(formatConfig, formatfields.RSSFieldAuthor, "Author"), view.Author)
	}
	if len(view.Categories) > 0 {
		appendPart(slackRSSLabel(formatConfig, formatfields.RSSFieldCategories, "Categories"), strings.Join(view.Categories, ", "))
	}
	if !view.Published.IsZero() {
		appendPart(slackRSSLabel(formatConfig, formatfields.RSSFieldPublished, "Published"), view.PublishedText)
	}
	appendPart(slackRSSLabel(formatConfig, formatfields.RSSFieldFeedTitle, "Source"), view.SourceText)

	return slackFallbackText(parts...)
}

func slackFallbackText(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return slackNaturalText(strings.Join(cleaned, " | "), slackSectionTextLimit)
}

func appendSlackBlock(blocks []slackBlock, block slackBlock) []slackBlock {
	if len(blocks) >= slackBlockLimit {
		return blocks
	}
	return append(blocks, block)
}

func appendSlackFieldSections(blocks []slackBlock, fields []slackTextObject) []slackBlock {
	if len(fields) == 0 {
		return blocks
	}
	for i := 0; i < len(fields); i += slackSectionFieldLimit {
		end := i + slackSectionFieldLimit
		if end > len(fields) {
			end = len(fields)
		}
		blocks = appendSlackBlock(blocks, slackBlock{
			Type:   "section",
			Fields: append([]slackTextObject(nil), fields[i:end]...),
		})
	}
	return blocks
}

func trimSlackBlocks(blocks []slackBlock) []slackBlock {
	if len(blocks) > slackBlockLimit {
		return blocks[:slackBlockLimit]
	}
	return blocks
}

func slackLabeledText(label, value, placeholder string, isURL bool, maxLength int) string {
	return slackLabeledFieldText(label, value, placeholder, false, maxLength, isURL)
}

func slackLabeledFieldText(label, value, placeholder string, rawMrkdwn bool, maxLength int, isURL ...bool) string {
	prefix := fmt.Sprintf("*%s:*\n", escapeSlackMrkdwnText(label))
	valueBudget := maxLength - len([]rune(prefix))
	if valueBudget < 0 {
		valueBudget = 0
	}
	renderedValue := textutil.TruncateText(value, valueBudget)
	if value != placeholder {
		switch {
		case len(isURL) > 0 && isURL[0]:
			renderedValue = slackURLText(value, valueBudget)
		case rawMrkdwn:
			renderedValue = textutil.TruncateText(value, valueBudget)
		default:
			renderedValue = slackNaturalText(value, valueBudget)
		}
	}
	return prefix + renderedValue
}

func slackLabeledDescriptionText(label, value, placeholder string, maxLength int) string {
	prefix := fmt.Sprintf("*%s:*\n", escapeSlackMrkdwnText(label))
	valueBudget := maxLength - len([]rune(prefix))
	if valueBudget < 0 {
		valueBudget = 0
	}
	if value == placeholder {
		return prefix + textutil.TruncateText(value, valueBudget)
	}
	return prefix + slackDescriptionText(value, valueBudget)
}

func slackDate(value time.Time) string {
	return slackDateWithDisplay(value, "", "")
}

func slackDateWithDisplay(value time.Time, timestampFormat, displayTimezone string) string {
	fallback := timeutil.FormatDisplayTime(value, timestampFormat, displayTimezone)
	return fmt.Sprintf("<!date^%d^{date_short_pretty} {time_secs}|%s>", value.Unix(), fallback)
}

func slackDateFallback(value time.Time) string {
	return slackDateFallbackWithDisplay(value, "", "")
}

func slackDateFallbackWithDisplay(value time.Time, timestampFormat, displayTimezone string) string {
	return timeutil.FormatDisplayTime(value, timestampFormat, displayTimezone)
}

// slackUntitledRSSHeaderText replaces an RSS header whose title is nothing but
// bidi controls. It is the same wording notifyfmt.rssDisplayTitle already uses
// as its last resort (internal/notifyfmt/rss.go:63,69), so no new operator-
// facing string is introduced.
const slackUntitledRSSHeaderText = "Untitled RSS item"

// slackHeaderText renders the plain_text of a Slack header block. Header text
// is the only rendered Slack string that carries no bidi isolate: a header
// block is a single value with no label and no neighbouring text, so there is
// nothing for an isolate to fence it off from. The bidi controls an untrusted
// title can contain are still removed, because U+202A-U+202E and U+2066-U+2069
// re-order the header line itself regardless of the plain_text type. A title
// that consists only of such controls would strip to "", which a Slack header
// block does not accept, so the caller's constant fallback is used instead.
// Truncation runs last, so the Slack header limit counts only characters that
// are actually rendered.
func slackHeaderText(value, fallback string) string {
	stripped := textutil.StripBidiControls(value)
	if strings.TrimSpace(stripped) == "" {
		stripped = fallback
	}
	return textutil.TruncateText(stripped, slackHeaderTextLimit)
}

func slackNaturalText(value string, maxLength int) string {
	if value == "" {
		return ""
	}
	escaped := escapeSlackMrkdwnText(value)
	if maxLength <= 2 {
		return textutil.TruncateText(escaped, maxLength)
	}
	return textutil.BidiIsolate(textutil.TruncateText(escaped, maxLength-2))
}

func slackDescriptionText(value string, maxLength int) string {
	if value == "" {
		return ""
	}
	escaped := escapeSlackMrkdwnText(value)
	if maxLength <= 2 {
		return textutil.TruncateDescription(escaped, maxLength)
	}
	return textutil.BidiIsolate(textutil.TruncateDescription(escaped, maxLength-2))
}

func slackURLText(value string, maxLength int) string {
	if value == "" {
		return ""
	}
	escaped := escapeSlackMrkdwnText(value)
	if maxLength <= 2 {
		return textutil.TruncateMiddle(escaped, maxLength)
	}
	return textutil.BidiIsolateLTR(textutil.TruncateMiddle(escaped, maxLength-2))
}

func escapeSlackMrkdwnText(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(value)
}

// formatRSSMessage formats an RSS entry as a Slack Block Kit message
func formatRSSMessage(entry model.RSSEntry, formatConfig *notifyfmt.FormatOptions, feedType string) slackMessage {
	fallbackView := notifyfmt.BuildRSSView(
		entry,
		rssViewOptions(formatConfig, slackSpecialLabel(formatConfig, "rss_item", "RSS item"), slackFieldTextLimit),
	)
	blockView := notifyfmt.BuildRSSView(entry, rssViewOptions(formatConfig, slackRSSHeaderText(formatConfig, feedType), slackFieldTextLimit))
	return slackMessage{
		Text:   rssFallbackText(fallbackView, formatConfig, feedType),
		Blocks: rssSlackBlocks(blockView, formatConfig, feedType),
	}
}

func rssViewOptions(formatConfig *notifyfmt.FormatOptions, titleFallback string, categoryMaxChars int) notifyfmt.RSSViewOptions {
	return notifyfmt.RSSViewOptions{
		TitleFallback:    titleFallback,
		TimestampFormat:  slackTimestampFormat(formatConfig),
		DisplayTimezone:  slackDisplayTimezone(formatConfig),
		CategoryLimit:    5,
		CategoryMaxChars: categoryMaxChars,
	}
}

func rssSlackBlocks(view notifyfmt.RSSView, formatConfig *notifyfmt.FormatOptions, feedType string) []slackBlock {
	options := rssSlackOptionsFromConfig(formatConfig)
	fieldOrder := slackRSSFieldOrder(formatConfig)
	fieldSet := rssFieldSet(fieldOrder)

	return trimSlackBlocks(appendRSSSlackFields(nil, fieldOrder, fieldSet, view, formatConfig, feedType, options))
}

func rssSlackOptionsFromConfig(formatConfig *notifyfmt.FormatOptions) rssSlackOptions {
	return rssSlackOptions{
		formatConfig:        formatConfig,
		showEmpty:           formatConfig != nil && formatConfig.ShowEmptyFields,
		showAuthor:          formatConfig != nil && formatConfig.RSS.ShowAuthor,
		placeholder:         slackEmptyFieldPlaceholder(formatConfig),
		descriptionMaxChars: slackRSSDescriptionMaxChars(formatConfig),
		timestampFormat:     slackTimestampFormat(formatConfig),
		displayTimezone:     slackDisplayTimezone(formatConfig),
	}
}

func appendRSSSlackFields(
	blocks []slackBlock, fieldOrder []string,
	fieldSet map[string]bool,
	view notifyfmt.RSSView,
	formatConfig *notifyfmt.FormatOptions,
	feedType string,
	options rssSlackOptions,
) []slackBlock {
	contextAdded := false

	for _, fieldName := range fieldOrder {
		blocks, contextAdded = appendRSSSlackFieldByName(
			blocks,
			fieldName,
			fieldSet,
			view,
			formatConfig,
			feedType,
			options,
			contextAdded,
		)
	}
	return blocks
}

func appendRSSSlackFieldByName(
	blocks []slackBlock, fieldName string,
	fieldSet map[string]bool,
	view notifyfmt.RSSView,
	formatConfig *notifyfmt.FormatOptions,
	feedType string,
	options rssSlackOptions,
	contextAdded bool,
) ([]slackBlock, bool) {
	switch fieldName {
	case formatfields.RSSFieldTitle:
		blocks = appendRSSSlackTitle(blocks, view, fieldSet[formatfields.RSSFieldLink], options)
	case formatfields.RSSFieldDescription:
		blocks = appendRSSSlackDescription(blocks, view, options)
	case formatfields.RSSFieldLink:
		if !fieldSet[formatfields.RSSFieldTitle] {
			blocks = appendRSSSlackLink(blocks, view, options.showEmpty, options.placeholder, options)
		} else if view.Link == "" && options.showEmpty {
			label := slackRSSLabel(options.formatConfig, formatfields.RSSFieldLink, "Link")
			blocks = appendRSSSlackPlaceholder(blocks, label, options.placeholder, slackSectionTextLimit)
		}
	case formatfields.RSSFieldAuthor:
		if options.showAuthor {
			label := slackRSSLabel(options.formatConfig, formatfields.RSSFieldAuthor, "Author")
			blocks = appendRSSSlackField(blocks, label, view.Author, options.showEmpty, options.placeholder)
		}
	case formatfields.RSSFieldCategories:
		blocks = appendRSSSlackCategories(blocks, view, options)
	case formatfields.RSSFieldPublished, formatfields.RSSFieldFeedTitle:
		if !contextAdded {
			blocks = appendRSSSlackContext(
				blocks,
				view,
				fieldSet[formatfields.RSSFieldPublished],
				fieldSet[formatfields.RSSFieldFeedTitle],
				options,
			)
			contextAdded = true
		}
	case formatfields.RSSFieldFeedURL:
		label := slackRSSLabel(options.formatConfig, formatfields.RSSFieldFeedURL, "Feed URL")
		blocks = appendRSSSlackURLField(blocks, label, view.FeedURL, options.showEmpty, options.placeholder)
	}
	return blocks, contextAdded
}

func PreviewRSSEntry(entry model.RSSEntry, formatConfig *notifyfmt.FormatOptions, feedType string) map[string]any {
	return slackMessagePreview("rss", formatRSSMessage(entry, formatConfig, feedType))
}

func slackMessagePreview(kind string, msg slackMessage) map[string]any {
	blocks := make([]map[string]any, 0, len(msg.Blocks))
	for _, block := range msg.Blocks {
		blocks = append(blocks, slackBlockPreview(block))
	}
	return map[string]any{
		"platform":      "slack",
		"kind":          kind,
		"fallback_text": msg.Text,
		"blocks":        blocks,
	}
}

func slackBlockPreview(block slackBlock) map[string]any {
	preview := map[string]any{
		"type": block.Type,
	}
	if block.Text != nil {
		preview["text"] = slackTextObjectPreview(*block.Text)
	}
	if len(block.Fields) > 0 {
		preview["fields"] = slackTextObjectsPreview(block.Fields)
	}
	if len(block.Elements) > 0 {
		preview["elements"] = slackTextObjectsPreview(block.Elements)
	}
	if block.Accessory != nil {
		preview["accessory"] = slackAccessoryPreview(block.Accessory)
	}
	return preview
}

func slackTextObjectsPreview(objects []slackTextObject) []map[string]any {
	previews := make([]map[string]any, 0, len(objects))
	for _, object := range objects {
		previews = append(previews, slackTextObjectPreview(object))
	}
	return previews
}

func slackTextObjectPreview(object slackTextObject) map[string]any {
	return map[string]any{
		"type": object.Type,
		"text": object.Text,
	}
}

func slackAccessoryPreview(accessory any) map[string]any {
	switch value := accessory.(type) {
	case slackButtonAccessory:
		return map[string]any{
			"type":                value.Type,
			"text":                slackTextObjectPreview(value.Text),
			"url":                 value.URL,
			"action_id":           value.ActionID,
			"accessibility_label": value.AccessibilityLabel,
		}
	default:
		return map[string]any{
			"type": fmt.Sprintf("%T", accessory),
		}
	}
}

func slackRSSFieldOrder(formatConfig *notifyfmt.FormatOptions) []string {
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

func rssFieldSet(fieldOrder []string) map[string]bool {
	fields := make(map[string]bool, len(fieldOrder))
	for _, field := range fieldOrder {
		fields[field] = true
	}
	return fields
}

func appendRSSSlackTitle(
	blocks []slackBlock, view notifyfmt.RSSView,
	includeLink bool,
	options rssSlackOptions,
) []slackBlock {
	blocks = append(blocks, slackBlock{
		Type: "header",
		Text: &slackTextObject{
			Type: "plain_text",
			Text: slackHeaderText(escapeSlackMrkdwnText(view.Title), slackUntitledRSSHeaderText),
		},
	})

	// Titles are never links. The button below is the URL's only home; if
	// it fails Slack's own validation, the fallback
	// field a few lines down guarantees the URL is still shown somewhere.
	titleText := slackNaturalText(view.Title, slackSectionTextLimit-2)
	titleBlock := slackBlock{
		Type: "section",
		Text: &slackTextObject{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*%s*", titleText),
		},
	}
	buttonAttached := false
	if includeLink {
		buttonLabel := slackSpecialLabel(options.formatConfig, "open_article", "Open Article")
		buttonURL := view.Link
		if buttonURL == "" {
			buttonLabel = slackSpecialLabel(options.formatConfig, "open_feed", "Open Feed")
			buttonURL = view.FeedURL
		}
		if button, ok := slackURLButton(buttonLabel, buttonURL); ok {
			titleBlock.Accessory = button
			buttonAttached = true
		}
	}
	blocks = append(blocks, titleBlock)
	blocks = append(blocks, slackBlock{Type: "divider"})

	// The removed title link always carried view.Link and nothing else, and
	// the button is now that URL's only home. If the button failed
	// slackURLButton's own validation (malformed link, non-http(s) scheme,
	// or over slackButtonURLLimit), fall back to a
	// standalone field so the article URL is never silently dropped. Scoped
	// to view.Link deliberately: the FeedURL case is the button's alone, and
	// falling back on it here would collide with the RSSFieldLink switch
	// case's own show_empty placeholder for the same empty view.Link. The
	// label is always "Link", matching appendRSSSlackLink's own label for
	// the same canonical field, so it never collides with a
	// separately-configured "Feed URL" field either.
	if includeLink && !buttonAttached && view.Link != "" {
		label := slackRSSLabel(options.formatConfig, formatfields.RSSFieldLink, "Link")
		blocks = appendRSSSlackURLField(blocks, label, view.Link, false, options.placeholder)
	}

	return blocks
}

func appendRSSSlackDescription(
	blocks []slackBlock, view notifyfmt.RSSView,
	options rssSlackOptions,
) []slackBlock {
	if view.Description != "" {
		return append(blocks, slackBlock{
			Type: "section",
			Text: &slackTextObject{
				Type: "mrkdwn",
				Text: slackDescriptionText(view.Description, options.descriptionMaxChars),
			},
		})
	}
	if options.showEmpty {
		label := slackRSSLabel(options.formatConfig, formatfields.RSSFieldDescription, "Description")
		return appendRSSSlackPlaceholder(blocks, label, options.placeholder, slackSectionTextLimit)
	}
	return blocks
}

func appendRSSSlackLink(blocks []slackBlock, view notifyfmt.RSSView, showEmpty bool, placeholder string, options rssSlackOptions) []slackBlock {
	label := slackRSSLabel(options.formatConfig, formatfields.RSSFieldLink, "Link")
	return appendRSSSlackURLField(blocks, label, view.Link, showEmpty, placeholder)
}

func appendRSSSlackField(blocks []slackBlock, label, value string, showEmpty bool, placeholder string) []slackBlock {
	if value == "" && !showEmpty {
		return blocks
	}
	if value == "" {
		value = placeholder
	}
	fieldPlaceholder := ""
	if value == placeholder {
		fieldPlaceholder = placeholder
	}
	return append(blocks, slackBlock{
		Type: "section",
		Fields: []slackTextObject{
			{
				Type: "mrkdwn",
				Text: slackLabeledText(label, value, fieldPlaceholder, false, slackFieldTextLimit),
			},
		},
	})
}

func appendRSSSlackCategories(blocks []slackBlock, view notifyfmt.RSSView, options rssSlackOptions) []slackBlock {
	categoriesStr := view.CategoryText
	if categoriesStr == "" && options.showEmpty {
		categoriesStr = options.placeholder
	}
	if categoriesStr == "" {
		return blocks
	}
	categoriesPlaceholder := ""
	if categoriesStr == options.placeholder {
		categoriesPlaceholder = options.placeholder
	}
	return append(blocks, slackBlock{
		Type: "section",
		Fields: []slackTextObject{
			{
				Type: "mrkdwn",
				Text: slackLabeledText(
					slackRSSLabel(options.formatConfig, formatfields.RSSFieldCategories, "Categories"),
					categoriesStr,
					categoriesPlaceholder,
					false,
					slackFieldTextLimit,
				),
			},
		},
	})
}

func appendRSSSlackURLField(blocks []slackBlock, label, rawURL string, showEmpty bool, placeholder string) []slackBlock {
	if rawURL == "" && !showEmpty {
		return blocks
	}
	value := rawURL
	fieldPlaceholder := ""
	if value == "" {
		value = placeholder
		fieldPlaceholder = placeholder
	}
	return append(blocks, slackBlock{
		Type: "section",
		Text: &slackTextObject{
			Type: "mrkdwn",
			Text: slackLabeledText(label, value, fieldPlaceholder, value != placeholder, slackSectionTextLimit),
		},
	})
}

func appendRSSSlackPlaceholder(blocks []slackBlock, label, placeholder string, maxLength int) []slackBlock {
	return append(blocks, slackBlock{
		Type: "section",
		Text: &slackTextObject{
			Type: "mrkdwn",
			Text: slackLabeledText(label, placeholder, placeholder, false, maxLength),
		},
	})
}

func appendRSSSlackContext(
	blocks []slackBlock, view notifyfmt.RSSView,
	includePublished, includeFeedTitle bool,
	options rssSlackOptions,
) []slackBlock {
	parts := make([]string, 0, 2)
	if includePublished {
		publishedLabel := escapeSlackMrkdwnText(slackRSSLabel(options.formatConfig, formatfields.RSSFieldPublished, "Published"))
		if !view.Published.IsZero() {
			parts = append(parts, publishedLabel+": "+slackDateWithDisplay(view.Published, options.timestampFormat, options.displayTimezone))
		} else {
			parts = append(parts, publishedLabel+": "+options.placeholder)
		}
	}
	if includeFeedTitle {
		if view.SourceText != "" {
			sourceLabel := escapeSlackMrkdwnText(slackRSSLabel(options.formatConfig, formatfields.RSSFieldFeedTitle, "Source"))
			parts = append(parts, sourceLabel+": "+slackNaturalText(view.SourceText, slackContextTextLimit))
		}
	}
	if len(parts) == 0 {
		return blocks
	}
	return append(blocks, slackBlock{
		Type: "context",
		Elements: []slackTextObject{
			{
				Type: "mrkdwn",
				Text: textutil.TruncateText(strings.Join(parts, " | "), slackContextTextLimit),
			},
		},
	})
}

func slackRSSDescriptionMaxChars(formatConfig *notifyfmt.FormatOptions) int {
	if formatConfig == nil || formatConfig.RSS.DescriptionMaxChars <= 0 {
		return notifyfmt.DefaultDescriptionMaxChars
	}
	if formatConfig.RSS.DescriptionMaxChars > slackSectionTextLimit {
		return slackSectionTextLimit
	}
	return formatConfig.RSS.DescriptionMaxChars
}

func slackRSSHeaderText(formatConfig *notifyfmt.FormatOptions, feedType string) string {
	switch feedType {
	case notifyfmt.FeedTypeGovernment:
		return slackRSSLabel(formatConfig, "government_rss_update", "Government RSS Update")
	case notifyfmt.FeedTypeRansomware:
		return slackRSSLabel(formatConfig, "ransomware_rss_update", "Ransomware RSS Update")
	default:
		if formatConfig != nil &&
			formatConfig.RSS.TitleText != "" &&
			formatConfig.RSS.TitleText != notifyfmt.DefaultRSSTitleText {
			return formatConfig.RSS.TitleText
		}
		if formatConfig != nil &&
			formatConfig.Slack.RSSText != "" &&
			formatConfig.Slack.RSSText != notifyfmt.DefaultSlackRSSText {
			return formatConfig.Slack.RSSText
		}
		if formatConfig != nil &&
			formatConfig.RSS.TitleText != "" &&
			formatConfig.RSS.TitleText != notifyfmt.DefaultRSSTitleText {
			return formatConfig.RSS.TitleText
		}
		return slackRSSLabel(formatConfig, "rss_update", notifyfmt.DefaultSlackRSSText)
	}
}
