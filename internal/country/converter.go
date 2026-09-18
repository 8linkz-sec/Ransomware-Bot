package country

import (
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

const (
	defaultDisplayLocale = "en"
	unknownCountryName   = "Unknown"
	unknownCountryFlag   = "\U0001F310"
)

// CountryInfo contains display country information.
type CountryInfo struct {
	Name string // Full country name, localized by display locale.
	Flag string // Unicode flag emoji.
}

// ConvertCountry converts an ISO-2 country code to CountryInfo using the
// default English display locale.
func ConvertCountry(isoCode string) CountryInfo {
	return ConvertCountryForLocale(isoCode, defaultDisplayLocale)
}

// ConvertCountryForLocale converts an ISO-2 country code to display
// information using CLDR region names for the requested BCP 47 locale.
func ConvertCountryForLocale(isoCode, displayLocale string) CountryInfo {
	code := strings.ToUpper(strings.TrimSpace(isoCode))
	region, ok := parseISORegion(code)
	if !ok {
		return CountryInfo{Name: unknownCountryName, Flag: unknownCountryFlag}
	}

	name := localizedRegionName(region, displayLocale)
	if name == "" {
		return CountryInfo{Name: unknownCountryName, Flag: unknownCountryFlag}
	}

	return CountryInfo{
		Name: name,
		Flag: generateFlagEmoji(code),
	}
}

func parseISORegion(code string) (language.Region, bool) {
	if len(code) != 2 {
		return language.Region{}, false
	}
	region, err := language.ParseRegion(code)
	if err != nil {
		return language.Region{}, false
	}
	return region, true
}

func localizedRegionName(region language.Region, displayLocale string) string {
	tag := language.English
	if parsed, err := language.Parse(strings.TrimSpace(displayLocale)); err == nil {
		tag = parsed
	}

	name := strings.TrimSpace(display.Regions(tag).Name(region))
	if name == "" || strings.EqualFold(name, region.String()) {
		name = strings.TrimSpace(display.Regions(language.English).Name(region))
	}
	if name == "" || strings.EqualFold(name, region.String()) {
		return ""
	}
	return name
}

// GetCountryFlag returns only the flag emoji for a given ISO-2 code.
func GetCountryFlag(isoCode string) string {
	return ConvertCountry(isoCode).Flag
}

// generateFlagEmoji converts ISO-2 country code to Unicode flag emoji.
func generateFlagEmoji(code string) string {
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return unknownCountryFlag
	}

	first := rune(0x1F1E6 + int32(code[0]-'A'))
	second := rune(0x1F1E6 + int32(code[1]-'A'))

	return string([]rune{first, second})
}

// FormatCountryDisplay formats country information for display.
func FormatCountryDisplay(isoCode string, showFlag bool) string {
	return FormatCountryDisplayForLocale(isoCode, showFlag, defaultDisplayLocale)
}

// FormatCountryDisplayForLocale formats country information for display using
// CLDR region names for the requested locale.
func FormatCountryDisplayForLocale(isoCode string, showFlag bool, displayLocale string) string {
	country := ConvertCountryForLocale(isoCode, displayLocale)
	if showFlag {
		return country.Flag + " " + country.Name
	}
	return country.Name
}
