package slack

import (
	"fmt"
	"strings"

	"Ransomware-Bot/internal/api"
	"Ransomware-Bot/internal/config"
	"Ransomware-Bot/internal/country"
	"Ransomware-Bot/internal/rss"
	"Ransomware-Bot/internal/textutil"
)

// SlackMessage represents a Slack message with Block Kit formatting
type SlackMessage struct {
	Text   string  `json:"text"`            // Fallback text
	Blocks []Block `json:"blocks,omitempty"` // Block Kit blocks
}

// Block represents a Slack Block Kit block
type Block struct {
	Type      string        `json:"type"`
	Text      *TextObject   `json:"text,omitempty"`
	Fields    []TextObject  `json:"fields,omitempty"`
	Elements  []TextObject  `json:"elements,omitempty"` // For context blocks
	Accessory interface{}   `json:"accessory,omitempty"`
}

// TextObject represents a text object in Slack Block Kit
type TextObject struct {
	Type string `json:"type"` // plain_text or mrkdwn
	Text string `json:"text"`
}

// formatRansomwareMessage formats a ransomware entry as a Slack Block Kit message
func formatRansomwareMessage(entry api.RansomwareEntry, formatConfig *config.FormatConfig) SlackMessage {
	// Create fallback text
	fallbackText := fmt.Sprintf("Ransomware Alert: %s -> %s", entry.Group, entry.Victim)

	blocks := []Block{}

	// Get Slack-specific title or use default
	titleText := "Ransomware Alert"
	if formatConfig != nil && formatConfig.Slack.TitleText != "" {
		titleText = formatConfig.Slack.TitleText
	}

	// Header block (without emoji for Slack, capped at 150 chars)
	blocks = append(blocks, Block{
		Type: "header",
		Text: &TextObject{
			Type: "plain_text",
			Text: textutil.TruncateText(titleText, 150),
		},
	})

	// Main section with key information
	fields := []TextObject{}

	// Use Slack-specific field order if available, otherwise use default
	var fieldOrder []string
	if formatConfig != nil {
		fieldOrder = formatConfig.FieldOrder
		if len(formatConfig.Slack.FieldOrder) > 0 {
			fieldOrder = formatConfig.Slack.FieldOrder
		}
	}
	// Use default field order if formatConfig is nil
	if len(fieldOrder) == 0 {
		fieldOrder = []string{"group", "victim", "country", "activity", "attackdate", "discovered", "screenshot", "post_url", "website", "description"}
	}

	// Determine placeholder settings
	showEmpty := formatConfig != nil && formatConfig.ShowEmptyFields
	placeholder := "N/A"
	if formatConfig != nil && formatConfig.EmptyFieldText != "" {
		placeholder = formatConfig.EmptyFieldText
	}

	// Build fields based on format config order
	for _, fieldName := range fieldOrder {
		var fieldText string
		hasValue := false

		switch fieldName {
		case "group":
			if entry.Group != "" {
				fieldText = fmt.Sprintf("*Group:*\n%s", entry.Group)
				hasValue = true
			}
		case "victim":
			if entry.Victim != "" {
				fieldText = fmt.Sprintf("*Victim:*\n%s", entry.Victim)
				hasValue = true
			}
		case "country":
			if entry.Country != "" {
				countryText := entry.Country
				if formatConfig != nil && formatConfig.ShowUnicodeFlags {
					countryText = country.GetCountryFlag(entry.Country) + " " + entry.Country
				}
				fieldText = fmt.Sprintf("*Country:*\n%s", countryText)
				hasValue = true
			}
		case "activity":
			if entry.Activity != "" {
				fieldText = fmt.Sprintf("*Activity:*\n%s", entry.Activity)
				hasValue = true
			}
		case "attackdate":
			if entry.AttackDate != "" {
				fieldText = fmt.Sprintf("*Attack Date:*\n%s", entry.AttackDate)
				hasValue = true
			}
		case "discovered":
			if !entry.Discovered.IsZero() {
				fieldText = fmt.Sprintf("*Discovered:*\n%s", entry.Discovered.Format("2006-01-02 15:04:05"))
				hasValue = true
			}
		}

		// Render placeholder for empty fields when ShowEmptyFields is enabled
		if !hasValue && showEmpty {
			switch fieldName {
			case "group":
				fieldText = fmt.Sprintf("*Group:*\n%s", placeholder)
			case "victim":
				fieldText = fmt.Sprintf("*Victim:*\n%s", placeholder)
			case "country":
				fieldText = fmt.Sprintf("*Country:*\n%s", placeholder)
			case "activity":
				fieldText = fmt.Sprintf("*Activity:*\n%s", placeholder)
			case "attackdate":
				fieldText = fmt.Sprintf("*Attack Date:*\n%s", placeholder)
			case "discovered":
				fieldText = fmt.Sprintf("*Discovered:*\n%s", placeholder)
			}
		}

		if fieldText != "" {
			fields = append(fields, TextObject{
				Type: "mrkdwn",
				Text: fieldText,
			})
		}
	}

	// Add section with fields (Slack limits to 10 fields per section)
	if len(fields) > 0 {
		// Split fields into chunks of max 10 fields
		const maxFieldsPerSection = 10
		for i := 0; i < len(fields); i += maxFieldsPerSection {
			end := i + maxFieldsPerSection
			if end > len(fields) {
				end = len(fields)
			}
			blocks = append(blocks, Block{
				Type:   "section",
				Fields: fields[i:end],
			})
		}
	}

	// Add divider
	blocks = append(blocks, Block{
		Type: "divider",
	})

	// Add URLs section - each URL in its own section with dividers between them
	urlsSectionAdded := false
	renderedURLFields := map[string]bool{} // track rendered URL fields to prevent duplicates
	for _, fieldName := range fieldOrder {
		var urlBlock *Block
		var dedupeKey string

		switch fieldName {
		case "screenshot":
			dedupeKey = "screenshot"
			value := placeholder
			if entry.Screenshot != "" {
				value = entry.Screenshot
			}
			if entry.Screenshot != "" || showEmpty {
				urlBlock = &Block{
					Type: "section",
					Fields: []TextObject{
						{
							Type: "mrkdwn",
							Text: fmt.Sprintf("*Screenshot:*\n%s", value),
						},
					},
				}
			}
		case "post_url", "claim_url":
			dedupeKey = "claim_url"
			value := placeholder
			if entry.ClaimURL != "" {
				value = textutil.DefangURL(entry.ClaimURL)
			}
			if entry.ClaimURL != "" || showEmpty {
				urlBlock = &Block{
					Type: "section",
					Fields: []TextObject{
						{
							Type: "mrkdwn",
							Text: fmt.Sprintf("*Ransom URL:*\n%s", value),
						},
					},
				}
			}
		case "url", "website":
			dedupeKey = "website"
			value := placeholder
			if entry.URL != "" {
				value = entry.URL
			}
			if entry.URL != "" || showEmpty {
				urlBlock = &Block{
					Type: "section",
					Fields: []TextObject{
						{
							Type: "mrkdwn",
							Text: fmt.Sprintf("*Website:*\n%s", value),
						},
					},
				}
			}
		}

		if urlBlock != nil && !renderedURLFields[dedupeKey] {
			if urlsSectionAdded {
				blocks = append(blocks, Block{Type: "divider"})
			}
			blocks = append(blocks, *urlBlock)
			urlsSectionAdded = true
			renderedURLFields[dedupeKey] = true
		}
	}

	// Add description
	for _, fieldName := range fieldOrder {
		if fieldName == "description" {
			value := placeholder
			if entry.Description != "" {
				value = textutil.TruncateText(entry.Description, 500)
			}
			if entry.Description != "" || showEmpty {
				blocks = append(blocks, Block{
					Type: "section",
					Text: &TextObject{
						Type: "mrkdwn",
						Text: fmt.Sprintf("*Description:*\n%s", value),
					},
				})
			}
			break
		}
	}

	// Add context footer
	discoveredTime := "Unknown"
	if !entry.Discovered.IsZero() {
		discoveredTime = entry.Discovered.Format("2006-01-02 15:04:05")
	}
	blocks = append(blocks, Block{
		Type: "context",
		Elements: []TextObject{
			{
				Type: "mrkdwn",
				Text: fmt.Sprintf("Discovered: %s | Source: ransomware.live", discoveredTime),
			},
		},
	})

	// Slack Block Kit allows max 50 blocks per message
	if len(blocks) > 50 {
		blocks = blocks[:50]
	}

	return SlackMessage{
		Text:   fallbackText,
		Blocks: blocks,
	}
}

