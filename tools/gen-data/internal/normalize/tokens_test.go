// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package normalize

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bennypowers.dev/asimonim/parser"
	"bennypowers.dev/asimonim/resolver"
	"bennypowers.dev/asimonim/schema"
	"bennypowers.dev/asimonim/token"
	"bennypowers.dev/asimonim/validator"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

func loadFixture(t *testing.T, major string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tokens", major, "rhds.tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func findToken(t *testing.T, tokens []*token.Token, name string) *token.Token {
	t.Helper()
	for _, tok := range tokens {
		if tok.Name == name {
			return tok
		}
	}
	t.Fatalf("token %q not found", name)
	return nil
}

func parseNormalized(t *testing.T, normalized []byte) []*token.Token {
	t.Helper()
	version, err := schema.FromString("2025.10")
	if err != nil {
		t.Fatal(err)
	}
	// Draft is detected from the normalized document itself; the version
	// argument is only a fallback.
	_ = version
	detected := schema.Draft
	tokens, err := parser.NewJSONParser().Parse(normalized, parser.Options{
		Prefix:        "rh",
		SkipPositions: true,
		SchemaVersion: detected,
	})
	if err != nil {
		t.Fatalf("parsing normalized tokens: %v", err)
	}
	if err := resolver.ResolveAliases(tokens, detected); err != nil {
		t.Fatalf("resolving aliases: %v", err)
	}
	return tokens
}

func TestNormalizeEachMajor(t *testing.T) {
	for _, major := range []string{"1.x", "2.x", "3.x"} {
		t.Run(major, func(t *testing.T) {
			input := loadFixture(t, major)
			result, err := NormalizeTokens(input)
			if err != nil {
				t.Fatalf("NormalizeTokens() = %v", err)
			}
			if len(result.Document) == 0 {
				t.Fatal("NormalizeTokens() returned an empty document")
			}
			if result.Themes == nil {
				t.Fatal("NormalizeTokens() returned nil themes")
			}

			// Determinism.
			again, err := NormalizeTokens(input)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(result.Document, again.Document) {
				t.Fatal("NormalizeTokens() is not deterministic")
			}

			// The validator must accept the normalized document.
			problems := validator.ValidateConsistency(result.Document, schema.Draft)
			if len(problems) > 0 {
				t.Fatalf("validator rejected normalized tokens:\n%v", problems)
			}

			// Parsing succeeds and every token is typed.
			tokens := parseNormalized(t, result.Document)
			if len(tokens) < 300 {
				t.Fatalf("parsed %d tokens, want at least 300", len(tokens))
			}
			for _, tok := range tokens {
				if tok.Type == "" {
					t.Errorf("token %q has no $type after normalization", tok.Name)
				}
				if tok.CSSVariableName() == "" {
					t.Errorf("token %q has no CSS variable name", tok.Name)
				}
			}

			// Aliases survive from original.$value.
			border := findToken(t, tokens, "border-width-sm")
			if border.RawValue != "{length.4xs}" {
				t.Errorf("border-width-sm raw value = %#v, want the original alias", border.RawValue)
			}
			if len(border.ResolutionChain) == 0 {
				t.Errorf("border-width-sm alias chain is empty")
			}
			if border.DisplayValue() != "1px" {
				t.Errorf("border-width-sm display value = %q, want 1px", border.DisplayValue())
			}

			// The prefix produces RHDS CSS names.
			if got := border.CSSVariableName(); got != "--rh-border-width-sm" {
				t.Errorf("CSSVariableName() = %q, want --rh-border-width-sm", got)
			}

			// Missing $type is recovered from attributes.type.
			radius := findToken(t, tokens, "border-radius-default")
			if radius.Type != token.TypeDimension {
				t.Errorf("border-radius-default type = %q, want dimension", radius.Type)
			}
			opacity := findToken(t, tokens, "opacity-50")
			if opacity.Type != token.TypeNumber {
				t.Errorf("opacity-50 type = %q, want number", opacity.Type)
			}
			if got, want := opacity.DisplayValue(), "0.5"; got != want {
				t.Errorf("opacity-50 display = %q, want %q", got, want)
			}
			lineHeight := findToken(t, tokens, "line-height-body-text")
			if lineHeight.Type != token.TypeNumber {
				t.Errorf("line-height-body-text type = %q, want number", lineHeight.Type)
			}

			// Generated color variants stay in DTCG as valid hex colors.
			rgb := findToken(t, tokens, "color-gray-90-rgb")
			if rgb.Type != token.TypeColor {
				t.Errorf("color-gray-90-rgb type = %q, want color", rgb.Type)
			}
			if got := rgb.DisplayValue(); !isHexColor(got) {
				t.Errorf("color-gray-90-rgb value = %q, want a hex color", got)
			}

			// mediaQuery is an RHDS vendor type outside DTCG.
			if got := result.Stats.Types["mediaQuery"]; got != 6 {
				t.Errorf("mediaQuery token count = %d, want 6", got)
			}
			media := findToken(t, tokens, "media-2xl")
			if media.Type != "mediaQuery" {
				t.Errorf("media-2xl type = %q, want mediaQuery", media.Type)
			}
			vendor, ok := media.Extensions["com.rhds.mcp"].(map[string]any)
			if !ok || vendor["vendorType"] != "mediaQuery" {
				t.Errorf("media-2xl vendor extension = %#v, want vendorType mediaQuery", media.Extensions)
			}
			if _, exists := media.Extensions["com.style-dictionary"]; exists {
				t.Errorf("media-2xl was re-typed as a DTCG token: %#v", media.Extensions)
			}

			// A draft group-marker token ("_") is flattened to the published
			// name instead of emitting a trailing underscore.
			subtle := findToken(t, tokens, "color-icon-subtle")
			if got, want := subtle.CSSVariableName(), "--rh-color-icon-subtle"; got != want {
				t.Errorf("color-icon-subtle CSS variable = %q, want %q", got, want)
			}
			if raw, ok := subtle.RawValue.(string); !ok || !strings.HasPrefix(raw, "{color.gray.") {
				t.Errorf("color-icon-subtle raw value = %#v, want a gray alias", subtle.RawValue)
			}
			findToken(t, tokens, "color-icon-subtle-hover")

			// Descriptions and extensions survive.
			breakpoint := findToken(t, tokens, "breakpoint-xs")
			if breakpoint.Description == "" {
				t.Error("breakpoint-xs has no description")
			}
			if breakpoint.Extensions == nil || breakpoint.Extensions["com.redhat.ux"] == nil {
				t.Errorf("breakpoint-xs extensions = %#v, want com.redhat.ux", breakpoint.Extensions)
			}
			// Style Dictionary kept camelCase keys for max-width breakpoints.
			maxBreakpoint := findToken(t, tokens, "breakpoint-2xs-max")
			if got, want := maxBreakpoint.CSSVariableName(), "--rh-breakpoint-2xs-max"; got != want {
				t.Errorf("2xs max CSS variable = %q, want %q", got, want)
			}

			// light-dark composites are excluded from DTCG and appear in the
			// theme map instead.
			switch major {
			case "2.x":
				if got := result.Themes["light"]["--rh-color-accent-base"]; got != "#0066cc" {
					t.Errorf("2.x light theme color-accent-base = %q, want #0066cc", got)
				}
				if got := result.Themes["dark"]["--rh-color-accent-base"]; got != "#92c5f9" {
					t.Errorf("2.x dark theme color-accent-base = %q, want #92c5f9", got)
				}
			case "3.x":
				for _, tok := range tokens {
					if tok.Name == "color-accent-base" {
						t.Error("light-dark composite color-accent-base leaked into DTCG")
					}
					if strings.Contains(tok.DisplayValue(), "light-dark(") {
						t.Errorf("token %q still contains a light-dark() expression", tok.Name)
					}
				}
				if got := result.Themes["light"]["--rh-color-accent-base"]; got != "#0066cc" {
					t.Errorf("light theme color-accent-base = %q, want #0066cc", got)
				}
				if got := result.Themes["dark"]["--rh-color-accent-base"]; got != "#92c5f9" {
					t.Errorf("dark theme color-accent-base = %q, want #92c5f9", got)
				}
			}

			// Style Dictionary fields do not survive.
			if bytes.Contains(result.Document, []byte(`"filePath"`)) ||
				bytes.Contains(result.Document, []byte(`"isSource"`)) ||
				bytes.Contains(result.Document, []byte(`"original"`)) {
				t.Error("normalized document still contains Style Dictionary fields")
			}

			// Golden file.
			goldenPath := filepath.Join("..", "..", "testdata", "tokens", major, "expected.json")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, result.Document, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			golden, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("reading golden file (run with -update to create it): %v", err)
			}
			if !bytes.Equal(result.Document, golden) {
				t.Errorf("%s does not match the golden file (run go test ./tools/gen-data/internal/normalize -update)", goldenPath)
			}
		})
	}
}

