package config

import "github.com/8linkz-sec/Ransomware-News-Bot/internal/filter"

// FilterRules converts JSON webhook filter configuration into immutable
// filter-layer rules.
func FilterRules(filters *WebhookFilters) *filter.Rules {
	if filters == nil {
		return nil
	}
	return &filter.Rules{
		IncludeGroups:     cloneStringSlice(filters.IncludeGroups),
		IncludeCountries:  cloneStringSlice(filters.IncludeCountries),
		IncludeActivities: cloneStringSlice(filters.IncludeActivities),
		IncludeKeywords:   cloneStringSlice(filters.IncludeKeywords),
		IncludeCategories: cloneStringSlice(filters.IncludeCategories),
		IncludeFields:     cloneStringSliceMap(filters.IncludeFields),
		ExcludeGroups:     cloneStringSlice(filters.ExcludeGroups),
		ExcludeCountries:  cloneStringSlice(filters.ExcludeCountries),
		ExcludeActivities: cloneStringSlice(filters.ExcludeActivities),
		ExcludeKeywords:   cloneStringSlice(filters.ExcludeKeywords),
		ExcludeCategories: cloneStringSlice(filters.ExcludeCategories),
		ExcludeFields:     cloneStringSliceMap(filters.ExcludeFields),
		KeywordMatchMode:  filters.KeywordMatchMode,
	}
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}

func cloneStringSliceMap(values map[string][]string) map[string][]string {
	if values == nil {
		return nil
	}
	copied := make(map[string][]string, len(values))
	for key, value := range values {
		copied[key] = cloneStringSlice(value)
	}
	return copied
}
