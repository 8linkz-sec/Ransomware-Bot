package keywordmode

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		want    string
		wantErr bool
	}{
		{name: "empty defaults to literal", want: Literal},
		{name: "literal", mode: "literal", want: Literal},
		{name: "regex mixed case", mode: "ReGeX", want: Regex},
		{name: "regex with whitespace", mode: " regex ", want: Regex},
		{name: "invalid", mode: "glob", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.mode)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Normalize() succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Normalize() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsRegex(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want bool
	}{
		{name: "regex", mode: "regex", want: true},
		{name: "regex mixed case with whitespace", mode: " ReGeX ", want: true},
		{name: "literal", mode: "literal", want: false},
		{name: "empty defaults to literal", mode: "", want: false},
		{name: "invalid mode", mode: "glob", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRegex(tt.mode); got != tt.want {
				t.Fatalf("IsRegex(%q) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}
