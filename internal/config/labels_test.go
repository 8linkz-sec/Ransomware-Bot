package config

import "testing"

func TestFormatLabelUsesPrecedenceAndNormalizedKeys(t *testing.T) {
	got := FormatLabel(
		[]string{"feed_title", "source"},
		"Source",
		map[string]string{"Feed Title": "Platform Source"},
		map[string]string{"feed-title": "RSS Source"},
		map[string]string{"source": "Global Source"},
	)
	if got != "Platform Source" {
		t.Fatalf("FormatLabel() = %q, want platform override", got)
	}
}

func TestFormatLabelFallsBackWhenNoConfiguredLabelMatches(t *testing.T) {
	got := FormatLabel([]string{"published"}, "Published", map[string]string{"source": "Quelle"})
	if got != "Published" {
		t.Fatalf("FormatLabel() = %q, want fallback", got)
	}
}

func TestValidateFormatConfigRejectsInvalidFieldLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
	}{
		{name: "empty key", labels: map[string]string{"": "Label"}},
		{name: "empty value", labels: map[string]string{"source": " "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Format.FieldLabels = tt.labels
			if err := validateFormatConfig(cfg); err == nil {
				t.Fatal("validateFormatConfig() error = nil, want invalid field_labels error")
			}
		})
	}
}
