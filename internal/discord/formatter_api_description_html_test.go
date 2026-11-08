package discord

import (
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
)

func TestDiscordAPIDescriptionStripsHTMLBeforeMarkdownEscaping(t *testing.T) {
	for _, tc := range []struct {
		name, description, want string
	}{
		{"tags and entity", "Leaked <b>files</b> &amp; data", "\u2068Leaked files & data\u2069"},
		{"escaped tags stay literal", "&lt;b&gt;visible&lt;/b&gt;", "\u2068<b>visible</b>\u2069"},
		{"encoded masked link", "Click &#91;here&#93;(https://evil.example) now", "\u2068Click \\[here\\](https://evil.example) now\u2069"},
		{"ordinary text", "Plain description", "\u2068Plain description\u2069"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := createRansomwareField("description", api.RansomwareEntry{Description: tc.description}, nil)
			if field == nil {
				t.Fatal("description field unexpectedly omitted")
			}
			if field.Value != tc.want {
				t.Fatalf("field.Value = %q, want %q", field.Value, tc.want)
			}
		})
	}
}

func TestDiscordAPIDescriptionEmptyAfterHTMLStripUsesExistingEmptyFieldPolicy(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	for _, description := range []string{"<b></b>", "\u202e"} {
		entry := api.RansomwareEntry{Description: description}
		formatCfg.ShowEmptyFields = false
		if field := createRansomwareField("description", entry, &formatCfg); field != nil {
			t.Fatalf("description %q with ShowEmptyFields=false yielded field %#v", description, field)
		}

		formatCfg.ShowEmptyFields = true
		formatCfg.EmptyFieldText = "N/A"
		field := createRansomwareField("description", entry, &formatCfg)
		if field == nil || field.Value != "N/A" {
			t.Fatalf("description %q yielded field %#v, want placeholder N/A", description, field)
		}
	}

	formatCfg.EmptyFieldText = "<configured placeholder>"
	field := createRansomwareField("description", api.RansomwareEntry{}, &formatCfg)
	if field == nil || field.Value != "<configured placeholder>" {
		t.Fatalf("operator placeholder changed: %#v", field)
	}
}
