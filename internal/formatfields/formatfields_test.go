package formatfields

import (
	"reflect"
	"testing"
)

func TestNormalizeAliases(t *testing.T) {
	tests := map[string]string{
		"attack_date": FieldAttackDate,
		"ATTACKDATE":  FieldAttackDate,
		" post_url ":  FieldPostURL,
		"CLAIM_URL":   FieldPostURL,
		"website":     FieldWebsite,
		" URL ":       FieldWebsite,
		"published":   FieldPublished,
	}

	for input, want := range tests {
		got, ok := Normalize(input)
		if !ok {
			t.Fatalf("Normalize(%q) ok = false", input)
		}
		if got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeRejectsUnknownField(t *testing.T) {
	if got, ok := Normalize("unknown"); ok {
		t.Fatalf("Normalize() = %q, true; want false", got)
	}
}

func TestDefaultOrdersAreCopied(t *testing.T) {
	discord := DefaultDiscordFieldOrder()
	slack := DefaultSlackFieldOrder()
	rss := DefaultRSSFieldOrder()

	if !reflect.DeepEqual(discord, []string{FieldGroup, FieldVictim, FieldCountry, FieldActivity, FieldDiscovered, FieldPostURL}) {
		t.Fatalf("DefaultDiscordFieldOrder() = %#v", discord)
	}
	if !reflect.DeepEqual(slack, []string{FieldGroup, FieldVictim, FieldCountry, FieldActivity, FieldDiscovered, FieldPostURL}) {
		t.Fatalf("DefaultSlackFieldOrder() = %#v", slack)
	}
	if !reflect.DeepEqual(rss, []string{RSSFieldTitle, RSSFieldDescription, RSSFieldLink, RSSFieldAuthor, RSSFieldCategories, RSSFieldPublished, RSSFieldFeedTitle}) {
		t.Fatalf("DefaultRSSFieldOrder() = %#v", rss)
	}

	discord[0] = "changed"
	slack[0] = "changed"
	rss[0] = "changed"
	if DefaultDiscordFieldOrder()[0] != FieldGroup {
		t.Fatal("DefaultDiscordFieldOrder returned shared backing array")
	}
	if DefaultSlackFieldOrder()[0] != FieldGroup {
		t.Fatal("DefaultSlackFieldOrder returned shared backing array")
	}
	if DefaultRSSFieldOrder()[0] != RSSFieldTitle {
		t.Fatal("DefaultRSSFieldOrder returned shared backing array")
	}
}

func TestIsActionLink(t *testing.T) {
	for _, field := range []string{FieldPostURL, FieldWebsite, FieldScreenshot} {
		if !IsActionLink(field) {
			t.Fatalf("IsActionLink(%q) = false", field)
		}
	}
	if IsActionLink(FieldVictim) {
		t.Fatal("IsActionLink(victim) = true")
	}
}

func TestNormalizeRSSFields(t *testing.T) {
	tests := map[string]string{
		" title ":      RSSFieldTitle, //nolint:gocritic // mapKey: whitespace is the point, testing normalization of a space-padded field name
		"LINK":         RSSFieldLink,
		"source":       RSSFieldFeedTitle,
		"feed_title":   RSSFieldFeedTitle,
		"feed_url":     RSSFieldFeedURL,
		"published_at": RSSFieldPublished,
	}

	for input, want := range tests {
		got, ok := NormalizeRSS(input)
		if !ok {
			t.Fatalf("NormalizeRSS(%q) ok = false", input)
		}
		if got != want {
			t.Fatalf("NormalizeRSS(%q) = %q, want %q", input, got, want)
		}
	}

	if got, ok := NormalizeRSS("victim"); ok {
		t.Fatalf("NormalizeRSS(victim) = %q, true; want false", got)
	}
}

func TestNormalizeRSSRemainingAliases(t *testing.T) {
	tests := map[string]string{
		"description": RSSFieldDescription,
		"AUTHOR":      RSSFieldAuthor,
		"categories":  RSSFieldCategories,
		" Category ":  RSSFieldCategories, //nolint:gocritic // mapKey: whitespace is the point, testing normalization of a space-padded field name
		"published":   RSSFieldPublished,
	}

	for input, want := range tests {
		got, ok := NormalizeRSS(input)
		if !ok {
			t.Fatalf("NormalizeRSS(%q) ok = false", input)
		}
		if got != want {
			t.Fatalf("NormalizeRSS(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRSSFieldLabel(t *testing.T) {
	tests := map[string]string{
		RSSFieldTitle:       "Title",
		RSSFieldDescription: "Description",
		RSSFieldLink:        "Link",
		RSSFieldAuthor:      "Author",
		RSSFieldCategories:  "Categories",
		RSSFieldPublished:   "Published",
		RSSFieldFeedTitle:   "Source",
		RSSFieldFeedURL:     "Feed URL",
		"custom_field":      "custom_field",
	}

	for input, want := range tests {
		if got := RSSFieldLabel(input); got != want {
			t.Fatalf("RSSFieldLabel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsValid(t *testing.T) {
	if !IsValid(" CLAIM_URL ") {
		t.Fatal("IsValid( CLAIM_URL ) = false, want true for known alias")
	}
	if IsValid("unknown") {
		t.Fatal("IsValid(unknown) = true, want false")
	}
}
