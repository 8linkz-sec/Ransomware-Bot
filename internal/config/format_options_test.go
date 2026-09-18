package config

import (
	"reflect"
	"testing"
)

func TestNotificationFormatOptionsCopiesFormatConfig(t *testing.T) {
	format := &FormatConfig{
		ShowUnicodeFlags: true,
		DisplayLocale:    "de",
		TimestampFormat:  "02.01.2006 15:04 MST",
		DisplayTimezone:  "Europe/Berlin",
		ShowEmptyFields:  true,
		EmptyFieldText:   "-",
		FieldOrder:       []string{"group", "victim"},
		FieldLabels:      map[string]string{"victim": "Ziel"},
		RSS: RSSFormatConfig{
			TitleText:           "RSS",
			ShowAuthor:          true,
			FieldOrder:          []string{"title", "author"},
			DescriptionMaxChars: 200,
			FieldLabels:         map[string]string{"author": "Autor"},
		},
		Discord: DiscordFormatConfig{
			ShowIcons:           true,
			RansomwareColor:     "#010203",
			RSSColor:            "#040506",
			GovernmentColor:     "#070809",
			DescriptionMaxChars: 123,
			FieldLabels:         map[string]string{"group": "Gruppe"},
		},
		Slack: SlackFormatConfig{
			TitleText:           "Alarm",
			RSSText:             "Feed",
			FieldOrder:          []string{"victim", "group"},
			DescriptionMaxChars: 456,
			FieldLabels:         map[string]string{"open_feed": "Feed oeffnen"},
		},
	}

	options := NotificationFormatOptions(format)
	if options == nil {
		t.Fatal("NotificationFormatOptions() returned nil")
	}
	if options.DisplayLocale != format.DisplayLocale ||
		options.RSS.TitleText != format.RSS.TitleText ||
		options.Discord.RansomwareColor != format.Discord.RansomwareColor ||
		options.Slack.TitleText != format.Slack.TitleText {
		t.Fatalf("NotificationFormatOptions() lost values: %#v", options)
	}
	if !reflect.DeepEqual(options.FieldOrder, format.FieldOrder) ||
		!reflect.DeepEqual(options.RSS.FieldOrder, format.RSS.FieldOrder) ||
		!reflect.DeepEqual(options.Discord.FieldLabels, format.Discord.FieldLabels) ||
		!reflect.DeepEqual(options.Slack.FieldLabels, format.Slack.FieldLabels) {
		t.Fatalf("NotificationFormatOptions() did not preserve collections: %#v", options)
	}

	format.FieldOrder[0] = "changed"
	format.FieldLabels["victim"] = "changed"
	format.RSS.FieldOrder[0] = "changed"
	format.Discord.FieldLabels["group"] = "changed"
	format.Slack.FieldLabels["open_feed"] = "changed"

	if options.FieldOrder[0] != "group" ||
		options.FieldLabels["victim"] != "Ziel" ||
		options.RSS.FieldOrder[0] != "title" ||
		options.Discord.FieldLabels["group"] != "Gruppe" ||
		options.Slack.FieldLabels["open_feed"] != "Feed oeffnen" {
		t.Fatalf("NotificationFormatOptions() aliased config collections: %#v", options)
	}
}

func TestNotificationFormatOptionsNil(t *testing.T) {
	if got := NotificationFormatOptions(nil); got != nil {
		t.Fatalf("NotificationFormatOptions(nil) = %#v, want nil", got)
	}
}
