package discord

import (
	"encoding/json"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

// TestExplicitBlackColorSurvivesMarshal is the regression test for the fix to
// encoding/json's "omitempty" on a plain
// int field dropping the color key whenever the resolved color is the Go zero
// value 0 -- which happens exactly when an operator configures an explicit
// "#000000". MessageEmbed.Color is now *int, so a non-nil pointer to 0 still
// marshals.
func TestExplicitBlackColorSurvivesMarshal(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.Discord.RansomwareColor = "#000000"

	embed := formatRansomwareEmbed(api.RansomwareEntry{Group: "LockBit", Victim: "Corp"}, &formatCfg)
	if embed.Color == nil {
		t.Fatal("embed.Color = nil, want a non-nil pointer to 0 for explicit #000000")
	}
	if *embed.Color != 0 {
		t.Fatalf("embed.Color = %#x, want 0 (black)", *embed.Color)
	}

	payload, err := json.Marshal(embed)
	if err != nil {
		t.Fatalf("marshal embed: %v", err)
	}
	if !jsonHasColorKey(t, payload) {
		t.Fatalf("marshalled embed = %s, want a \"color\" key present for explicit black", payload)
	}
}

// TestNonBlackColorUnaffectedByPointerChange is the invariant guard from the
// same finding: every non-black color must marshal byte-identically to
// before the *int change (present, same numeric value).
func TestNonBlackColorUnaffectedByPointerChange(t *testing.T) {
	entry := rss.Entry{Title: "RSS item"}
	embed := formatRSSEmbed(entry, "ransomware", nil)
	if embed.Color == nil || *embed.Color != defaultRansomwareColor {
		t.Fatalf("embed.Color = %#v, want pointer to default ransomware color %#x", embed.Color, defaultRansomwareColor)
	}

	payload, err := json.Marshal(embed)
	if err != nil {
		t.Fatalf("marshal embed: %v", err)
	}
	if !jsonHasColorKey(t, payload) {
		t.Fatalf("marshalled embed = %s, want a \"color\" key present for the default color", payload)
	}
}

func jsonHasColorKey(t *testing.T, payload []byte) bool {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	_, ok := decoded["color"]
	return ok
}