// formatRSSMessage formats an RSS entry as a Slack Block Kit message
func formatRSSMessage(entry rss.Entry, formatConfig *config.FormatConfig) SlackMessage {
	// Create fallback text
	fallbackText := fmt.Sprintf("RSS Update: %s", entry.Title)

	blocks := []Block{}

	// Get Slack-specific RSS title or use default
	rssText := "RSS Feed Update"
	if formatConfig != nil && formatConfig.Slack.RSSText != "" {
		rssText = formatConfig.Slack.RSSText
	}

	// Header block (without emoji for Slack, capped at 150 chars)
	blocks = append(blocks, Block{
		Type: "header",
		Text: &TextObject{
			Type: "plain_text",
			Text: textutil.TruncateText(rssText, 150),
		},
	})

	// Title section with larger text
	blocks = append(blocks, Block{
		Type: "section",
		Text: &TextObject{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*%s*", entry.Title),
		},
	})

	// Add divider after title
	blocks = append(blocks, Block{
		Type: "divider",
	})

	// Description if present
	if entry.Description != "" {
		cleanDescription := textutil.StripHTML(entry.Description)
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: textutil.TruncateText(cleanDescription, 500),
			},
		})
	}

	// Link section (prominent)
	if entry.Link != "" {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Link:*\n%s", entry.Link),
			},
		})
	}

	// Metadata section
	// Author section
	if entry.Author != "" {
		blocks = append(blocks, Block{
			Type: "section",
			Fields: []TextObject{
				{
					Type: "mrkdwn",
					Text: fmt.Sprintf("*Author:*\n%s", entry.Author),
				},
			},
		})
	}

	// Divider between Author and Categories
	if entry.Author != "" && len(entry.Categories) > 0 {
		blocks = append(blocks, Block{
			Type: "divider",
		})
	}

	// Categories section
	if len(entry.Categories) > 0 {
		blocks = append(blocks, Block{
			Type: "section",
			Fields: []TextObject{
				{
					Type: "mrkdwn",
					Text: fmt.Sprintf("*Categories:*\n%s", strings.Join(entry.Categories, ", ")),
				},
			},
		})
	}

	// Context footer
	publishedTime := "Unknown"
	if !entry.Published.IsZero() {
		publishedTime = entry.Published.Format("2006-01-02 15:04:05")
	}

	feedInfo := entry.FeedTitle
	if feedInfo == "" {
		feedInfo = "RSS Feed"
	}

	blocks = append(blocks, Block{
		Type: "context",
		Elements: []TextObject{
			{
				Type: "mrkdwn",
				Text: fmt.Sprintf("Published: %s | Source: %s", publishedTime, feedInfo),
			},
		},
	})

	// Slack Block Kit allows max 50 blocks per message
	if len(blocks) > 50 {
		blocks = blocks[:50]
	}

	return SlackMessage{
		Text:   fallbackText,
		Blocks: blocks,
	}
}

