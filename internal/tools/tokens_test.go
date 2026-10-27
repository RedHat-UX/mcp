// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/redhat-ux/mcp/internal/data"
)

// serviceWithoutThemes builds a store from the fixture tree with the theme
// artifact removed.
func serviceWithoutThemes(t *testing.T) *Service {
	t.Helper()
	indexBytes, err := os.ReadFile("testdata/index.json")
	if err != nil {
		t.Fatalf("reading fixture index: %v", err)
	}
	var index data.Index
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatalf("decoding fixture index: %v", err)
	}

	fsys := fstest.MapFS{}
	filtered := make([]data.Artifact, 0, len(index.Artifacts))
	for _, artifact := range index.Artifacts {
		if artifact.Kind == kindThemes {
			continue
		}
		content, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("reading %s: %v", artifact.Path, err)
		}
		fsys[artifact.Path] = &fstest.MapFile{Data: content}
		filtered = append(filtered, artifact)
	}
	index.Artifacts = filtered
	index.SchemaVersion = data.SchemaVersion
	encoded, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshaling fixture index: %v", err)
	}
	fsys["index.json"] = &fstest.MapFile{Data: encoded}

	store, err := data.Open(fsys)
	if err != nil {
		t.Fatalf("opening fixture store without themes: %v", err)
	}
	return New(store, nil)
}

func TestQueryTokensAliasChain(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token:  "color-interactive",
		Format: "value",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
	}
	tok := out.Tokens[0]
	if tok.Name != "color-interactive" {
		t.Errorf("name = %q, want color-interactive", tok.Name)
	}
	if tok.CSSVariable != "--rh-color-interactive" {
		t.Errorf("css variable = %q, want --rh-color-interactive", tok.CSSVariable)
	}
	if tok.Type != "color" {
		t.Errorf("type = %q, want color", tok.Type)
	}
	if tok.Value != "#ff0000" {
		t.Errorf("value = %q, want #ff0000", tok.Value)
	}
	if tok.RawValue != "{color.brand}" {
		t.Errorf("raw value = %q, want the preserved alias", tok.RawValue)
	}
	wantChain := []string{"color-brand", "color-red-50"}
	if !slices.Equal(tok.AliasChain, wantChain) {
		t.Errorf("alias chain = %v, want %v", tok.AliasChain, wantChain)
	}
	if tok.Source != "json" {
		t.Errorf("source = %q, want json", tok.Source)
	}
}

func TestQueryTokensByCSSVariableName(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "--rh-space-md",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
	}
	if got, want := out.Tokens[0].Value, "1rem"; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
	if got, want := out.Tokens[0].CSSVariable, "--rh-space-md"; got != want {
		t.Errorf("css variable = %q, want %q", got, want)
	}
}

func TestQueryTokensByDotPath(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color.brand",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
	}
	if got, want := out.Tokens[0].Name, "color-brand"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := out.Tokens[0].CSSVariable, "--rh-color-brand"; got != want {
		t.Errorf("css variable = %q, want %q", got, want)
	}
}

func TestQueryTokensSearch(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Query: "brand",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	var names []string
	for _, tok := range out.Tokens {
		names = append(names, tok.Name)
	}
	slices.Sort(names)
	want := []string{"color-brand", "color-red-50", "color-red-60"}
	if !slices.Equal(names, want) {
		t.Errorf("search names = %v, want %v", names, want)
	}
}

func TestQueryTokensFormatCSS(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token:  "color-brand",
		Format: "css",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
	}
	want := "--rh-color-brand: #ff0000;"
	if out.Tokens[0].Value != want {
		t.Errorf("css value = %q, want %q", out.Tokens[0].Value, want)
	}
}

func TestQueryTokensFormatChain(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token:  "color-interactive",
		Format: "chain",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
	}
	want := "color-brand -> color-red-50"
	if out.Tokens[0].Value != want {
		t.Errorf("chain value = %q, want %q", out.Tokens[0].Value, want)
	}
}

