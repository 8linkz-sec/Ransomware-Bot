package config

import (
	"reflect"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/keywordmode"
)

func TestFilterRulesCopiesWebhookFilters(t *testing.T) {
	filters := &WebhookFilters{
		IncludeGroups:     []string{"lockbit"},
		IncludeCountries:  []string{"DE"},
		IncludeActivities: []string{"published"},
		IncludeKeywords:   []string{"hospital"},
		IncludeCategories: []string{"security"},
		IncludeFields:     map[string][]string{"victim": {"hospital"}},
		ExcludeGroups:     []string{"test-group"},
		ExcludeCountries:  []string{"CN"},
		ExcludeActivities: []string{"removed"},
		ExcludeKeywords:   []string{"draft"},
		ExcludeCategories: []string{"spam"},
		ExcludeFields:     map[string][]string{"feed_title": {"noise"}},
		KeywordMatchMode:  keywordmode.Regex,
	}

	rules := FilterRules(filters)
	if rules == nil {
		t.Fatal("FilterRules() returned nil")
	}

	if !reflect.DeepEqual(rules.IncludeGroups, filters.IncludeGroups) ||
		!reflect.DeepEqual(rules.IncludeCountries, filters.IncludeCountries) ||
		!reflect.DeepEqual(rules.IncludeActivities, filters.IncludeActivities) ||
		!reflect.DeepEqual(rules.IncludeKeywords, filters.IncludeKeywords) ||
		!reflect.DeepEqual(rules.IncludeCategories, filters.IncludeCategories) ||
		!reflect.DeepEqual(rules.IncludeFields, filters.IncludeFields) ||
		!reflect.DeepEqual(rules.ExcludeGroups, filters.ExcludeGroups) ||
		!reflect.DeepEqual(rules.ExcludeCountries, filters.ExcludeCountries) ||
		!reflect.DeepEqual(rules.ExcludeActivities, filters.ExcludeActivities) ||
		!reflect.DeepEqual(rules.ExcludeKeywords, filters.ExcludeKeywords) ||
		!reflect.DeepEqual(rules.ExcludeCategories, filters.ExcludeCategories) ||
		!reflect.DeepEqual(rules.ExcludeFields, filters.ExcludeFields) ||
		rules.KeywordMatchMode != filters.KeywordMatchMode {
		t.Fatalf("FilterRules() did not preserve filter values: %#v", rules)
	}

	filters.IncludeGroups[0] = "changed"
	if got := rules.IncludeGroups[0]; got != "lockbit" {
		t.Fatalf("FilterRules() aliased IncludeGroups slice, got %q", got)
	}
	filters.IncludeFields["victim"][0] = "changed"
	if got := rules.IncludeFields["victim"][0]; got != "hospital" {
		t.Fatalf("FilterRules() aliased IncludeFields map slice, got %q", got)
	}
}

func TestFilterRulesNil(t *testing.T) {
	if got := FilterRules(nil); got != nil {
		t.Fatalf("FilterRules(nil) = %#v, want nil", got)
	}
}
