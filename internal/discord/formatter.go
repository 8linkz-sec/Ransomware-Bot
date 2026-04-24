package discord

import (
	"fmt"
	"strings"
	"time"

	"Ransomware-Bot/internal/api"
	"Ransomware-Bot/internal/config"
	"Ransomware-Bot/internal/country"
	"Ransomware-Bot/internal/rss"
	"Ransomware-Bot/internal/textutil"

	"github.com/bwmarrin/discordgo"
)

// formatTimestamp formats a timestamp string consistently
// Handles multiple input formats and normalizes to "2006-01-02 15:04:05"
func formatTimestamp(timestamp string) string {
	if timestamp == "" {
		return ""
	}

	// Try parsing with microseconds
	if t, err := time.Parse("2006-01-02 15:04:05.999999", timestamp); err == nil {
		return t.Format("2006-01-02 15:04:05")
	}

	// Try parsing without microseconds
	if t, err := time.Parse("2006-01-02 15:04:05", timestamp); err == nil {
		return t.Format("2006-01-02 15:04:05")
	}

	// Try parsing ISO 8601 format
	if t, err := time.Parse(time.RFC3339, timestamp); err == nil {
		return t.Format("2006-01-02 15:04:05")
	}

	// Fallback: return as-is if parsing fails
	return timestamp
}

const (
	// Color constants for Discord embeds
	ColorRansomware = 0xff0000 // Red for ransomware alerts
	ColorRSS        = 0x0099ff // Blue for RSS feeds
	ColorGovernment = 0xffa500 // Orange for government feeds
)

// formatRansomwareEmbed creates a Discord embed for a ransomware entry
func (w *WebhookSender) formatRansomwareEmbed(entry api.RansomwareEntry, formatConfig *config.FormatConfig) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:     textutil.TruncateText(fmt.Sprintf("🚨 Ransomware Alert: %s", entry.Group), 256),
		Color:     ColorRansomware,
		Timestamp: entry.Discovered.Format(time.RFC3339),
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Ransomware.live API",
		},
	}

	// Add fields based on the format configuration
	for _, fieldName := range formatConfig.FieldOrder {
		// Add spacing before description
		if fieldName == "description" {
			// Add empty field for spacing
			spacer := &discordgo.MessageEmbedField{
				Name:   "\u200B", // Invisible character
				Value:  "\u200B", // Invisible character
				Inline: false,
			}
			embed.Fields = append(embed.Fields, spacer)
		}
		// Create the field for the current entry
		field := w.createRansomwareField(fieldName, entry, formatConfig)
		if field != nil {
			embed.Fields = append(embed.Fields, field)
		}
	}

	return embed
}

