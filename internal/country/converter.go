package country

import (
	"strings"
)

// CountryInfo contains country code, name and flag emoji
type CountryInfo struct {
	Code string // ISO-2 country code (e.g., "DE")
	Name string // Full country name (e.g., "Germany")
	Flag string // Unicode flag emoji (e.g., "🇩🇪")
}

// ConvertCountry converts an ISO-2 country code to CountryInfo
// Returns fallback values for unknown or invalid codes
func ConvertCountry(isoCode string) CountryInfo {
	// Normalize input
	code := strings.ToUpper(strings.TrimSpace(isoCode))

	// Handle empty or invalid codes
	if len(code) != 2 {
		return CountryInfo{
			Code: "UN",
			Name: "Unknown",
			Flag: "🌐",
		}
	}

	// Get country name from mapping
	name, exists := countryMapping[code]
	if !exists {
		return CountryInfo{
			Code: code,
			Name: "Unknown",
			Flag: "🌐",
		}
	}

	// Generate Unicode flag emoji
	flag := generateFlagEmoji(code)

	return CountryInfo{
		Code: code,
		Name: name,
		Flag: flag,
	}
}

// GetCountryFlag returns only the flag emoji for a given ISO-2 code
func GetCountryFlag(isoCode string) string {
	return ConvertCountry(isoCode).Flag
}

// generateFlagEmoji converts ISO-2 country code to Unicode flag emoji
// Each flag is composed of two Regional Indicator Symbol letters
func generateFlagEmoji(code string) string {
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return "🌐"
	}

	// Convert each letter to Regional Indicator Symbol
	// A = U+1F1E6, B = U+1F1E7, ..., Z = U+1F1FF
	// Use rune (int32) instead of byte to handle Unicode values
	first := rune(0x1F1E6 + int32(code[0]-'A'))
	second := rune(0x1F1E6 + int32(code[1]-'A'))

	return string([]rune{first, second})
}

// FormatCountryDisplay formats country information for display
// Returns formatted string with flag and name if showFlag is true
func FormatCountryDisplay(isoCode string, showFlag bool) string {
	country := ConvertCountry(isoCode)

	if showFlag {
		return country.Flag + " " + country.Name
	}
	return country.Name
}
