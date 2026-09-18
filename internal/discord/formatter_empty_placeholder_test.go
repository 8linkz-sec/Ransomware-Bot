package discord

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
)

// Regression test for createRansomwareField: unlike formatRSSEmbed
// (L284-287) and slack's slackEmptyFieldPlaceholder, which both substitute
// "N/A" when formatConfig.EmptyFieldText is the empty string,
// createRansomwareField used to marshal formatConfig.EmptyFieldText RAW. A
// config of {"show_empty_fields": true, "empty_field_text": ""} loads without
// complaint (validateFormatConfig has no empty_field_text check) and then
// every missing ransomware field marshalled as {"name":"...","inline":true}
// with NO "value" key at all (MessageEmbedField.Value is
// `json:"value,omitempty"`), because Discord requires embed.fields[].value
// to be 1-1024 chars, and drops the whole webhook (400) -- the alert was
// never delivered.

func TestFormatRansomwareEmbedEmptyPlaceholderFallsBackToNA(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder:      []string{"group", "victim", "country", "activity", "discovered", "post_url"},
		ShowEmptyFields: true,
		EmptyFieldText:  "",
	}
	entry := model.RansomwareEntry{Group: "LockBit"} // every other field left empty

	embed := formatRansomwareEmbed(entry, formatCfg)

	payload, err := json.Marshal(embed)
	if err != nil {
		t.Fatalf("json.Marshal(embed) failed: %v", err)
	}
	t.Logf("marshalled embed: %s", payload)

	if len(embed.Fields) != 6 {
		t.Fatalf("field count = %d, want 6", len(embed.Fields))
	}
	missingValueKey := 0
	for _, field := range embed.Fields {
		fieldPayload, err := json.Marshal(field)
		if err != nil {
			t.Fatalf("json.Marshal(field %q) failed: %v", field.Name, err)
		}
		if !strings.Contains(string(fieldPayload), `"value"`) {
			missingValueKey++
			t.Logf("field %q marshals with no value key: %s", field.Name, fieldPayload)
		}
		if field.Value == "" {
			t.Errorf("field %q has empty Value, want a non-empty placeholder fallback (\"N/A\")", field.Name)
		}
	}
	if missingValueKey != 0 {
		t.Fatalf("%d of %d fields marshalled with no \"value\" key -- Discord rejects the whole webhook (400) when this happens", missingValueKey, len(embed.Fields))
	}
}

// Confirms the fallback is invisible for every configured placeholder that is
// not the empty string, including a single space -- only "" changes behavior.
func TestCreateRansomwareFieldNonEmptyPlaceholderByteIdentical(t *testing.T) {
	placeholders := []string{"N/A", "No data", "-", " ", "[none]"}
	entry := model.RansomwareEntry{} // Victim left empty -> placeholder path

	for _, placeholder := range placeholders {
		t.Run(placeholder, func(t *testing.T) {
			formatCfg := &notifyfmt.FormatOptions{
				ShowEmptyFields: true,
				EmptyFieldText:  placeholder,
			}
			field := createRansomwareField("victim", entry, formatCfg)
			if field == nil {
				t.Fatal("createRansomwareField returned nil, want a placeholder field")
			}
			if field.Value != placeholder {
				t.Fatalf("field.Value = %q, want unchanged placeholder %q", field.Value, placeholder)
			}
		})
	}
}
