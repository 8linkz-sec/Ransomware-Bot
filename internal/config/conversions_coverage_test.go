package config

import "testing"

func TestFilterRulesPreservesNilCollections(t *testing.T) {
	rules := FilterRules(&WebhookFilters{})
	if rules == nil {
		t.Fatal("FilterRules() = nil, want rules for non-nil filters")
	}
	if rules.IncludeGroups != nil || rules.ExcludeGroups != nil {
		t.Fatalf("group rules = %#v/%#v, want nil for unset lists", rules.IncludeGroups, rules.ExcludeGroups)
	}
	if rules.IncludeFields != nil || rules.ExcludeFields != nil {
		t.Fatalf("field rules = %#v/%#v, want nil for unset maps", rules.IncludeFields, rules.ExcludeFields)
	}
}

func TestNotificationFormatOptionsPreservesNilLabelMaps(t *testing.T) {
	format := &FormatConfig{}
	opts := NotificationFormatOptions(format)
	if opts == nil {
		t.Fatal("NotificationFormatOptions() = nil, want options for non-nil format")
	}
	if opts.FieldLabels != nil {
		t.Fatalf("FieldLabels = %#v, want nil for unset map", opts.FieldLabels)
	}
	if opts.Discord.FieldLabels != nil || opts.Slack.FieldLabels != nil || opts.RSS.FieldLabels != nil {
		t.Fatal("platform FieldLabels should stay nil for unset maps")
	}
}

func TestQuietHoursIsActiveUsesCurrentTime(t *testing.T) {
	var nilQuietHours *QuietHours
	if nilQuietHours.IsActive() {
		t.Fatal("nil QuietHours IsActive() = true, want false")
	}

	disabled := &QuietHours{Enabled: false, Start: "00:00", End: "23:59"}
	if disabled.IsActive() {
		t.Fatal("disabled QuietHours IsActive() = true, want false")
	}
}
