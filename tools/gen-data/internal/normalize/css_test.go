// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package normalize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractThemeMapFromDefaultTheme(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tokens", "3.x", "default-theme.css"))
	if err != nil {
		t.Fatal(err)
	}
	themes, err := ExtractThemeMap(data)
	if err != nil {
		t.Fatalf("ExtractThemeMap() = %v", err)
	}
	tests := []struct {
		theme string
		name  string
		want  string
	}{
		{"light", "--rh-color-accent-base", "#0066cc"},
		{"dark", "--rh-color-accent-base", "#92c5f9"},
		{"light", "--rh-color-accent-brand", "#ee0000"},
		{"dark", "--rh-color-accent-brand", "#ee0000"},
		{"light", "--rh-color-border-strong", "#151515"},
		{"dark", "--rh-color-border-strong", "#ffffff"},
	}
	for _, tt := range tests {
		if got := themes[tt.theme][tt.name]; got != tt.want {
			t.Errorf("%s %s = %q, want %q", tt.theme, tt.name, got, tt.want)
		}
	}
	if len(themes["light"]) < 50 {
		t.Errorf("light theme has %d entries, want at least 50", len(themes["light"]))
	}
}

func TestExtractThemeMapHandlesLiteralsAndComments(t *testing.T) {
	css := `
/* a comment with : and ; */
:root {
  --rh-space-md: 1rem;
  --rh-color-a:
    light-dark(
      var(--rh-color-a-light, #111111),
      var(--rh-color-a-dark, #eeeeee)
    );
  --rh-shadow: 0 1px 2px rgba(0, 0, 0, 0.2);
}
other {
  --rh-ignored: red;
}
`
	themes, err := ExtractThemeMap([]byte(css))
	if err != nil {
		t.Fatalf("ExtractThemeMap() = %v", err)
	}
	if got := themes["light"]["--rh-space-md"]; got != "1rem" {
		t.Errorf("literal = %q", got)
	}
	if got := themes["dark"]["--rh-space-md"]; got != "1rem" {
		t.Errorf("literal dark = %q", got)
	}
	if got := themes["light"]["--rh-color-a"]; got != "#111111" {
		t.Errorf("light = %q", got)
	}
	if got := themes["dark"]["--rh-color-a"]; got != "#eeeeee" {
		t.Errorf("dark = %q", got)
	}
	if got := themes["light"]["--rh-shadow"]; got != "0 1px 2px rgba(0, 0, 0, 0.2)" {
		t.Errorf("shadow = %q", got)
	}
	if _, ok := themes["light"]["--rh-ignored"]; ok {
		t.Error("parsed a declaration outside :root")
	}
}

func TestExtractThemeMapRejectsMalformedCSS(t *testing.T) {
	if _, err := ExtractThemeMap([]byte(`:root { --x: light-dark(red); }`)); err == nil {
		t.Fatal("ExtractThemeMap() accepted light-dark() with one arm")
	}
}
