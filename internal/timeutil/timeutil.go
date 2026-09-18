package timeutil

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	LayoutDateTime       = "2006-01-02 15:04:05"
	LayoutDateTimeMicros = "2006-01-02 15:04:05.999999"
	LayoutDateOnly       = "2006-01-02"
	LayoutDateTimeUTC    = "2006-01-02 15:04:05 UTC"
	LayoutDateTimeZone   = "2006-01-02 15:04:05 MST"
	LayoutRFC1123Z       = "Mon, 02 Jan 2006 15:04:05 -0700"

	// Loose RFC 822/1123 variants seen in real-world feeds (e.g. Proofpoint's
	// newsroom feed emits "07 Aug 2026 15:08:04" without weekday and zone).
	LayoutRFC1123NoWeekdayZ   = "02 Jan 2006 15:04:05 -0700"
	LayoutRFC1123NoWeekday    = "02 Jan 2006 15:04:05 MST"
	LayoutRFC1123NoZone       = "Mon, 02 Jan 2006 15:04:05"
	LayoutRFC1123NoWeekdayNoZ = "02 Jan 2006 15:04:05"

	// Hand-rolled feeds that spell the weekday out. RSS 2.0 permits either a
	// numeric offset or a zone abbreviation, so both variants are needed. The
	// zone-less long-weekday form is deliberately absent: such an item stays
	// undated (and is delivered) rather than guessing a zone.
	LayoutRFC1123LongWeekdayZ = "Monday, 02 Jan 2006 15:04:05 -0700"
	LayoutRFC1123LongWeekday  = "Monday, 02 Jan 2006 15:04:05 MST"

	// Zone-less ISO 8601, emitted by WordPress/Drupal and hand-built <dc:date>
	// values with no timezone configured. Tried last, after every zoned layout.
	LayoutISO8601NoZone = "2006-01-02T15:04:05"

	// BSI's Government Site Builder CMS emits an unpadded day ("Fri, 4 Sep
	// 2026 09:55:00 +0200"): RFC 822's date production is 1*2DIGIT, so a
	// single digit is conformant, zero-padding is only a convention. Go's "2"
	// day token accepts both one and two digits (unlike the fixed-width "02"
	// every other layout here uses), so this layout is a strict superset of
	// LayoutRFC1123Z for the day field alone -- it cannot mis-parse anything
	// LayoutRFC1123Z already owns, and listing it right after LayoutRFC1123Z
	// means a two-digit-day value keeps matching that stricter layout first.
	LayoutRFC1123UnpaddedDayZ = "Mon, 2 Jan 2006 15:04:05 -0700"

	// CISA's all.xml emits RFC 822's two-digit year together with seconds
	// ("Mon, 31 Aug 26 12:00:00 +0000"), a combination neither time.RFC822Z
	// (no seconds) nor "Mon, "+time.RFC822Z (no seconds) accepts. The 4-digit
	// year in every other zoned layout here requires exactly 4 digit
	// characters, so it cannot consume this value's 2-digit year followed by
	// a space -- no shadowing in either direction. Inherits Go's two-digit-year
	// pivot (yy>=69 -> 19yy, else 20yy), which differs from RFC 2822's
	// 50-99->19xx/00-49->20xx for yy in [50,68]; pre-existing for every other
	// two-digit-year layout in this table, irrelevant to CISA's "26".
	LayoutRFC1123ShortYearZ = "Mon, 02 Jan 06 15:04:05 -0700"
)

var zoneTimestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	LayoutRFC1123Z,
	LayoutRFC1123UnpaddedDayZ, // strictly generalizes LayoutRFC1123Z's day field; placed right after it
	time.RFC1123,
	LayoutRFC1123NoWeekdayZ,
	LayoutRFC1123LongWeekdayZ,
	LayoutRFC1123NoWeekday,
	LayoutRFC1123LongWeekday,
	time.RFC822Z,
	time.RFC822,
	"Mon, " + time.RFC822Z,
	"Mon, " + time.RFC822,
	LayoutRFC1123ShortYearZ, // grouped with the other two-digit-year layouts
}

var localTimestampLayouts = []string{
	LayoutDateTimeMicros,
	LayoutDateTime,
	LayoutDateOnly,
	LayoutRFC1123NoZone,
	LayoutRFC1123NoWeekdayNoZ,
	LayoutISO8601NoZone,
}

// AcceptedFlexibleTimestampLayouts returns the layouts accepted by ParseFlexibleTimestamp.
func AcceptedFlexibleTimestampLayouts() []string {
	layouts := make([]string, 0, len(zoneTimestampLayouts)+len(localTimestampLayouts))
	layouts = append(layouts, zoneTimestampLayouts...)
	layouts = append(layouts, localTimestampLayouts...)
	return layouts
}

// ParseFlexibleTimestamp parses timestamp formats used by API responses,
// persisted status files, RSS feeds, and recovery code.
func ParseFlexibleTimestamp(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("timestamp is empty")
	}

	if normalized, ok := normalizeUTZoneSuffix(value); ok {
		if parsed, err := ParseFlexibleTimestamp(normalized); err == nil {
			return parsed, nil
		}
	}

	var firstNamedZoneErr error
	for _, layout := range zoneTimestampLayouts {
		if layoutUsesNamedZone(layout) {
			parsed, err := parseNamedZoneTimestamp(layout, value)
			if err == nil {
				return parsed, nil
			}
			if firstNamedZoneErr == nil && errors.Is(err, errUnknownZoneAbbreviation) {
				firstNamedZoneErr = err
			}
			continue
		}
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	for _, layout := range localTimestampLayouts {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed, nil
		}
	}
	if firstNamedZoneErr != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q: %w", value, firstNamedZoneErr)
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
}

