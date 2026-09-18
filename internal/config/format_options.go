package config

import "github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"

// NotificationFormatOptions converts JSON formatting configuration into
// formatter-facing options.
func NotificationFormatOptions(format *FormatConfig) *notifyfmt.FormatOptions {
	if format == nil {
		return nil
	}
	return &notifyfmt.FormatOptions{
		ShowUnicodeFlags: format.ShowUnicodeFlags,
		DisplayLocale:    format.DisplayLocale,
		TimestampFormat:  format.TimestampFormat,
		DisplayTimezone:  format.DisplayTimezone,
		ShowEmptyFields:  format.ShowEmptyFields,
		EmptyFieldText:   format.EmptyFieldText,
		FieldOrder:       cloneStringSlice(format.FieldOrder),
		FieldLabels:      cloneStringMap(format.FieldLabels),
		RSS: notifyfmt.RSSFormatOptions{
			TitleText:           format.RSS.TitleText,
			ShowAuthor:          format.RSS.ShowAuthor,
			FieldOrder:          cloneStringSlice(format.RSS.FieldOrder),
			DescriptionMaxChars: format.RSS.DescriptionMaxChars,
			FieldLabels:         cloneStringMap(format.RSS.FieldLabels),
		},
		Discord: notifyfmt.DiscordFormatOptions{
			ShowIcons:           format.Discord.ShowIcons,
			RansomwareColor:     format.Discord.RansomwareColor,
			RSSColor:            format.Discord.RSSColor,
			GovernmentColor:     format.Discord.GovernmentColor,
			DescriptionMaxChars: format.Discord.DescriptionMaxChars,
			FieldLabels:         cloneStringMap(format.Discord.FieldLabels),
		},
		Slack: notifyfmt.SlackFormatOptions{
			TitleText:           format.Slack.TitleText,
			RSSText:             format.Slack.RSSText,
			FieldOrder:          cloneStringSlice(format.Slack.FieldOrder),
			DescriptionMaxChars: format.Slack.DescriptionMaxChars,
			FieldLabels:         cloneStringMap(format.Slack.FieldLabels),
		},
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}
