package timeutil

import (
	"testing"
	"time"

	// Embed tzdata so timezone-dependent assertions (e.g. Europe/Berlin) do not
	// rely on the host's timezone database.
	_ "time/tzdata"
)

func TestParseFlexibleTimestampAcceptsPersistedAndFeedLayouts(t *testing.T) {
	tests := []string{
		"2026-01-02T03:04:05.123456789Z",
		"2026-01-02T03:04:05Z",
		"2026-01-02 03:04:05.123456",
		"2026-01-02 03:04:05",
		"Fri, 02 Jan 2026 03:04:05 +0000",
		"2026-01-02",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			parsed, err := ParseFlexibleTimestamp(input)
			if err != nil {
				t.Fatalf("ParseFlexibleTimestamp(%q) error = %v", input, err)
			}
			if parsed.IsZero() {
				t.Fatalf("ParseFlexibleTimestamp(%q) returned zero time", input)
			}
		})
	}
}

func TestParseFlexibleTimestampRejectsInvalidInput(t *testing.T) {
	if _, err := ParseFlexibleTimestamp("not a timestamp"); err == nil {
		t.Fatal("ParseFlexibleTimestamp accepted invalid input")
	}
	if got := ParseFlexibleTimestampOrZero("not a timestamp"); !got.IsZero() {
		t.Fatalf("ParseFlexibleTimestampOrZero() = %s, want zero", got)
	}
}

func TestParseFlexibleTimestampUsesUTCForUnzonedLayouts(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"2026-01-02 03:04:05", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		// Zone-less ISO 8601: WordPress/Drupal and hand-built <dc:date> values
		// with no timezone configured emit this. Read as UTC like every other
		// unzoned layout, instead of leaving the item undated.
		{"2026-01-15T10:00:00", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)},
		// Non-shadowing guards: the shorter date-only and space-separated
		// layouts keep their current results and must not swallow the ISO form.
		{"2026-01-15", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)},
		{"2026-01-15 10:00:00", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			parsed, err := ParseFlexibleTimestamp(tt.input)
			if err != nil {
				t.Fatalf("ParseFlexibleTimestamp(%q) error = %v", tt.input, err)
			}
			if parsed.Location() != time.UTC {
				t.Fatalf("ParseFlexibleTimestamp(%q) location = %v, want UTC", tt.input, parsed.Location())
			}
			if !parsed.Equal(tt.want) {
				t.Fatalf("ParseFlexibleTimestamp(%q) = %s, want %s", tt.input, parsed.UTC(), tt.want)
			}
		})
	}
}

func TestAcceptedFlexibleTimestampLayoutsReturnsCopy(t *testing.T) {
	layouts := AcceptedFlexibleTimestampLayouts()
	if len(layouts) == 0 {
		t.Fatal("AcceptedFlexibleTimestampLayouts() returned no layouts")
	}
	layouts[0] = "mutated"

	if got := AcceptedFlexibleTimestampLayouts()[0]; got == "mutated" {
		t.Fatal("AcceptedFlexibleTimestampLayouts() returned shared backing storage")
	}
}

func TestFormatHelpersUseSharedLayouts(t *testing.T) {
	value := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.FixedZone("CET", 3600))

	if got := FormatDateTime(value); got != "2026-01-02 03:04:05" {
		t.Fatalf("FormatDateTime() = %q", got)
	}
	if got := FormatDateTimeMicros(value); got != "2026-01-02 03:04:05.123456" {
		t.Fatalf("FormatDateTimeMicros() = %q", got)
	}
	if got := FormatDateOnly(value); got != "2026-01-02" {
		t.Fatalf("FormatDateOnly() = %q", got)
	}
	if got := FormatDateTimeUTC(value); got != "2026-01-02 02:04:05 UTC" {
		t.Fatalf("FormatDateTimeUTC() = %q", got)
	}
}