// normalizeUTZoneSuffix rewrites a trailing RFC 822/5322 "UT" (Universal
// Time) zone token to the equivalent numeric offset "+0000" so it reaches
// the existing offset-based layouts. Go's own zone-abbreviation parser
// (time/format.go parseTimeZone) requires at least 3 characters, so a
// 2-character token like "UT" never matches an "MST"-style layout
// placeholder at all -- it is not a namedZoneOffsets lookup miss, it never
// reaches time.Parse in a form it can attempt. Unlike the single-letter
// military zones (deliberately unsupported, see the namedZoneOffsets
// comment), "UT" is unambiguous: RFC 5322 section 4.3 lists it, apart from
// the broken single-letter set, as a defined synonym for "+0000".
func normalizeUTZoneSuffix(value string) (string, bool) {
	const suffix = " UT"
	if !strings.HasSuffix(value, suffix) {
		return "", false
	}
	return strings.TrimSuffix(value, suffix) + " +0000", true
}

func ParseFlexibleTimestampOrZero(value string) time.Time {
	parsed, err := ParseFlexibleTimestamp(value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func FormatDateTime(value time.Time) string {
	return value.Format(LayoutDateTime)
}

func FormatDateTimeMicros(value time.Time) string {
	return value.Format(LayoutDateTimeMicros)
}

func FormatDateOnly(value time.Time) string {
	return value.Format(LayoutDateOnly)
}

func FormatDateTimeUTC(value time.Time) string {
	return value.UTC().Format(LayoutDateTimeUTC)
}

func FormatDisplayTime(value time.Time, layout, timezone string) string {
	layout = strings.TrimSpace(layout)
	if layout == "" {
		layout = LayoutDateTimeZone
	}

	location := time.UTC
	if timezone = strings.TrimSpace(timezone); timezone != "" {
		if configured, err := time.LoadLocation(timezone); err == nil {
			location = configured
		}
	}

	return value.In(location).Format(layout)
}

func FormatDisplayTimestamp(value, layout, timezone string) string {
	parsed, err := ParseFlexibleTimestamp(value)
	if err != nil {
		return value
	}
	return FormatDisplayTime(parsed, layout, timezone)
}

// namedZoneOffsets maps RFC 822/1123 feed zone abbreviations to fixed UTC offsets in
// seconds. Regionally ambiguous abbreviations are deliberately absent: an unknown one
// must fail so the caller's fallback runs instead of yielding an instant hours off.
var namedZoneOffsets = map[string]int{
	// RFC 822 section 5.1 North American zones, plus the unambiguous remainder.
	"EST": -5 * 3600, "EDT": -4 * 3600, "CST": -6 * 3600, "CDT": -5 * 3600,
	"MST": -7 * 3600, "MDT": -6 * 3600, "PST": -8 * 3600, "PDT": -7 * 3600,
	"AKST": -9 * 3600, "AKDT": -8 * 3600, "HST": -10 * 3600,
	"WET": 0, "WEST": 1 * 3600, "CET": 1 * 3600, "CEST": 2 * 3600,
	"EET": 2 * 3600, "EEST": 3 * 3600, "BST": 1 * 3600,
	"JST": 9 * 3600, "AEST": 10 * 3600, "AEDT": 11 * 3600,
	"NZST": 12 * 3600, "NZDT": 13 * 3600,
}

// layoutUsesNamedZone reports whether a layout carries the named-zone token "MST".
// It is only valid for the fixed zoneTimestampLayouts list, where "MST" appears
// exactly in the five named-zone layouts; revisit it if a layout is added.
func layoutUsesNamedZone(layout string) bool {
	return strings.Contains(layout, "MST")
}

// parseNamedZoneTimestamp parses a layout whose zone token is an abbreviation. Parsing in
// time.UTC makes it host-independent: time.UTC has an empty zone table, so lookupName never
// matches and every abbreviation deterministically yields offset 0 plus its literal name,
// leaving namedZoneOffsets as the only thing that decides. The one exception is Go's own
// "GMT+h" special case, which resolves an offset regardless of the location argument.
func parseNamedZoneTimestamp(layout, value string) (time.Time, error) {
	parsed, err := time.ParseInLocation(layout, value, time.UTC)
	if err != nil {
		return time.Time{}, err
	}
	abbreviation, offset := parsed.Zone()
	if offset != 0 {
		// Go resolves the RFC 822 "GMT+h" / "GMT-h" form itself but leaves the
		// instant at the wall clock; apply the offset it found.
		return parsed.Add(-time.Duration(offset) * time.Second), nil
	}
	if abbreviation == "UTC" || abbreviation == "GMT" {
		return parsed, nil
	}
	offset, ok := namedZoneOffsets[abbreviation]
	if !ok {
		return time.Time{}, fmt.Errorf("%w %q in timestamp %q", errUnknownZoneAbbreviation, abbreviation, value)
	}
	return parsed.Add(-time.Duration(offset) * time.Second).In(time.FixedZone(abbreviation, offset)), nil
}

// errUnknownZoneAbbreviation identifies a parseNamedZoneTimestamp failure
// caused by a zone abbreviation absent from namedZoneOffsets, as opposed to
// a plain layout/value structural mismatch (wrong weekday spelling, wrong
// field count, ...). ParseFlexibleTimestamp uses it to recover the first
// such detail across the zoned-layout loop, instead of discarding it in
// favour of the generic "invalid timestamp" message.
var errUnknownZoneAbbreviation = errors.New("unknown time zone abbreviation")
