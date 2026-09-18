package discord

// WebhookParams is the subset of Discord webhook payload fields used by this
// bot. Keeping it local avoids pulling in the full Discord client library for
// simple webhook POSTs.
type WebhookParams struct {
	Content string          `json:"content,omitempty"`
	Embeds  []*MessageEmbed `json:"embeds,omitempty"`
}

type MessageEmbed struct {
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
	// Color is a pointer so that encoding/json's "omitempty" keys off
	// pointer-nilness instead of the Go zero value: a plain int field would
	// drop the "color" key whenever the resolved color happens to be 0, which
	// is exactly what an explicit "#000000" configuration parses to. See
	// discordColorPointer in formatter.go.
	Color  *int                 `json:"color,omitempty"`
	Footer *MessageEmbedFooter  `json:"footer,omitempty"`
	Author *MessageEmbedAuthor  `json:"author,omitempty"`
	Fields []*MessageEmbedField `json:"fields,omitempty"`
}

type MessageEmbedFooter struct {
	Text string `json:"text,omitempty"`
}

type MessageEmbedAuthor struct {
	Name string `json:"name,omitempty"`
}

type MessageEmbedField struct {
	Name   string `json:"name,omitempty"`
	Value  string `json:"value,omitempty"`
	Inline bool   `json:"inline,omitempty"`
}
