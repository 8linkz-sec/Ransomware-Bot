package keywordmode

import (
	"fmt"
	"strings"
)

const (
	Literal = "literal"
	Regex   = "regex"
)

func Normalize(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", Literal:
		return Literal, nil
	case Regex:
		return Regex, nil
	default:
		return "", fmt.Errorf("invalid keyword_match mode %q (must be %q or %q)", mode, Literal, Regex)
	}
}

func IsRegex(mode string) bool {
	normalized, err := Normalize(mode)
	return err == nil && normalized == Regex
}
