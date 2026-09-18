package discordurl

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	Host       = "discord.com"
	LegacyHost = "discordapp.com"
)

type Parts struct {
	ID    string
	Token string
}

func Parse(rawURL string) (Parts, error) {
	if strings.TrimSpace(rawURL) != rawURL {
		return Parts{}, fmt.Errorf("webhook URL must not contain surrounding whitespace")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Parts{}, fmt.Errorf("webhook URL is malformed: %w", err)
	}
	if parsed.Scheme != "https" {
		return Parts{}, fmt.Errorf("webhook URL must use https")
	}

	host := strings.ToLower(parsed.Hostname())
	if host != Host && host != LegacyHost {
		return Parts{}, fmt.Errorf("webhook URL must use %s", Host)
	}

	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 4 || segments[0] != "api" || segments[1] != "webhooks" {
		return Parts{}, fmt.Errorf("webhook URL path must be /api/webhooks/{id}/{token}")
	}

	parts := Parts{
		ID:    segments[2],
		Token: segments[3],
	}
	if parts.ID == "" || parts.Token == "" {
		return Parts{}, fmt.Errorf("webhook ID or token is empty")
	}
	return parts, nil
}

func Validate(rawURL string) error {
	_, err := Parse(rawURL)
	return err
}
