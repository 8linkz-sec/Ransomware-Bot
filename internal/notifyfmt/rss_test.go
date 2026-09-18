package notifyfmt

import (
	"testing"
	"time"

	// Embed tzdata so timezone-dependent assertions (e.g. Europe/Berlin) do not
	// rely on the host's timezone database.
	_ "time/tzdata"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

func TestBuildRSSViewPreparesSharedFields(t *testing.T) {
	published := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entry := rss.Entry{
		Title:       "  Incident Report  ",
		Description: "Already normalized <indicator> & text",
		Link:        "https://example.test/article",
		Author:      "Reporter",
		Categories:  []string{" malware ", "ransomware", "", "patch"},
		Published:   published,
		FeedTitle:   "Vendor Feed",
		FeedURL:     "https://example.test/feed.xml",
	}

	view := BuildRSSView(entry, RSSViewOptions{
		TitleFallback:    "Fallback",
		TimestampFormat:  "02.01.2006 15:04 MST",
		DisplayTimezone:  "Europe/Berlin",
		CategoryLimit:    2,
		CategoryMaxChars: 80,
	})

	if view.Title != "Incident Report" {
		t.Fatalf("Title = %q, want trimmed title", view.Title)
	}
	if view.Description != entry.Description {
		t.Fatalf("Description = %q, want unchanged normalized text", view.Description)
	}
	if view.CategoryText != "malware, ransomware (+1 more)" {
		t.Fatalf("CategoryText = %q, want summarized categories", view.CategoryText)
	}
	if view.PublishedText != "02.01.2026 04:04 CET" {
		t.Fatalf("PublishedText = %q, want localized display time", view.PublishedText)
	}
	if view.SourceText != "Vendor Feed" {
		t.Fatalf("SourceText = %q, want feed title", view.SourceText)
	}
	entry.Categories[0] = "mutated"
	if view.Categories[0] != " malware " {
		t.Fatalf("Categories shared backing storage: %#v", view.Categories)
	}
}

func TestBuildRSSViewUsesFallbackSource(t *testing.T) {
	view := BuildRSSView(rss.Entry{}, RSSViewOptions{TitleFallback: "Fallback title"})

	if view.Title != "Fallback title" {
		t.Fatalf("Title = %q, want fallback", view.Title)
	}
	if view.SourceText != "RSS Feed" {
		t.Fatalf("SourceText = %q, want RSS Feed fallback", view.SourceText)
	}
}

func TestBuildRSSViewTitlePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		entry    rss.Entry
		fallback string
		want     string
	}{
		{
			name:     "entry title wins",
			entry:    rss.Entry{Title: "Entry", FeedTitle: "Feed", Link: "https://example.test/a"},
			fallback: "Fallback",
			want:     "Entry",
		},
		{
			name:     "feed title before link",
			entry:    rss.Entry{FeedTitle: "Feed", Link: "https://example.test/a"},
			fallback: "Fallback",
			want:     "Feed",
		},
		{
			name:     "link before fallback",
			entry:    rss.Entry{Link: "https://example.test/a"},
			fallback: "Fallback",
			want:     "https://example.test/a",
		},
		{
			name:  "untitled placeholder when everything is blank",
			entry: rss.Entry{Title: "  ", FeedTitle: " ", Link: ""},
			want:  "Untitled RSS item",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := BuildRSSView(tt.entry, RSSViewOptions{TitleFallback: tt.fallback})
			if view.Title != tt.want {
				t.Fatalf("Title = %q, want %q", view.Title, tt.want)
			}
		})
	}
}

func TestBuildRSSViewWithoutCategoryMaxCharsSkipsCategoryText(t *testing.T) {
	view := BuildRSSView(rss.Entry{Categories: []string{"malware", "ransomware"}}, RSSViewOptions{})

	if view.CategoryText != "" {
		t.Fatalf("CategoryText = %q, want empty when CategoryMaxChars is unset", view.CategoryText)
	}
	if view.PublishedText != "" {
		t.Fatalf("PublishedText = %q, want empty for zero Published time", view.PublishedText)
	}
}
