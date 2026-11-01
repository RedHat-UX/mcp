// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

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

func TestQueryTokensOmitsDarkOnlyTokenOutsideDarkTheme(t *testing.T) {
	svc := serviceWithThemes(t, []byte(`{
		"light": {"--rh-color-brand": "#ff0000"},
		"dark": {"--rh-color-dark-only": "#111111"}
	}`))
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		theme string
	}{
		{"unthemed", ""},
		{"light", "light"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := svc.QueryTokens(ctx, QueryTokensInput{Query: "dark-only", Theme: tc.theme})
			if out.Error != nil {
				t.Fatalf("QueryTokens() error = %+v", out.Error)
			}
			if len(out.Tokens) != 0 || out.Total != 0 {
				t.Errorf("tokens = %+v, total = %d, want the dark-only token omitted",
					out.Tokens, out.Total)
			}
		})
	}

	dark := svc.QueryTokens(ctx, QueryTokensInput{Query: "dark-only", Theme: "dark"})
	if dark.Error != nil {
		t.Fatalf("dark QueryTokens() error = %+v", dark.Error)
	}
	if len(dark.Tokens) != 1 {
		t.Fatalf("dark tokens = %+v, want one dark-only token", dark.Tokens)
	}
	if got, want := dark.Tokens[0].Name, "color-dark-only"; got != want {
		t.Errorf("token name = %q, want %q", got, want)
	}
	if got, want := dark.Tokens[0].Value, "#111111"; got != want {
		t.Errorf("dark token base value = %q, want %q", got, want)
	}
	if got, want := dark.Tokens[0].ThemeValue, "#111111"; got != want {
		t.Errorf("dark token theme value = %q, want %q", got, want)
	}
}

func TestQueryTokensExactDarkOnlyTokenRequiresDarkTheme(t *testing.T) {
	svc := serviceWithThemes(t, []byte(`{
		"light": {"--rh-color-brand": "#ff0000"},
		"dark": {"--rh-color-dark-only": "#111111"}
	}`))
	ctx := context.Background()

	for _, theme := range []string{"", "light"} {
		out := svc.QueryTokens(ctx, QueryTokensInput{Token: "color-dark-only", Theme: theme})
		if out.Error == nil || out.Error.Code != "token_not_found" {
			t.Fatalf("theme %q error = %+v, want token_not_found", theme, out.Error)
		}
		if !strings.Contains(out.Error.Hint, "dark") {
			t.Errorf("theme %q hint = %q, want the defining theme", theme, out.Error.Hint)
		}
	}

	out := svc.QueryTokens(ctx, QueryTokensInput{Token: "color-dark-only", Theme: "dark"})
	if out.Error != nil {
		t.Fatalf("dark error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 || out.Tokens[0].Value != "#111111" {
		t.Errorf("dark token = %+v, want #111111", out.Tokens)
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

// serviceWithThemes builds a store from the fixture tree, replacing the theme
// artifact contents with themesJSON.
func serviceWithThemes(t *testing.T, themesJSON []byte) *Service {
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
	for i := range index.Artifacts {
		artifact := &index.Artifacts[i]
		content, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("reading %s: %v", artifact.Path, err)
		}
		if artifact.Kind == kindThemes {
			content = themesJSON
			sum := sha256.Sum256(content)
			artifact.SHA256 = hex.EncodeToString(sum[:])
			artifact.Bytes = int64(len(content))
		}
		fsys[artifact.Path] = &fstest.MapFile{Data: content}
	}
	index.SchemaVersion = data.SchemaVersion
	encoded, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshaling fixture index: %v", err)
	}
	fsys["index.json"] = &fstest.MapFile{Data: encoded}

	store, err := data.Open(fsys)
	if err != nil {
		t.Fatalf("opening fixture store: %v", err)
	}
	return New(store, nil)
}

// Phase C loads the theme map while building the token set so light-dark()
// composites can be synthesized. It must still parse the map once per version.
func TestQueryTokensParsesThemesOnce(t *testing.T) {
	svc := fixtureService(t)
	for range 2 {
		out := svc.QueryTokens(context.Background(), QueryTokensInput{Token: "color-brand"})
		if out.Error != nil {
			t.Fatalf("QueryTokens() error = %+v", out.Error)
		}
	}
	if got := svc.themeParses.Load(); got != 1 {
		t.Errorf("theme parses = %d, want 1", got)
	}
}

func TestQueryTokensIgnoresMalformedThemesWhenNotRequested(t *testing.T) {
	svc := serviceWithThemes(t, []byte(`{"not":"a theme map"}`))

	out := svc.QueryTokens(context.Background(), QueryTokensInput{Token: "color-brand"})
	if out.Error != nil {
		t.Fatalf("QueryTokens() without theme error = %+v, want none", out.Error)
	}

	out = svc.QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-brand",
		Theme: "light",
	})
	if out.Error == nil || out.Error.Code != "invalid_themes" {
		t.Fatalf("QueryTokens() with malformed themes error = %+v, want invalid_themes", out.Error)
	}
}

