package notifyfmt

import (
	"strings"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"
)

const defaultRSSCategoryLimit = 5

type RSSViewOptions struct {
	TitleFallback    string
	TimestampFormat  string
	DisplayTimezone  string
	CategoryLimit    int
	CategoryMaxChars int
}

type RSSView struct {
	Title         string
	Description   string
	Link          string
	Author        string
	Categories    []string
	CategoryText  string
	Published     time.Time
	PublishedText string
	FeedTitle     string
	FeedURL       string
	SourceText    string
}

func BuildRSSView(entry model.RSSEntry, options RSSViewOptions) RSSView {
	categoryLimit := options.CategoryLimit
	if categoryLimit <= 0 {
		categoryLimit = defaultRSSCategoryLimit
	}

	view := RSSView{
		Title:       rssDisplayTitle(entry, options.TitleFallback),
		Description: entry.Description,
		Link:        entry.Link,
		Author:      entry.Author,
		Categories:  append([]string(nil), entry.Categories...),
		Published:   entry.Published,
		FeedTitle:   entry.FeedTitle,
		FeedURL:     entry.FeedURL,
		SourceText:  rssSourceText(entry.FeedTitle),
	}
	if options.CategoryMaxChars > 0 {
		view.CategoryText = textutil.FormatCategorySummary(entry.Categories, categoryLimit, options.CategoryMaxChars)
	}
	if !entry.Published.IsZero() {
		view.PublishedText = timeutil.FormatDisplayTime(entry.Published, options.TimestampFormat, options.DisplayTimezone)
	}
	return view
}

func rssDisplayTitle(entry model.RSSEntry, fallback string) string {
	for _, candidate := range []string{entry.Title, entry.FeedTitle, entry.Link, fallback, "Untitled RSS item"} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return "Untitled RSS item"
}

func rssSourceText(feedTitle string) string {
	if strings.TrimSpace(feedTitle) == "" {
		return "RSS Feed"
	}
	return feedTitle
}