func TestNormalizedNamesMatchPublishedCSS(t *testing.T) {
	input := loadFixture(t, "3.x")
	result, err := NormalizeTokens(input)
	if err != nil {
		t.Fatal(err)
	}
	tokens := parseNormalized(t, result.Document)
	got := make(map[string]bool, len(tokens))
	for _, tok := range tokens {
		got[tok.CSSVariableName()] = true
	}

	css, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tokens", "3.x", "global.css"))
	if err != nil {
		t.Fatal(err)
	}
	want := cssCustomProperties(string(css))
	if len(got) != len(want) {
		t.Errorf("normalized token names = %d, published CSS properties = %d", len(got), len(want))
	}
	for name := range want {
		if !got[name] {
			t.Errorf("published CSS property %s is missing from normalized tokens", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("normalized token %s is missing from published CSS", name)
		}
	}
}

func cssCustomProperties(css string) map[string]bool {
	result := make(map[string]bool)
	for offset := 0; offset < len(css); {
		index := strings.Index(css[offset:], "--rh-")
		if index < 0 {
			break
		}
		start := offset + index
		end := start
		for end < len(css) {
			c := css[end]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_' {
				end++
				continue
			}
			break
		}
		result[css[start:end]] = true
		offset = end
	}
	return result
}

func TestNormalizeRejectsMalformedJSON(t *testing.T) {
	if _, err := NormalizeTokens([]byte("{")); err == nil {
		t.Fatal("NormalizeTokens() accepted malformed JSON")
	}
}