func TestFormatDisplayTimeUsesConfiguredLayoutAndTimezone(t *testing.T) {
	value := time.Date(2026, 1, 2, 2, 4, 5, 0, time.UTC)

	got := FormatDisplayTime(value, "02.01.2006 15:04 MST", "Europe/Berlin")

	if got != "02.01.2026 03:04 CET" {
		t.Fatalf("FormatDisplayTime() = %q", got)
	}
}

func TestFormatDisplayTimestampParsesFlexibleInput(t *testing.T) {
	got := FormatDisplayTimestamp("2026-01-02 02:04:05", "02.01.2006 15:04 MST", "Europe/Berlin")

	if got != "02.01.2026 03:04 CET" {
		t.Fatalf("FormatDisplayTimestamp() = %q", got)
	}
}

func TestParseFlexibleTimestampRejectsEmptyInput(t *testing.T) {
	for _, input := range []string{"", "   "} {
		if _, err := ParseFlexibleTimestamp(input); err == nil {
			t.Fatalf("ParseFlexibleTimestamp(%q) accepted empty input", input)
		}
	}
}

func TestParseFlexibleTimestampOrZeroReturnsParsedValue(t *testing.T) {
	got := ParseFlexibleTimestampOrZero("2026-01-02 03:04:05")

	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("ParseFlexibleTimestampOrZero() = %s, want %s", got, want)
	}
}

func TestFormatDisplayTimeFallsBackToDefaults(t *testing.T) {
	value := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	tests := []struct {
		name     string
		layout   string
		timezone string
		want     string
	}{
		{name: "empty layout and timezone default to UTC zone layout", layout: "", timezone: "", want: "2026-01-02 03:04:05 UTC"},
		{name: "whitespace layout defaults to UTC zone layout", layout: "   ", timezone: "", want: "2026-01-02 03:04:05 UTC"},
		{name: "unknown timezone falls back to UTC", layout: "", timezone: "Not/AZone", want: "2026-01-02 03:04:05 UTC"},
		{name: "valid timezone converts wall time", layout: "", timezone: "Europe/Berlin", want: "2026-01-02 04:04:05 CET"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatDisplayTime(value, tt.layout, tt.timezone); got != tt.want {
				t.Fatalf("FormatDisplayTime(%q, %q) = %q, want %q", tt.layout, tt.timezone, got, tt.want)
			}
		})
	}
}

func TestFormatDisplayTimestampReturnsInputWhenUnparsable(t *testing.T) {
	if got := FormatDisplayTimestamp("not a timestamp", "", "Europe/Berlin"); got != "not a timestamp" {
		t.Fatalf("FormatDisplayTimestamp(invalid) = %q, want input echoed back", got)
	}
}

