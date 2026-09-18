package formatfields

import (
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/country"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"
)

type RansomwareFieldValue struct {
	Canonical     string
	Label         string
	Value         string
	ParsedTime    time.Time
	HasParsedTime bool
}

func RansomwareFieldValueFor(fieldName string, entry model.RansomwareEntry, showUnicodeFlags bool) (RansomwareFieldValue, bool) {
	return RansomwareFieldValueForLocale(fieldName, entry, showUnicodeFlags, "")
}

func RansomwareFieldValueForLocale(
	fieldName string,
	entry model.RansomwareEntry,
	showUnicodeFlags bool,
	displayLocale string,
) (RansomwareFieldValue, bool) {
	return RansomwareFieldValueForDisplay(fieldName, entry, showUnicodeFlags, displayLocale, "", "")
}

func RansomwareFieldValueForDisplay(
	fieldName string,
	entry model.RansomwareEntry,
	showUnicodeFlags bool,
	displayLocale string,
	timestampFormat string,
	displayTimezone string,
) (RansomwareFieldValue, bool) {
	canonical, ok := Normalize(fieldName)
	if !ok {
		return RansomwareFieldValue{}, false
	}

	value := RansomwareFieldValue{
		Canonical: canonical,
		Label:     RansomwareFieldLabel(canonical),
	}

	switch canonical {
	case FieldID:
		value.Value = entry.ID
	case FieldCountry:
		if entry.Country != "" {
			value.Value = country.FormatCountryDisplayForLocale(entry.Country, showUnicodeFlags, displayLocale)
		}
	case FieldVictim:
		value.Value = entry.Victim
	case FieldGroup:
		value.Value = entry.Group
	case FieldActivity:
		value.Value = entry.Activity
	case FieldAttackDate:
		value.Value = timeutil.FormatDisplayTimestamp(entry.AttackDate, timestampFormat, displayTimezone)
		if parsed, err := timeutil.ParseFlexibleTimestamp(entry.AttackDate); err == nil {
			value.ParsedTime = parsed
			value.HasParsedTime = true
		}
	case FieldDiscovered:
		if !entry.Discovered.IsZero() {
			value.Value = timeutil.FormatDisplayTime(entry.Discovered, timestampFormat, displayTimezone)
			value.ParsedTime = entry.Discovered
			value.HasParsedTime = true
		}
	case FieldPublished:
		if !entry.Published.IsZero() {
			value.Value = timeutil.FormatDisplayTime(entry.Published, timestampFormat, displayTimezone)
			value.ParsedTime = entry.Published
			value.HasParsedTime = true
		}
	case FieldPostURL:
		value.Value = textutil.DefangURL(entry.ClaimURL)
	case FieldWebsite:
		value.Value = entry.WebsiteURL
	case FieldDescription:
		value.Value = entry.Description
	case FieldScreenshot:
		value.Value = entry.Screenshot
	default:
		return RansomwareFieldValue{}, false
	}

	return value, true
}

func RansomwareFieldLabel(canonicalField string) string {
	switch canonicalField {
	case FieldID:
		return "ID"
	case FieldCountry:
		return "Country"
	case FieldVictim:
		return "Victim"
	case FieldGroup:
		return "Group"
	case FieldActivity:
		return "Activity"
	case FieldAttackDate:
		return "Attack Date"
	case FieldDiscovered:
		return "Discovered"
	case FieldPublished:
		return "Published"
	case FieldPostURL:
		return "Ransom URL"
	case FieldWebsite:
		return "Website"
	case FieldDescription:
		return "Description"
	case FieldScreenshot:
		return "Screenshot"
	default:
		return canonicalField
	}
}
