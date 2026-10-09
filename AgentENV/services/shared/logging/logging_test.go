package logging

import "testing"

func TestResolveFormatAutoInteractive(t *testing.T) {
	format, err := resolveFormat("auto", true)
	if err != nil {
		t.Fatalf("resolveFormat returned error: %v", err)
	}
	if format != "console" {
		t.Fatalf("expected console, got %q", format)
	}
}

func TestResolveFormatAutoNonInteractive(t *testing.T) {
	format, err := resolveFormat("auto", false)
	if err != nil {
		t.Fatalf("resolveFormat returned error: %v", err)
	}
	if format != "json" {
		t.Fatalf("expected json, got %q", format)
	}
}

func TestResolveFormatExplicit(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "console", input: "console", want: "console"},
		{name: "json uppercase", input: "JSON", want: "json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, err := resolveFormat(tt.input, false)
			if err != nil {
				t.Fatalf("resolveFormat returned error: %v", err)
			}
			if format != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, format)
			}
		})
	}
}

func TestResolveFormatRejectsUnsupportedValue(t *testing.T) {
	if _, err := resolveFormat("pretty", true); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}
