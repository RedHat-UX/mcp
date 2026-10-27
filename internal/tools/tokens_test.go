// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"slices"
	"testing"

	"bennypowers.dev/asimonim/token"

	"github.com/redhat-ux/mcp/internal/data"
)

func findToken(tokens []TokenResult, name string) (TokenResult, bool) {
	for _, tok := range tokens {
		if tok.Name == name {
			return tok, true
		}
	}
	return TokenResult{}, false
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
		Token: "color.red.50",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
	}
	if got, want := out.Tokens[0].CSSVariable, "--rh-color-red-50"; got != want {
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
	if _, ok := findToken(out.Tokens, "color-brand"); !ok {
		t.Errorf("search for brand did not include color-brand: %+v", out.Tokens)
	}
	if _, ok := findToken(out.Tokens, "color-interactive"); ok {
		// The alias chain mentions color-brand, but the search matches names and
		// descriptions only.
		t.Log("color-interactive also matched, which is acceptable")
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

func TestQueryTokensTheme(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-brand",
		Theme: "dark",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want exactly one", out.Tokens)
	}
	if got, want := out.Tokens[0].ThemeValue, "#ff6666"; got != want {
		t.Errorf("theme value = %q, want %q", got, want)
	}
	if got, want := out.Tokens[0].Theme, "dark"; got != want {
		t.Errorf("theme = %q, want %q", got, want)
	}
}

func TestQueryTokensAllSorted(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if out.Total != 10 {
		t.Errorf("total = %d, want 10", out.Total)
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

func TestAddThemeTokens(t *testing.T) {
	tokens := []*token.Token{{
		Name:  "color-brand",
		Value: "#ff0000",
		Type:  token.TypeColor,
	}}
	themes := map[string]map[string]string{
		"light": {
			"--rh-color-brand":       "#ff0000",
			"--rh-color-accent-base": "#0066cc",
		},
		"dark": {
			"--rh-color-brand":       "#ff6666",
			"--rh-color-accent-base": "#92c5f9",
		},
	}
	got := addThemeTokens(tokens, themes)
	if len(got) != 2 {
		t.Fatalf("addThemeTokens() = %d tokens, want 2", len(got))
	}
	if got[0].Name != "color-brand" {
		t.Errorf("existing token was replaced: %q", got[0].Name)
	}
	if got[1].Name != "color-accent-base" {
		t.Errorf("synthesized token = %q", got[1].Name)
	}
	if got[1].Value != "#0066cc" {
		t.Errorf("synthesized value = %q", got[1].Value)
	}
	if got[1].CSSVariableName() != "--rh-color-accent-base" {
		t.Errorf("synthesized CSS variable = %q", got[1].CSSVariableName())
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

func TestAddThemeTokensIteratesAllThemes(t *testing.T) {
	themes := map[string]map[string]string{
		"dark": {
			"--rh-color-dark-only": "#111111",
		},
		"sepia": {
			"--rh-color-sepia-only": "#222222",
		},
	}
	got := addThemeTokens(nil, themes)
	if len(got) != 2 {
		t.Fatalf("addThemeTokens() = %d tokens, want 2", len(got))
	}
	names := map[string]bool{}
	for _, tok := range got {
		names[tok.Name] = true
	}
	if !names["color-dark-only"] || !names["color-sepia-only"] {
		t.Errorf("names = %v, want dark-only and sepia-only", names)
	}
}