func TestQueryTokensThemes(t *testing.T) {
	tests := []struct {
		theme        string
		wantValue    string
		wantAutoWarn bool
		wantSource   string
	}{
		{theme: "light", wantValue: "#ff0000", wantSource: "css/default-theme.css"},
		{theme: "dark", wantValue: "#ff6666", wantSource: "css/default-theme.css"},
		{theme: "auto", wantValue: "#ff0000", wantAutoWarn: true, wantSource: "css/default-theme.css"},
	}
	for _, tt := range tests {
		t.Run(tt.theme, func(t *testing.T) {
			out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
				Token: "color-brand",
				Theme: tt.theme,
			})
			if out.Error != nil {
				t.Fatalf("QueryTokens() error = %+v", out.Error)
			}
			if len(out.Tokens) != 1 {
				t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
			}
			tok := out.Tokens[0]
			if tok.Theme != tt.theme {
				t.Errorf("theme = %q, want %q", tok.Theme, tt.theme)
			}
			if tok.ThemeValue != tt.wantValue {
				t.Errorf("theme value = %q, want %q", tok.ThemeValue, tt.wantValue)
			}
			if tok.Source != tt.wantSource {
				t.Errorf("source = %q, want %q", tok.Source, tt.wantSource)
			}
			foundAuto := false
			for _, warning := range out.Resolved.Warnings {
				if strings.Contains(warning, "auto") {
					foundAuto = true
				}
			}
			if foundAuto != tt.wantAutoWarn {
				t.Errorf("auto warning = %t, want %t (warnings: %v)",
					foundAuto, tt.wantAutoWarn, out.Resolved.Warnings)
			}
		})
	}
}

func TestQueryTokensRejectsInvalidTheme(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-brand",
		Theme: "sepia",
	})
	if out.Error == nil || out.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v, want invalid_argument", out.Error)
	}
}

func TestQueryTokensWarnsWhenThemeDataIsMissing(t *testing.T) {
	out := serviceWithoutThemes(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-brand",
		Theme: "dark",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
	}
	if out.Tokens[0].ThemeValue != "" {
		t.Errorf("theme value = %q, want empty", out.Tokens[0].ThemeValue)
	}
	if out.Tokens[0].Source != "json" {
		t.Errorf("source = %q, want json", out.Tokens[0].Source)
	}
	found := false
	for _, warning := range out.Resolved.Warnings {
		if strings.Contains(warning, "no theme data") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a missing theme data warning", out.Resolved.Warnings)
	}
}

func TestQueryTokensDeprecatedToken(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-legacy",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
	}
	tok := out.Tokens[0]
	if !tok.Deprecated {
		t.Error("color-legacy should be deprecated")
	}
	if tok.Deprecation != "Use color.brand instead." {
		t.Errorf("deprecation message = %q", tok.Deprecation)
	}
	if got, want := tok.Value, "#ff0000"; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
}

func TestQueryTokensAllSorted(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if out.Total != 9 {
		t.Errorf("total = %d, want 9", out.Total)
	}
	if !slices.IsSortedFunc(out.Tokens, func(a, b TokenResult) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	}) {
		t.Error("tokens are not sorted by name")
	}
}

func TestQueryTokensNotFound(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-does-not-exist",
	})
	if out.Error == nil {
		t.Fatal("error = nil, want token_not_found")
	}
	if out.Error.Code != "token_not_found" {
		t.Errorf("error code = %q, want token_not_found", out.Error.Code)
	}
}

func TestQueryTokensRejectsUnknownFormat(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token:  "color-brand",
		Format: "scss",
	})
	if out.Error == nil || out.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v, want invalid_argument", out.Error)
	}
}

func TestQueryTokensWithoutDataReturnsStructuredError(t *testing.T) {
	out := New(nil, data.ErrNoData).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-brand",
	})
	if out.Error == nil || out.Error.Code != "no_bundled_data" {
		t.Fatalf("error = %+v, want no_bundled_data", out.Error)
	}
}