func TestQueryTokensAcceptsVPrefixedVersion(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token:   "color-brand",
		Version: "v3.2.0",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if out.Resolved.Tokens != "3.2.0" {
		t.Errorf("resolved tokens version = %q, want 3.2.0", out.Resolved.Tokens)
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

func TestQueryTokensIncludesDescriptionAndDeprecation(t *testing.T) {
	svc := fixtureService(t)
	summary := svc.QueryTokens(context.Background(), QueryTokensInput{Token: "color-legacy"})
	if summary.Error != nil {
		t.Fatalf("summary error = %+v", summary.Error)
	}
	if len(summary.Tokens) != 1 {
		t.Fatalf("summary tokens = %+v, want one", summary.Tokens)
	}
	if summary.Tokens[0].Description == "" {
		t.Error("summary detail omitted the description")
	}
	if summary.Tokens[0].Deprecation == "" {
		t.Error("summary detail omitted the deprecation message")
	}

	full := svc.QueryTokens(context.Background(), QueryTokensInput{
		Token:  "color-legacy",
		Detail: "full",
	})
	if full.Error != nil {
		t.Fatalf("full error = %+v", full.Error)
	}
	if len(full.Tokens) != 1 {
		t.Fatalf("full tokens = %+v, want one", full.Tokens)
	}
	tok := full.Tokens[0]
	if tok.Description == "" {
		t.Error("full detail omitted the description")
	}
	if !tok.Deprecated {
		t.Error("full detail did not mark the token deprecated")
	}
	if tok.Deprecation == "" {
		t.Error("full detail omitted the deprecation message")
	}
}

// A direct service caller has no workspace cascade. A cursor must resolve the
// version that produced it instead of the latest bundled tokens release.
func TestQueryTokensCursorKeepsOlderVersion(t *testing.T) {
	svc := fixtureService(t)
	first := svc.QueryTokens(context.Background(), QueryTokensInput{
		Version:  "1.1.2",
		PageSize: 1,
	})
	if first.Error != nil {
		t.Fatalf("first page error = %+v", first.Error)
	}
	if first.NextCursor == "" {
		t.Fatal("nextCursor = empty, want a second page")
	}
	second := svc.QueryTokens(context.Background(), QueryTokensInput{
		Cursor:   first.NextCursor,
		PageSize: 1,
	})
	if second.Error != nil {
		t.Fatalf("second page error = %+v", second.Error)
	}
	if second.Resolved.Tokens != "1.1.2" {
		t.Errorf("second page tokens version = %q, want 1.1.2", second.Resolved.Tokens)
	}
	if second.Resolved.Source != "cursor" {
		t.Errorf("source = %q, want cursor", second.Resolved.Source)
	}
	if len(second.Tokens) != 1 {
		t.Errorf("second page tokens = %d, want 1", len(second.Tokens))
	}
}
