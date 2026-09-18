package i18n

import (
	"strings"
	"testing"
)

func TestFromArgsAndEnvPrefersLocaleFlag(t *testing.T) {
	t.Setenv(LocaleEnv, "en")

	msg := FromArgsAndEnv([]string{"--locale", "de"})

	if msg.Locale() != "de" {
		t.Fatalf("Locale() = %q, want de", msg.Locale())
	}
	if got := msg.T("cli.config_valid"); got != "Konfiguration gueltig" {
		t.Fatalf("German message = %q", got)
	}
}

func TestFromArgsAndEnvUsesBotLocale(t *testing.T) {
	t.Setenv(LocaleEnv, "de-DE")

	msg := FromArgsAndEnv(nil)

	if msg.Locale() != "de" {
		t.Fatalf("Locale() = %q, want de", msg.Locale())
	}
}

func TestUnsupportedLocaleFallsBackToEnglish(t *testing.T) {
	msg := ForLocale("fr")

	if msg.Locale() != "en" {
		t.Fatalf("Locale() = %q, want en fallback", msg.Locale())
	}
	if got := msg.T("cli.config_valid"); got != "Configuration valid" {
		t.Fatalf("fallback message = %q", got)
	}
}

func TestDefaultUsesEnglish(t *testing.T) {
	msg := Default()

	if msg.Locale() != "en" {
		t.Fatalf("Locale() = %q, want en", msg.Locale())
	}
	if got := msg.T("cli.config_valid"); got != "Configuration valid" {
		t.Fatalf("default message = %q", got)
	}
}

func TestTReturnsKeyForUnknownMessage(t *testing.T) {
	for _, locale := range []string{"en", "de"} {
		if got := ForLocale(locale).T("does.not.exist"); got != "does.not.exist" {
			t.Fatalf("T(unknown key, %q) = %q, want key echoed back", locale, got)
		}
	}
}

func TestTfFormatsArguments(t *testing.T) {
	got := Default().Tf("cli.version", "1.2.3", "abcdef", "2026-01-02")

	want := "Ransomware News Bot v1.2.3 (commit abcdef, built 2026-01-02)"
	if got != want {
		t.Fatalf("Tf() = %q, want %q", got, want)
	}
}

func TestFromArgsAndEnvUsesLegacyEnvFallback(t *testing.T) {
	t.Setenv(LocaleEnv, "")
	t.Setenv(LegacyLocaleEnv, "de")

	msg := FromArgsAndEnv(nil)

	if msg.Locale() != "de" {
		t.Fatalf("Locale() = %q, want de from legacy env", msg.Locale())
	}
}

func TestLocaleFlagWithoutValueFallsBackToEnv(t *testing.T) {
	t.Setenv(LocaleEnv, "de")
	t.Setenv(LegacyLocaleEnv, "")

	msg := FromArgsAndEnv([]string{"--locale"})

	if msg.Locale() != "de" {
		t.Fatalf("Locale() = %q, want de fallback from env", msg.Locale())
	}
}

func TestLocaleFlagEqualsSyntax(t *testing.T) {
	t.Setenv(LocaleEnv, "")
	t.Setenv(LegacyLocaleEnv, "")

	msg := FromArgsAndEnv([]string{"--verbose", "--locale=de-CH"})

	if msg.Locale() != "de" {
		t.Fatalf("Locale() = %q, want de from --locale= flag", msg.Locale())
	}
}

func TestForLocaleNormalizesVariants(t *testing.T) {
	tests := []struct {
		locale string
		want   string
	}{
		{locale: "", want: "en"},
		{locale: "   ", want: "en"},
		{locale: "DE", want: "de"},
		{locale: "de_AT", want: "de"},
		{locale: "de-de", want: "de"},
		{locale: "fr-FR", want: "en"},
	}

	for _, tt := range tests {
		t.Run(tt.locale, func(t *testing.T) {
			if got := ForLocale(tt.locale).Locale(); got != tt.want {
				t.Fatalf("ForLocale(%q).Locale() = %q, want %q", tt.locale, got, tt.want)
			}
		})
	}
}

func TestGermanCatalogCoversEveryEnglishKey(t *testing.T) {
	for key := range englishMessages {
		if _, ok := germanMessages[key]; !ok {
			t.Errorf("germanMessages is missing key %q", key)
		}
	}
	for key := range germanMessages {
		if _, ok := englishMessages[key]; !ok {
			t.Errorf("englishMessages is missing key %q", key)
		}
	}
}

// catalogFormatVerbs returns the ordered sequence of fmt verbs in value,
// ignoring escaped percent signs.
func catalogFormatVerbs(value string) []string {
	verbs := []string{}
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			continue
		}
		if i+1 >= len(value) {
			verbs = append(verbs, "%")
			break
		}
		i++
		if value[i] == '%' {
			continue
		}
		start := i
		for i < len(value) && strings.ContainsRune("+-# 0123456789.", rune(value[i])) {
			i++
		}
		if i >= len(value) {
			verbs = append(verbs, "%"+value[start:])
			break
		}
		verbs = append(verbs, "%"+value[start:i+1])
	}
	return verbs
}

func TestCatalogFormatVerbsMatchPerKey(t *testing.T) {
	for key, english := range englishMessages {
		german, ok := germanMessages[key]
		if !ok {
			continue
		}
		englishVerbs := catalogFormatVerbs(english)
		germanVerbs := catalogFormatVerbs(german)
		if len(englishVerbs) != len(germanVerbs) {
			t.Errorf("key %q: verbs en=%v de=%v", key, englishVerbs, germanVerbs)
			continue
		}
		for i := range englishVerbs {
			if englishVerbs[i] != germanVerbs[i] {
				t.Errorf("key %q: verb %d en=%q de=%q", key, i, englishVerbs[i], germanVerbs[i])
			}
		}
	}
}