func TestParseFlexibleTimestampAcceptsLooseRFC822Variants(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		// Proofpoint newsroom feed: no weekday, no timezone -> assume UTC.
		{"07 Aug 2026 15:08:04", time.Date(2026, 8, 7, 15, 8, 4, 0, time.UTC)},
		// Weekday but no timezone -> assume UTC.
		{"Fri, 07 Aug 2026 15:08:04", time.Date(2026, 8, 7, 15, 8, 4, 0, time.UTC)},
		// No weekday, numeric offset.
		{"07 Aug 2026 15:08:04 +0200", time.Date(2026, 8, 7, 13, 8, 4, 0, time.UTC)},
		// No weekday, zone abbreviation.
		{"07 Aug 2026 15:08:04 GMT", time.Date(2026, 8, 7, 15, 8, 4, 0, time.UTC)},
		// RFC 822 with two-digit year.
		{"07 Aug 26 15:08 GMT", time.Date(2026, 8, 7, 15, 8, 0, 0, time.UTC)},
		{"Fri, 07 Aug 26 15:08 -0100", time.Date(2026, 8, 7, 16, 8, 0, 0, time.UTC)},
		// Spelled-out weekday (long-form RFC 1123), emitted by hand-rolled
		// feeds. RSS 2.0 permits a numeric offset or a zone abbreviation, so
		// both variants must parse.
		{"Thursday, 15 Jan 2026 10:00:00 GMT", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)},
		{"Thursday, 15 Jan 2026 10:00:00 PDT", time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC)},
		{"Thursday, 15 Jan 2026 10:00:00 +0200", time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC)},
		// Non-shadowing guards: the short-weekday and ISO layouts keep their
		// current results after the long-weekday layouts were added.
		{"Thu, 15 Jan 2026 10:00:00 GMT", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)},
		{"Mon, 02 Jan 2026 15:04:05", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)},
		{"2026-01-15T10:00:00Z", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)},
		{"2026-01-15T10:00:00+02:00", time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC)},
		// Real captured pubDate values (bsi_csw.xml, f3.xml) -- BSI's Government
		// Site Builder CMS omits the leading zero on a single-digit day; CISA's
		// all.xml emits RFC 822's two-digit year together with seconds.
		{"Fri, 4 Sep 2026 09:55:00 +0200", time.Date(2026, 9, 4, 7, 55, 0, 0, time.UTC)},
		{"Wed, 2 Sep 2026 15:15:00 +0200", time.Date(2026, 9, 2, 13, 15, 0, 0, time.UTC)},
		{"Mon, 31 Aug 26 12:00:00 +0000", time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)},
		{"Thu, 03 Sep 26 12:00:00 +0000", time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)},
		// Non-shadowing guards: the layouts these two generalize/extend keep
		// their current results (kills a mutation that widens the day or year
		// token too far on an existing layout instead of adding a new one).
		{"Fri, 24 Apr 2026 12:10:00 +0200", time.Date(2026, 4, 24, 10, 10, 0, 0, time.UTC)}, // 2-digit day, LayoutRFC1123Z still matches first
		{"07 Aug 26 15:08 GMT", time.Date(2026, 8, 7, 15, 8, 0, 0, time.UTC)},               // RFC822 2-digit-year, no seconds, unaffected
		// Go's Parse collapses a run of spaces in the value against a single
		// space in the layout, so the "2" day token also accepts the
		// space-padded RFC 822 form -- pins that a later "2" -> "02" "tidy-up"
		// of LayoutRFC1123UnpaddedDayZ would be caught.
		{"Fri,  4 Sep 2026 09:55:00 +0200", time.Date(2026, 9, 4, 7, 55, 0, 0, time.UTC)},
		// The normalizeUTZoneSuffix path now also reaches the new short-year layout.
		{"Mon, 31 Aug 26 12:00:00 UT", time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			parsed, err := ParseFlexibleTimestamp(tt.input)
			if err != nil {
				t.Fatalf("ParseFlexibleTimestamp(%q) error = %v", tt.input, err)
			}
			if !parsed.Equal(tt.want) {
				t.Fatalf("ParseFlexibleTimestamp(%q) = %s, want %s", tt.input, parsed.UTC(), tt.want)
			}
		})
	}

	// Deliberate gaps: none of these occurs in any captured configured feed,
	// and accepting one would mean a layout was widened past what the two
	// target feeds (BSI, CISA) need. RFC 822 permits some of these variations;
	// this parser does not guess at them.
	for _, rejected := range []string{
		"Fri, 4 Sep 26 09:55:00 +0200",    // unpadded day AND two-digit year
		"Fri, 4 Sep 2026 09:55:00 CEST",   // unpadded day + named zone
		"Fri, 4 Sep 2026 09:55:00 +02:00", // colon in the numeric offset
		"Fri, 0 Sep 2026 09:55:00 +0200",  // day out of range
		"Fri, 32 Sep 2026 09:55:00 +0200",
		"Fri, 123 Sep 2026 09:55:00 +0200",
		"Fri, 4 Sep 202 09:55:00 +0200",  // three-digit year
		"Mon, 31 Aug 260 12:00:00 +0000", // three-digit short year
		"Mon, 31 Aug 26 25:00:00 +0000",  // hour out of range
		// The two layouts added for BSI and CISA relax exactly one field each
		// (the day, resp. the year+seconds combination). Their remaining
		// minute, second and zone tokens stay fixed-width, so a value with an
		// unpadded minute or second, or a bare "Z" zone designator in place of
		// a numeric offset, must still be rejected -- each row below is
		// accepted the moment one of those tokens is relaxed as well.
		"Fri, 4 Sep 2026 09:55:0 +0200",
		"Fri, 4 Sep 2026 09:5:00 +0200",
		"Mon, 31 Aug 26 12:00:0 +0000",
		"Mon, 31 Aug 26 12:0:00 +0000",
		"Mon, 31 Aug 26 12:00:00 Z",
	} {
		t.Run("rejects "+rejected, func(t *testing.T) {
			if _, err := ParseFlexibleTimestamp(rejected); err == nil {
				t.Errorf("ParseFlexibleTimestamp(%q) accepted a value the layout table must not own", rejected)
			}
		})
	}
}