// createRansomwareField creates a Discord embed field for a specific ransomware entry field.
// When ShowEmptyFields is enabled, missing values are rendered with EmptyFieldText as placeholder.
func (w *WebhookSender) createRansomwareField(fieldName string, entry api.RansomwareEntry, formatConfig *config.FormatConfig) *discordgo.MessageEmbedField {
	showFlags := formatConfig.ShowUnicodeFlags
	placeholder := formatConfig.EmptyFieldText
	showEmpty := formatConfig.ShowEmptyFields

	switch strings.ToLower(fieldName) {
	case "country":
		value := placeholder
		if entry.Country != "" {
			value = country.FormatCountryDisplay(entry.Country, showFlags)
		}
		if value != placeholder || showEmpty {
			return &discordgo.MessageEmbedField{
				Name:   "🌍 Country",
				Value:  value,
				Inline: true,
			}
		}

	case "victim":
		value := placeholder
		if entry.Victim != "" {
			value = entry.Victim
		}
		if value != placeholder || showEmpty {
			return &discordgo.MessageEmbedField{
				Name:   "🎯 Victim",
				Value:  value,
				Inline: true,
			}
		}

	case "group":
		value := placeholder
		if entry.Group != "" {
			value = entry.Group
		}
		if value != placeholder || showEmpty {
			return &discordgo.MessageEmbedField{
				Name:   "💀 Group",
				Value:  value,
				Inline: true,
			}
		}

	case "activity":
		value := placeholder
		if entry.Activity != "" {
			value = entry.Activity
		}
		if value != placeholder || showEmpty {
			return &discordgo.MessageEmbedField{
				Name:   "📊 Activity",
				Value:  value,
				Inline: true,
			}
		}

	case "attackdate":
		value := placeholder
		if entry.AttackDate != "" {
			value = formatTimestamp(entry.AttackDate)
		}
		if value != placeholder || showEmpty {
			return &discordgo.MessageEmbedField{
				Name:   "⚔️ Attack Date",
				Value:  value,
				Inline: true,
			}
		}

	case "discovered":
		// Discovered always has a value (timestamp)
		formattedDate := entry.Discovered.Format("2006-01-02 15:04:05")
		return &discordgo.MessageEmbedField{
			Name:   "🔍 Discovered",
			Value:  formattedDate,
			Inline: true,
		}

	case "post_url", "claim_url":
		value := placeholder
		if entry.ClaimURL != "" {
			value = textutil.TruncateText(textutil.DefangURL(entry.ClaimURL), 1024)
		}
		if value != placeholder || showEmpty {
			return &discordgo.MessageEmbedField{
				Name:   "🔗 Ransom URL",
				Value:  value,
				Inline: false,
			}
		}

	case "website", "url":
		value := placeholder
		if entry.URL != "" {
			value = textutil.TruncateText(entry.URL, 1024)
		}
		if value != placeholder || showEmpty {
			return &discordgo.MessageEmbedField{
				Name:   "🌐 Website",
				Value:  value,
				Inline: false,
			}
		}

	case "description":
		value := placeholder
		if entry.Description != "" {
			value = textutil.TruncateText(entry.Description, 500)
		}
		if value != placeholder || showEmpty {
			return &discordgo.MessageEmbedField{
				Name:   "📝 Description",
				Value:  value,
				Inline: false,
			}
		}

	case "screenshot":
		value := placeholder
		if entry.Screenshot != "" {
			value = textutil.TruncateText(entry.Screenshot, 1024)
		}
		if value != placeholder || showEmpty {
			return &discordgo.MessageEmbedField{
				Name:   "📸 Screenshot",
				Value:  value,
				Inline: false,
			}
		}
	}

	return nil
}

// formatRSSEmbed creates a Discord embed for an RSS entry
func (w *WebhookSender) formatRSSEmbed(entry rss.Entry) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:       textutil.TruncateText(entry.Title, 256),
		Description: w.truncateDescription(entry.Description, 2048),
		Color:       ColorRSS,
		Timestamp:   entry.Published.Format(time.RFC3339),
		Footer: &discordgo.MessageEmbedFooter{
			Text: entry.FeedTitle,
		},
	}

	// Add URL if available
	if entry.Link != "" {
		embed.URL = entry.Link
	}

	// Add author if available
	if entry.Author != "" {
		embed.Author = &discordgo.MessageEmbedAuthor{
			Name: entry.Author,
		}
	}

	// Add categories as a field if available
	if len(entry.Categories) > 0 {
		categoriesStr := strings.Join(entry.Categories, ", ")
		if len(categoriesStr) <= 1024 {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "📂 Categories",
				Value:  categoriesStr,
				Inline: false,
			})
		}
	}

	// Add publication date field
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "📅 Published",
		Value:  entry.Published.Format("2006-01-02 15:04:05"),
		Inline: true,
	})

	return embed
}

// truncateDescription truncates a description to fit Discord's limits (Unicode-safe)
func (w *WebhookSender) truncateDescription(description string, maxLength int) string {
	runes := []rune(description)
	if len(runes) <= maxLength {
		return description
	}

	// Find a good place to cut off (preferably at a sentence boundary)
	truncated := runes[:maxLength-3]

	// Look for the last period, exclamation mark, or question mark
	lastSentenceEnd := -1
	for i := len(truncated) - 1; i >= 0; i-- {
		if truncated[i] == '.' || truncated[i] == '!' || truncated[i] == '?' {
			lastSentenceEnd = i
			break
		}
	}

	// If we found a sentence boundary and it's not too early, use it
	if lastSentenceEnd > maxLength/2 {
		return string(truncated[:lastSentenceEnd+1])
	}

	// Otherwise, just truncate and add ellipsis
	return string(truncated) + "..."
}