func TestNormalizeRejectsTokenWithoutType(t *testing.T) {
	input := []byte(`{
		"mystery": {"$value": "1"}
	}`)
	if _, err := NormalizeTokens(input); err == nil {
		t.Fatal("NormalizeTokens() accepted a token with no derivable type")
	}
}

func TestNormalizeShadowValue(t *testing.T) {
	input := []byte(`{
		"box-shadow": {
			"sm": {
				"$type": "shadow",
				"$value": "0 2px 4px 0 rgba(21, 21, 21, 0.2)",
				"original": {
					"$type": "shadow",
					"$value": {
						"offsetX": "0",
						"offsetY": "{length.3xs}",
						"blur": "{length.xs}",
						"spread": 0,
						"color": "rgba(21, 21, 21, 0.2)"
					}
				},
				"attributes": {"category": "box-shadow"}
			}
		},
		"length": {
			"3xs": {"$type": "dimension", "$value": "2px"},
			"xs": {"$type": "dimension", "$value": "4px"}
		}
	}`)
	result, err := NormalizeTokens(input)
	if err != nil {
		t.Fatalf("NormalizeTokens() = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(result.Document, &doc); err != nil {
		t.Fatal(err)
	}
	shadow := doc["box-shadow"].(map[string]any)["sm"].(map[string]any)
	value := shadow["$value"].(map[string]any)
	if value["offsetX"] != "0px" {
		t.Errorf("offsetX = %#v, want 0px", value["offsetX"])
	}
	if value["offsetY"] != "{length.3xs}" {
		t.Errorf("offsetY = %#v, want the original alias", value["offsetY"])
	}
	if value["color"] != "#15151533" {
		t.Errorf("color = %#v, want #15151533", value["color"])
	}
}

func hasToken(tokens []*token.Token, name string) bool {
	for _, tok := range tokens {
		if tok.Name == name {
			return true
		}
	}
	return false
}

func TestMediaQueryVendorTypeIsPreserved(t *testing.T) {
	input := []byte(`{
		"breakpoint": {"sm": {"$type": "dimension", "$value": "768px"}},
		"media": {"sm": {"$type": "mediaQuery", "$value": {"min-width": "{breakpoint.sm}"}}}
	}`)
	result, err := NormalizeTokens(input)
	if err != nil {
		t.Fatalf("NormalizeTokens() = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(result.Document, &doc); err != nil {
		t.Fatal(err)
	}
	media := doc["media"].(map[string]any)["sm"].(map[string]any)
	if media["$type"] != "mediaQuery" {
		t.Errorf("$type = %#v, want mediaQuery", media["$type"])
	}
	value, ok := media["$value"].(map[string]any)
	if !ok {
		t.Fatalf("$value = %#v, want an object", media["$value"])
	}
	if value["min-width"] != "{breakpoint.sm}" {
		t.Errorf("min-width = %#v, want the unresolved reference", value["min-width"])
	}
	extensions := media["$extensions"].(map[string]any)
	vendor := extensions["com.rhds.mcp"].(map[string]any)
	if vendor["vendorType"] != "mediaQuery" {
		t.Errorf("vendor extension = %#v", extensions)
	}
	if _, exists := extensions["com.style-dictionary"]; exists {
		t.Error("mediaQuery token was re-typed as a DTCG token")
	}

	// asimonim does not resolve references nested inside composite objects.
	tokens := parseNormalized(t, result.Document)
	mediaToken := findToken(t, tokens, "media-sm")
	raw, ok := mediaToken.RawValue.(map[string]any)
	if !ok || raw["min-width"] != "{breakpoint.sm}" {
		t.Errorf("raw media value = %#v, want the unresolved reference", mediaToken.RawValue)
	}
}

func TestSurfacePaletteIsNotAToken(t *testing.T) {
	for _, major := range []string{"1.x", "2.x", "3.x"} {
		t.Run(major, func(t *testing.T) {
			result, err := NormalizeTokens(loadFixture(t, major))
			if err != nil {
				t.Fatal(err)
			}
			tokens := parseNormalized(t, result.Document)
			// rh-surface palettes are prose, not a light/dark token. The
			// six-arm marker is dropped from DTCG by design.
			if hasToken(tokens, "color-surface") {
				t.Error("color-surface palette leaked into DTCG")
			}
			if !hasToken(tokens, "color-surface-lightest") && !hasToken(tokens, "color-surface-light") {
				t.Error("surface palette children are missing")
			}
		})
	}
}

func TestCanonicalCubicBezierStringRequiresFourValues(t *testing.T) {
	bad := []byte(`{"animation":{"timing":{"$type":"cubicBezier","$value":"cubic-bezier(0.1, 0.2, 0.3)"}}}`)
	if _, err := NormalizeTokens(bad); err == nil {
		t.Fatal("NormalizeTokens() accepted a three-value cubic-bezier()")
	}
	good := []byte(`{"animation":{"timing":{"$type":"cubicBezier","$value":"cubic-bezier(0.1, 0.2, 0.3, 0.4)"}}}`)
	result, err := NormalizeTokens(good)
	if err != nil {
		t.Fatalf("NormalizeTokens() = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(result.Document, &doc); err != nil {
		t.Fatal(err)
	}
	value := doc["animation"].(map[string]any)["timing"].(map[string]any)["$value"].([]any)
	if len(value) != 4 {
		t.Fatalf("cubicBezier value = %v, want four numbers", value)
	}
}

func TestCanonicalColor(t *testing.T) {
	tests := []struct {
		value string
		hex   string
		alpha float64
		want  string
	}{
		{"#FFF", "", 1, "#ffffff"},
		{"#abc", "", 1, "#aabbcc"},
		{"#ABCDEF", "", 1, "#abcdef"},
		{"rgba(21, 21, 21, 0.2)", "151515", 0.2, "#15151533"},
		{"0 0% 12.15%", "1f1f1f", 1, "#1f1f1f"},
		{"233 247 223", "e9f7df", 1, "#e9f7df"},
	}
	for _, tt := range tests {
		attrs := map[string]any{}
		if tt.hex != "" {
			attrs["hex"] = tt.hex
			attrs["rgb"] = map[string]any{"a": tt.alpha}
		}
		got, err := CanonicalColor(tt.value, attrs)
		if err != nil {
			t.Errorf("CanonicalColor(%q) = %v", tt.value, err)
			continue
		}
		if got != tt.want {
			t.Errorf("CanonicalColor(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}
	if _, err := CanonicalColor("not-a-color", nil); err == nil {
		t.Fatal("CanonicalColor() accepted an invalid color")
	}
}

func isHexColor(value string) bool {
	if len(value) != 7 && len(value) != 9 {
		return false
	}
	if value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