// namedZoneCases pins the exact instant every accepted zone abbreviation must
// map to. It is shared by TestParseFlexibleTimestampMapsNamedZoneAbbreviations
// and TestParseFlexibleTimestampNamedZonesAreHostIndependent so the two cannot
// drift apart. No test in this package calls t.Parallel(): the host-independence
// test mutates the process-global time.Local, so keep it that way.
var namedZoneCases = []struct {
	input string
	want  time.Time
}{
	// Northern winter: standard time abbreviations.
	{"Fri, 02 Jan 2026 15:04:05 EST", time.Date(2026, 1, 2, 20, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 CST", time.Date(2026, 1, 2, 21, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 MST", time.Date(2026, 1, 2, 22, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 PST", time.Date(2026, 1, 2, 23, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 AKST", time.Date(2026, 1, 3, 0, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 HST", time.Date(2026, 1, 3, 1, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 WET", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 CET", time.Date(2026, 1, 2, 14, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 EET", time.Date(2026, 1, 2, 13, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 JST", time.Date(2026, 1, 2, 6, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 AEST", time.Date(2026, 1, 2, 5, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 NZST", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
	// UTC and GMT keep today's instant.
	{"Fri, 02 Jan 2026 15:04:05 GMT", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 UTC", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)},

	// Northern summer: daylight saving abbreviations.
	{"Tue, 07 Jul 2026 15:04:05 EDT", time.Date(2026, 7, 7, 19, 4, 5, 0, time.UTC)},
	{"Tue, 07 Jul 2026 15:04:05 CDT", time.Date(2026, 7, 7, 20, 4, 5, 0, time.UTC)},
	{"Tue, 07 Jul 2026 15:04:05 MDT", time.Date(2026, 7, 7, 21, 4, 5, 0, time.UTC)},
	{"Tue, 07 Jul 2026 15:04:05 PDT", time.Date(2026, 7, 7, 22, 4, 5, 0, time.UTC)},
	{"Tue, 07 Jul 2026 15:04:05 AKDT", time.Date(2026, 7, 7, 23, 4, 5, 0, time.UTC)},
	{"Tue, 07 Jul 2026 15:04:05 WEST", time.Date(2026, 7, 7, 14, 4, 5, 0, time.UTC)},
	{"Tue, 07 Jul 2026 15:04:05 CEST", time.Date(2026, 7, 7, 13, 4, 5, 0, time.UTC)},
	{"Tue, 07 Jul 2026 15:04:05 EEST", time.Date(2026, 7, 7, 12, 4, 5, 0, time.UTC)},
	{"Tue, 07 Jul 2026 15:04:05 BST", time.Date(2026, 7, 7, 14, 4, 5, 0, time.UTC)},

	// Southern hemisphere daylight saving.
	{"Tue, 06 Jan 2026 15:04:05 AEDT", time.Date(2026, 1, 6, 4, 4, 5, 0, time.UTC)},
	{"Tue, 06 Jan 2026 15:04:05 NZDT", time.Date(2026, 1, 6, 2, 4, 5, 0, time.UTC)},

	// RFC 822 "GMT+h" / "GMT-h" form: Go resolves the offset itself.
	{"Fri, 02 Jan 2026 15:04:05 GMT+1", time.Date(2026, 1, 2, 14, 4, 5, 0, time.UTC)},
	{"Fri, 02 Jan 2026 15:04:05 GMT-5", time.Date(2026, 1, 2, 20, 4, 5, 0, time.UTC)},

	// The other three named layouts.
	{"02 Jan 2026 15:04:05 EST", time.Date(2026, 1, 2, 20, 4, 5, 0, time.UTC)},
	{"02 Jan 26 15:04 EST", time.Date(2026, 1, 2, 20, 4, 0, 0, time.UTC)},
	{"Fri, 02 Jan 26 15:04 EST", time.Date(2026, 1, 2, 20, 4, 0, 0, time.UTC)},

	// Numeric-offset layouts keep priority and stay host-independent.
	{"Fri, 02 Jan 2026 15:04:05 -0500", time.Date(2026, 1, 2, 20, 4, 5, 0, time.UTC)},
	{"02 Jan 2026 15:04:05 +0200", time.Date(2026, 1, 2, 13, 4, 5, 0, time.UTC)},
	{"02 Jan 26 15:04 -0100", time.Date(2026, 1, 2, 16, 4, 0, 0, time.UTC)},
	{"2026-01-02T15:04:05Z", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)},

	// Unzoned layout: still parsed as UTC.
	{"2026-01-02 15:04:05", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)},
}

func TestParseFlexibleTimestampMapsNamedZoneAbbreviations(t *testing.T) {
	for _, tt := range namedZoneCases {
		t.Run(tt.input, func(t *testing.T) {
			parsed, err := ParseFlexibleTimestamp(tt.input)
			if err != nil {
				t.Fatalf("ParseFlexibleTimestamp(%q) error = %v", tt.input, err)
			}
			if !parsed.Equal(tt.want) {
				t.Fatalf("ParseFlexibleTimestamp(%q) = %s, want %s", tt.input, parsed.UTC(), tt.want)
			}
		})
	}
}

func TestParseFlexibleTimestampRejectsUnknownZoneAbbreviation(t *testing.T) {
	inputs := []string{
		// Corrupt abbreviation across all five named layouts.
		"Fri, 02 Jan 2026 15:04:05 XYZ",
		"02 Jan 2026 15:04:05 XYZ",
		"02 Jan 26 15:04 XYZ",
		"Fri, 02 Jan 26 15:04 XYZ",
		"Monday, 15 Jan 2026 10:00:00 XYZ",
		// A garbage weekday stays rejected; the long-weekday layouts must not
		// widen what counts as a weekday.
		"Notaday, 15 Jan 2026 10:00:00 GMT",
		// A long weekday without any zone is deliberately not added to
		// localTimestampLayouts: such an item stays undated (and is delivered).
		"Thursday, 15 Jan 2026 10:00:00",
		// Regionally ambiguous, deliberately absent from the offset table.
		"Fri, 02 Jan 2026 15:04:05 IST",
		// Go hard-codes these two in parseTimeZone; they fabricated offset 0.
		"Fri, 02 Jan 2026 15:04:05 WITA",
		"Fri, 02 Jan 2026 15:04:05 ChST",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			parsed, err := ParseFlexibleTimestamp(input)
			if err == nil {
				t.Fatalf("ParseFlexibleTimestamp(%q) = %s, want error", input, parsed.UTC())
			}
			if got := ParseFlexibleTimestampOrZero(input); !got.IsZero() {
				t.Fatalf("ParseFlexibleTimestampOrZero(%q) = %s, want zero", input, got)
			}
		})
	}
}

func TestParseFlexibleTimestampNamedZonesAreHostIndependent(t *testing.T) {
	// No t.Parallel() anywhere in this file: time.Local is process-global.
	for _, name := range []string{"Europe/Berlin", "UTC", "America/New_York"} {
		t.Run(name, func(t *testing.T) {
			loc, err := time.LoadLocation(name)
			if err != nil {
				t.Fatalf("LoadLocation(%q) error = %v", name, err)
			}
			saved := time.Local
			t.Cleanup(func() { time.Local = saved })
			time.Local = loc

			for _, tt := range namedZoneCases {
				parsed, err := ParseFlexibleTimestamp(tt.input)
				if err != nil || !parsed.Equal(tt.want) {
					t.Errorf("ParseFlexibleTimestamp(%q) = %v, %v; want %s", tt.input, parsed, err, tt.want)
				}
			}
		})
	}
}

// TestParseFlexibleTimestampAttachesNamedZoneLocation pins the *location* the
// parser attaches, not just the instant. namedZoneCases compares with
// parsed.Equal, which ignores the location entirely, so without this test the
// offset handed to time.FixedZone is unpinned — flipping its sign leaves the
// whole suite green. The location is load-bearing: rssSignatureDate
// (internal/model/rss.go:205) and statusSignatureDate
// (internal/status/tracker.go:2487) day-bucket the legacy dedup signatures via
// FormatDateOnly, which formats in the value's own location, so the attached
// offset decides the bucket. Keeping the feed's wall clock is what stops the
// named-zone fix from re-bucketing already-stored items.
func TestParseFlexibleTimestampAttachesNamedZoneLocation(t *testing.T) {
	tests := []struct {
		input      string
		wantZone   string
		wantOffset int
		wantWall   string
	}{
		{"Fri, 02 Jan 2026 15:04:05 EST", "EST", -5 * 3600, "2026-01-02 15:04:05"},
		{"Fri, 02 Jan 2026 15:04:05 NZST", "NZST", 12 * 3600, "2026-01-02 15:04:05"},
		{"Tue, 07 Jul 2026 15:04:05 PDT", "PDT", -7 * 3600, "2026-07-07 15:04:05"},
		{"Fri, 02 Jan 2026 15:04:05 WET", "WET", 0, "2026-01-02 15:04:05"},
		{"Fri, 02 Jan 2026 15:04:05 GMT", "GMT", 0, "2026-01-02 15:04:05"},
		{"Fri, 02 Jan 2026 15:04:05 UTC", "UTC", 0, "2026-01-02 15:04:05"},
		{"Fri, 02 Jan 2026 15:04:05 GMT+1", "GMT+1", 1 * 3600, "2026-01-02 15:04:05"},
		{"Fri, 02 Jan 2026 15:04:05 GMT-5", "GMT-5", -5 * 3600, "2026-01-02 15:04:05"},
		// Numeric offsets keep Go's nameless fixed zone; same wall-clock rule.
		{"Fri, 02 Jan 2026 15:04:05 -0500", "", -5 * 3600, "2026-01-02 15:04:05"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			parsed, err := ParseFlexibleTimestamp(tt.input)
			if err != nil {
				t.Fatalf("ParseFlexibleTimestamp(%q) error = %v", tt.input, err)
			}
			zone, offset := parsed.Zone()
			if zone != tt.wantZone || offset != tt.wantOffset {
				t.Fatalf("ParseFlexibleTimestamp(%q).Zone() = %q, %d; want %q, %d", tt.input, zone, offset, tt.wantZone, tt.wantOffset)
			}
			if got := FormatDateTime(parsed); got != tt.wantWall {
				t.Fatalf("FormatDateTime(ParseFlexibleTimestamp(%q)) = %q, want %q (feed wall clock preserved)", tt.input, got, tt.wantWall)
			}
			if got, want := FormatDateOnly(parsed), tt.wantWall[:10]; got != want {
				t.Fatalf("FormatDateOnly(ParseFlexibleTimestamp(%q)) = %q, want %q (legacy signature day bucket)", tt.input, got, want)
			}
		})
	}
}

func TestParseFlexibleTimestampParsesUTZoneSuffix(t *testing.T) {
	parsed, err := ParseFlexibleTimestamp("Fri, 02 Jan 2026 15:04:05 UT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	if !parsed.Equal(want) {
		t.Fatalf("got %v, want %v", parsed, want)
	}
}

// Documents the RFC 5322 section 4.3 decision: single-letter military zones,
// including "Z", stay unsupported by design -- resolving them would guess an
// offset RFC 5322 itself says is unknown. Do not "fix" this test to accept
// them -- see timeutil.go's own comment on this decision.
func TestParseFlexibleTimestampRejectsMilitaryLetterZonesDocumented(t *testing.T) {
	for _, c := range []string{
		"Fri, 02 Jan 2026 15:04:05 Z",
		"Fri, 02 Jan 2026 15:04:05 A",
		"Fri, 02 Jan 2026 15:04:05 M",
		"Fri, 02 Jan 2026 15:04:05 N",
		"Fri, 02 Jan 2026 15:04:05 Y",
	} {
		if _, err := ParseFlexibleTimestamp(c); err == nil {
			t.Fatalf("ParseFlexibleTimestamp(%q) unexpectedly succeeded", c)
		}
	}
}

func TestParseFlexibleTimestampWrapsNamedZoneAbbreviationDetail(t *testing.T) {
	_, err := ParseFlexibleTimestamp("Fri, 02 Jan 2026 15:04:05 XYZ")
	if err == nil {
		t.Fatal("expected error")
	}
	want := `invalid timestamp "Fri, 02 Jan 2026 15:04:05 XYZ": unknown time zone abbreviation "XYZ" in timestamp "Fri, 02 Jan 2026 15:04:05 XYZ"`
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

// TestParseFlexibleTimestampNoWrappedDetailOnStructuralMismatch pins the
// other side of the wrapped-detail rule: the named-zone abbreviation detail must
// only be surfaced when parseNamedZoneTimestamp's own abbreviation lookup
// fails (errUnknownZoneAbbreviation), never when a named-zone layout's shape
// simply does not match the value at all (wrong weekday spelling, wrong
// field count, or no layout shape matches whatsoever). Without this test,
// firstNamedZoneErr capturing every parseNamedZoneTimestamp error --
// including plain structural time.ParseInLocation failures -- instead of
// only errors.Is(err, errUnknownZoneAbbreviation) ones would go undetected:
// every case here still returns a non-nil error either way, so only the
// exact message pins the distinction.
func TestParseFlexibleTimestampNoWrappedDetailOnStructuralMismatch(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Weekday spelling does not match any layout's weekday token, so
		// parseNamedZoneTimestamp never reaches its abbreviation lookup.
		{
			input: "Notaday, 15 Jan 2026 10:00:00 GMT",
			want:  `invalid timestamp "Notaday, 15 Jan 2026 10:00:00 GMT"`,
		},
		// Matches no layout's shape at all -- parseNamedZoneTimestamp is
		// never even attempted for this value.
		{
			input: "not a timestamp at all",
			want:  `invalid timestamp "not a timestamp at all"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := ParseFlexibleTimestamp(tt.input)
			if err == nil {
				t.Fatal("expected error")
			}
			if err.Error() != tt.want {
				t.Fatalf("got %q, want %q (a structural mismatch must not gain named-zone detail)", err.Error(), tt.want)
			}
		})
	}
}
