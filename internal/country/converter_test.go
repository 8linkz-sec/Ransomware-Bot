package country

import (
	"testing"
)

func TestConvertCountryKnown(t *testing.T) {
	tests := []struct {
		input        string
		expectedName string
	}{
		{"US", "United States"},
		{"us", "United States"},
		{"  DE  ", "Germany"},
		{"GB", "United Kingdom"},
		{"FR", "France"},
		{"JP", "Japan"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ConvertCountry(tt.input)
			if result.Name != tt.expectedName {
				t.Errorf("Name = %q, want %q", result.Name, tt.expectedName)
			}
			if result.Flag == "" || result.Flag == "\U0001F310" {
				t.Errorf("expected specific flag emoji, got %q", result.Flag)
			}
		})
	}
}

func TestConvertCountryUnknown(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"single char", "A"},
		{"three chars", "ABC"},
		{"unknown code", "XX"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertCountry(tt.input)
			if result.Name != "Unknown" {
				t.Errorf("Name = %q, want Unknown", result.Name)
			}
		})
	}
}

func TestConvertCountryRejectsMalformedTwoCharCodes(t *testing.T) {
	// Two characters long, but not a well-formed ISO region, so
	// language.ParseRegion fails and the converter falls back to Unknown.
	tests := []string{"1A", "a1", "1!", "??"}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			result := ConvertCountry(input)
			if result.Name != "Unknown" {
				t.Errorf("Name = %q, want Unknown", result.Name)
			}
			if result.Flag != "\U0001F310" {
				t.Errorf("Flag = %q, want globe emoji", result.Flag)
			}
		})
	}
}

func TestParseISORegionMalformed(t *testing.T) {
	if _, ok := parseISORegion("1A"); ok {
		t.Fatal("parseISORegion(1A) ok = true, want false")
	}
	if _, ok := parseISORegion("USA"); ok {
		t.Fatal("parseISORegion(USA) ok = true, want false")
	}
	if region, ok := parseISORegion("US"); !ok || region.String() != "US" {
		t.Fatalf("parseISORegion(US) = %v, %v, want US, true", region, ok)
	}
}

func TestConvertCountryUsesCLDRRegionsOutsideLegacyMap(t *testing.T) {
	result := ConvertCountry("AQ")
	if result.Name != "Antarctica" {
		t.Fatalf("Name = %q, want Antarctica", result.Name)
	}
	if result.Flag != "\U0001F1E6\U0001F1F6" {
		t.Fatalf("Flag = %q, want Antarctica flag", result.Flag)
	}
}

func TestConvertCountryForLocale(t *testing.T) {
	result := ConvertCountryForLocale("DE", "de")
	if result.Name != "Deutschland" {
		t.Fatalf("Name = %q, want Deutschland", result.Name)
	}
	if result.Flag != "\U0001F1E9\U0001F1EA" {
		t.Fatalf("Flag = %q, want German flag", result.Flag)
	}
}

func TestGetCountryFlag(t *testing.T) {
	flag := GetCountryFlag("US")
	if flag == "" || flag == "\U0001F310" {
		t.Errorf("expected US flag emoji, got %q", flag)
	}

	flag = GetCountryFlag("")
	if flag != "\U0001F310" {
		t.Errorf("expected globe emoji for empty input, got %q", flag)
	}
}

func TestGenerateFlagEmoji(t *testing.T) {
	tests := []struct {
		code     string
		expected string
	}{
		{"US", "\U0001F1FA\U0001F1F8"},
		{"GB", "\U0001F1EC\U0001F1E7"},
		{"DE", "\U0001F1E9\U0001F1EA"},
		{"A", "\U0001F310"},   // wrong length
		{"", "\U0001F310"},    // empty
		{"ABC", "\U0001F310"}, // too long
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			result := generateFlagEmoji(tt.code)
			if result != tt.expected {
				t.Errorf("generateFlagEmoji(%q) = %q, want %q", tt.code, result, tt.expected)
			}
		})
	}
}

func TestFormatCountryDisplay(t *testing.T) {
	// With flag
	result := FormatCountryDisplay("US", true)
	expected := "\U0001F1FA\U0001F1F8 United States"
	if result != expected {
		t.Errorf("FormatCountryDisplay(US, true) = %q, want %q", result, expected)
	}

	// Without flag
	result = FormatCountryDisplay("US", false)
	if result != "United States" {
		t.Errorf("FormatCountryDisplay(US, false) = %q, want %q", result, "United States")
	}

	// Unknown country
	result = FormatCountryDisplay("XX", true)
	if result != "\U0001F310 Unknown" {
		t.Errorf("FormatCountryDisplay(XX, true) = %q, want %q", result, "\U0001F310 Unknown")
	}
}

func TestFormatCountryDisplayForLocale(t *testing.T) {
	result := FormatCountryDisplayForLocale("DE", true, "de")
	expected := "\U0001F1E9\U0001F1EA Deutschland"
	if result != expected {
		t.Fatalf("FormatCountryDisplayForLocale(DE, true, de) = %q, want %q", result, expected)
	}
}
