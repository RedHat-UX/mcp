// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/redhat-ux/mcp/internal/data"
)

func readMarkupFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile("testdata/markup/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(content)
}

func findIssue(issues []MarkupIssue, rule string) (MarkupIssue, bool) {
	for _, issue := range issues {
		if issue.Rule == rule {
			return issue, true
		}
	}
	return MarkupIssue{}, false
}

func TestValidateMarkupValidHTML(t *testing.T) {
	out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: readMarkupFixture(t, "valid.html"),
		Format: "html",
	})
	if out.Error != nil {
		t.Fatalf("ValidateMarkup() error = %+v", out.Error)
	}
	if len(out.Issues) != 0 {
		t.Errorf("issues = %+v, want none", out.Issues)
	}
	if out.Resolved.Elements != "5.0.0" {
		t.Errorf("resolved version = %q, want 5.0.0", out.Resolved.Elements)
	}
}

func TestValidateMarkupInvalidHTML(t *testing.T) {
	out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: readMarkupFixture(t, "invalid.html"),
		Format: "html",
	})
	if out.Error != nil {
		t.Fatalf("ValidateMarkup() error = %+v", out.Error)
	}

	unknownTag, ok := findIssue(out.Issues, "unknown-tag")
	if !ok {
		t.Fatalf("missing unknown-tag issue: %+v", out.Issues)
	}
	if unknownTag.Element != "rh-alert" {
		t.Errorf("unknown-tag element = %q, want rh-alert", unknownTag.Element)
	}
	if unknownTag.Confidence != "high" {
		t.Errorf("unknown-tag confidence = %q, want high", unknownTag.Confidence)
	}
	if unknownTag.Line == 0 || unknownTag.Column == 0 {
		t.Errorf("unknown-tag has no position: %+v", unknownTag)
	}

	unknownAttr, ok := findIssue(out.Issues, "unknown-attribute")
	if !ok {
		t.Fatalf("missing unknown-attribute issue: %+v", out.Issues)
	}
	if unknownAttr.Element != "rh-button" {
		t.Errorf("unknown-attribute element = %q, want rh-button", unknownAttr.Element)
	}
	if !strings.Contains(unknownAttr.Message, "bogus-attribute") {
		t.Errorf("unknown-attribute message = %q", unknownAttr.Message)
	}

	invalidValue, ok := findIssue(out.Issues, "invalid-attribute-value")
	if !ok {
		t.Fatalf("missing invalid-attribute-value issue: %+v", out.Issues)
	}
	if !strings.Contains(invalidValue.Message, "huge") {
		t.Errorf("invalid value message = %q", invalidValue.Message)
	}

	if _, ok := findIssue(out.Issues, "deprecated-attribute"); !ok {
		t.Errorf("missing deprecated-attribute issue: %+v", out.Issues)
	}
	if _, ok := findIssue(out.Issues, "deprecated-element"); !ok {
		t.Errorf("missing deprecated-element issue: %+v", out.Issues)
	}
}

func TestValidateMarkupValidTwig(t *testing.T) {
	out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: readMarkupFixture(t, "valid.twig"),
		Format: "twig",
	})
	if out.Error != nil {
		t.Fatalf("ValidateMarkup() error = %+v", out.Error)
	}
	if len(out.Issues) != 0 {
		t.Errorf("issues = %+v, want none", out.Issues)
	}
	for _, warning := range out.Warnings {
		if strings.Contains(warning, "unbalanced") {
			t.Errorf("balanced Twig reported as unbalanced: %q", warning)
		}
	}
}

func TestValidateMarkupUnbalancedTwigDegradesToWarnings(t *testing.T) {
	out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: readMarkupFixture(t, "unbalanced.twig"),
		Format: "twig",
	})
	if out.Error != nil {
		t.Fatalf("ValidateMarkup() error = %+v", out.Error)
	}

	foundWarning := false
	for _, warning := range out.Warnings {
		if strings.Contains(warning, "unbalanced") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("warnings = %v, want an unbalanced control flow warning", out.Warnings)
	}

	unknownAttr, ok := findIssue(out.Issues, "unknown-attribute")
	if !ok {
		t.Fatalf("missing unknown-attribute issue for unbalanced Twig: %+v", out.Issues)
	}
	if unknownAttr.Element != "rh-button" {
		t.Errorf("unknown-attribute element = %q, want rh-button", unknownAttr.Element)
	}
	// Structure is uncertain, so structural findings must not claim high confidence.
	if unknownAttr.Confidence == "high" {
		t.Errorf("confidence = %q, want degraded confidence for unbalanced Twig", unknownAttr.Confidence)
	}

	unknownTag, ok := findIssue(out.Issues, "unknown-tag")
	if !ok {
		t.Fatalf("missing unknown-tag issue for unbalanced Twig: %+v", out.Issues)
	}
	if unknownTag.Severity != "warning" || unknownTag.Confidence != "low" {
		t.Errorf("unknown-tag severity/confidence = %q/%q, want warning/low",
			unknownTag.Severity, unknownTag.Confidence)
	}
}

func TestValidateMarkupIsCaseInsensitive(t *testing.T) {
	out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: `<RH-BUTTON VARIANT="huge" BOGUS-ATTRIBUTE="yes">Save</RH-BUTTON><RH-ALERT>Unknown</RH-ALERT>`,
		Format: "html",
	})
	if out.Error != nil {
		t.Fatalf("ValidateMarkup() error = %+v", out.Error)
	}

	unknownTag, ok := findIssue(out.Issues, "unknown-tag")
	if !ok {
		t.Fatalf("missing unknown-tag issue: %+v", out.Issues)
	}
	if unknownTag.Element != "rh-alert" {
		t.Errorf("unknown-tag element = %q, want rh-alert", unknownTag.Element)
	}

	unknownAttributeCount := 0
	for _, issue := range out.Issues {
		if issue.Rule != "unknown-attribute" {
			continue
		}
		unknownAttributeCount++
		if issue.Attribute != "bogus-attribute" {
			t.Errorf("unknown attribute = %q, want bogus-attribute", issue.Attribute)
		}
	}
	if unknownAttributeCount != 1 {
		t.Errorf("unknown-attribute issues = %d, want 1 (VARIANT must be recognized)", unknownAttributeCount)
	}

	invalidValue, ok := findIssue(out.Issues, "invalid-attribute-value")
	if !ok {
		t.Fatalf("missing invalid-attribute-value issue: %+v", out.Issues)
	}
	if invalidValue.Attribute != "variant" {
		t.Errorf("invalid value attribute = %q, want variant", invalidValue.Attribute)
	}
}

// singleQuotedManifest rewrites the fixture's double-quoted union literals to
// single quotes, matching what the published @rhds/elements manifests contain.
func singleQuotedManifest(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/elements/custom-elements.json")
	if err != nil {
		t.Fatalf("reading fixture manifest: %v", err)
	}
	replaced := strings.ReplaceAll(string(raw),
		`\"primary\" | \"secondary\" | \"tertiary\" | \"danger\" | \"link\"`,
		`'primary' | 'secondary' | 'tertiary' | 'danger' | 'link'`)
	if replaced == string(raw) {
		t.Fatal("fixture manifest did not contain the expected union type; update the test")
	}
	return []byte(replaced)
}

func TestValidateMarkupAcceptsSingleQuotedEnumValues(t *testing.T) {
	// The fixture mirrors the published @rhds/elements rh-button declaration,
	// whose union literals use single quotes.
	svc := memoryService(t, singleQuotedManifest(t), "5.0.0")

	for _, value := range []string{"primary", "tertiary"} {
		valid := svc.ValidateMarkup(context.Background(), ValidateMarkupInput{
			Markup: `<rh-button variant="` + value + `"></rh-button>`,
		})
		if valid.Error != nil {
			t.Fatalf("ValidateMarkup(%q) error = %+v", value, valid.Error)
		}
		if issue, ok := findIssue(valid.Issues, "invalid-attribute-value"); ok {
			t.Errorf("valid value %q flagged invalid: %+v", value, issue)
		}
	}

	invalid := svc.ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: `<rh-button variant="bogus"></rh-button>`,
	})
	issue, ok := findIssue(invalid.Issues, "invalid-attribute-value")
	if !ok {
		t.Fatalf("invalid value not flagged: %+v", invalid.Issues)
	}
	// cem's DisplayEnumValues re-quotes string literals, so diagnostics show
	// the double-quoted form regardless of the manifest's source quote style.
	want := `"bogus" is not a valid value for rh-button attribute "variant"; valid values: ["primary" "secondary" "tertiary" "danger" "link"]`
	if issue.Message != want {
		t.Errorf("message = %q, want %q", issue.Message, want)
	}
}

// manifestForAttributeType builds a minimal custom-elements manifest with one
// element exposing a single attribute of the given TypeScript type.
func manifestForAttributeType(t *testing.T, tagName, attributeName, typeText string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": "2.1.0",
		"modules": []any{map[string]any{
			"kind": "javascript-module",
			"path": "./" + tagName + ".js",
			"declarations": []any{map[string]any{
				"kind":          "class",
				"customElement": true,
				"name":          "TestElement",
				"tagName":       tagName,
				"attributes": []any{map[string]any{
					"name": attributeName,
					"type": map[string]any{"text": typeText},
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("marshaling test manifest: %v", err)
	}
	return raw
}

func TestValidateMarkupLiteralUnions(t *testing.T) {
	tests := []struct {
		name     string
		typeText string
		valid    []string
		invalid  string
		labels   string
	}{
		{
			name:     "single quoted",
			typeText: `'primary' | 'secondary' | 'tertiary'`,
			valid:    []string{"primary", "tertiary"},
			invalid:  "bogus",
			labels:   `["primary" "secondary" "tertiary"]`,
		},
		{
			name:     "double quoted",
			typeText: `"primary" | "secondary" | "tertiary"`,
			valid:    []string{"primary", "tertiary"},
			invalid:  "bogus",
			labels:   `["primary" "secondary" "tertiary"]`,
		},
		{
			name:     "multiline leading pipe",
			typeText: "\n    | 'primary'\n    | 'secondary'\n    | 'tertiary'",
			valid:    []string{"primary", "tertiary"},
			invalid:  "bogus",
			labels:   `["primary" "secondary" "tertiary"]`,
		},
		{
			name:     "numeric",
			typeText: `1 | 2 | 3`,
			valid:    []string{"1", "3"},
			invalid:  "4",
			labels:   `[1 2 3]`,
		},
		{
			name:     "boolean",
			typeText: `true | false`,
			valid:    []string{"true", "false"},
			invalid:  "yes",
			labels:   `[true false]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := memoryService(t, manifestForAttributeType(t, "rh-test", "kind", tt.typeText), "5.0.0")

			for _, value := range tt.valid {
				out := svc.ValidateMarkup(context.Background(), ValidateMarkupInput{
					Markup: fmt.Sprintf(`<rh-test kind="%s"></rh-test>`, value),
				})
				if out.Error != nil {
					t.Fatalf("ValidateMarkup(%q) error = %+v", value, out.Error)
				}
				if issue, ok := findIssue(out.Issues, "invalid-attribute-value"); ok {
					t.Errorf("valid value %q flagged invalid: %+v", value, issue)
				}
			}

			out := svc.ValidateMarkup(context.Background(), ValidateMarkupInput{
				Markup: fmt.Sprintf(`<rh-test kind="%s"></rh-test>`, tt.invalid),
			})
			if out.Error != nil {
				t.Fatalf("ValidateMarkup(%q) error = %+v", tt.invalid, out.Error)
			}
			issue, ok := findIssue(out.Issues, "invalid-attribute-value")
			if !ok {
				t.Fatalf("invalid value %q not flagged: %+v", tt.invalid, out.Issues)
			}
			want := fmt.Sprintf(`%q is not a valid value for rh-test attribute "kind"; valid values: %s`,
				tt.invalid, tt.labels)
			if issue.Message != want {
				t.Errorf("message = %q, want %q", issue.Message, want)
			}
		})
	}
}

func TestValidateMarkupRejectsNullForClosedEnum(t *testing.T) {
	svc := memoryService(t, manifestForAttributeType(t, "rh-test", "size", `'sm' | null`), "5.0.0")

	valid := svc.ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: `<rh-test size="sm"></rh-test>`,
	})
	if valid.Error != nil {
		t.Fatalf("ValidateMarkup(sm) error = %+v", valid.Error)
	}
	if issue, ok := findIssue(valid.Issues, "invalid-attribute-value"); ok {
		t.Errorf(`valid value "sm" flagged: %+v`, issue)
	}

	out := svc.ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: `<rh-test size="null"></rh-test>`,
	})
	if out.Error != nil {
		t.Fatalf("ValidateMarkup() error = %+v", out.Error)
	}
	issue, ok := findIssue(out.Issues, "invalid-attribute-value")
	if !ok {
		t.Fatalf(`null not flagged for 'sm' | null: %+v`, out.Issues)
	}
	want := `"null" is not a valid value for rh-test attribute "size"; valid values: ["sm"]`
	if issue.Message != want {
		t.Errorf("message = %q, want %q", issue.Message, want)
	}
}

func TestValidateMarkupSkipsOpenAndLiteralFreeUnions(t *testing.T) {
	tests := []struct {
		name     string
		typeText string
		value    string
	}{
		{name: "open union", typeText: `'a' | string`, value: "b"},
		{name: "literal-free union", typeText: `string | number`, value: "bogus"},
		{name: "null-only closed enum", typeText: `null | undefined`, value: "bogus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := memoryService(t, manifestForAttributeType(t, "rh-test", "kind", tt.typeText), "5.0.0")
			out := svc.ValidateMarkup(context.Background(), ValidateMarkupInput{
				Markup: fmt.Sprintf(`<rh-test kind="%s"></rh-test>`, tt.value),
			})
			if out.Error != nil {
				t.Fatalf("ValidateMarkup() error = %+v", out.Error)
			}
			if issue, ok := findIssue(out.Issues, "invalid-attribute-value"); ok {
				t.Errorf("value %q flagged for non-closed enum %q: %+v", tt.value, tt.typeText, issue)
			}
		})
	}
}

func TestValidateMarkupFlagsEmptyEnumValues(t *testing.T) {
	for _, markup := range []string{
		`<rh-button variant></rh-button>`,
		`<rh-button variant=""></rh-button>`,
	} {
		out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
			Markup: markup,
		})
		if out.Error != nil {
			t.Fatalf("ValidateMarkup(%q) error = %+v", markup, out.Error)
		}
		if _, ok := findIssue(out.Issues, "invalid-attribute-value"); !ok {
			t.Errorf("empty enum value not flagged for %q: %+v", markup, out.Issues)
		}
	}
}

func TestValidateMarkupAcceptsTemplatedAttributeValues(t *testing.T) {
	tests := []struct {
		name   string
		markup string
	}{
		{name: "interpolation", markup: `<rh-button variant="{{ variant }}"></rh-button>`},
		{name: "statement", markup: `<rh-button variant="{% if x %}primary{% endif %}"></rh-button>`},
		{name: "comment", markup: `<rh-button variant="{# comment #}primary"></rh-button>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
				Markup: tt.markup,
				Format: "twig",
			})
			if out.Error != nil {
				t.Fatalf("ValidateMarkup() error = %+v", out.Error)
			}
			if issue, ok := findIssue(out.Issues, "invalid-attribute-value"); ok {
				t.Errorf("templated value flagged invalid: %+v", issue)
			}
		})
	}
}

func TestValidateMarkupSelfClosingCustomElements(t *testing.T) {
	// cem's tree-sitter query reports no matches for self-closing custom
	// elements, so these must still be validated after normalization.
	out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: `<rh-button variant="huge" /><rh-alert />`,
	})
	if out.Error != nil {
		t.Fatalf("ValidateMarkup() error = %+v", out.Error)
	}
	if _, ok := findIssue(out.Issues, "invalid-attribute-value"); !ok {
		t.Errorf("invalid value on self-closing element not flagged: %+v", out.Issues)
	}
	if _, ok := findIssue(out.Issues, "unknown-tag"); !ok {
		t.Errorf("unknown self-closing element not flagged: %+v", out.Issues)
	}

	valid := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: `<rh-button variant="primary" />`,
	})
	if len(valid.Issues) != 0 {
		t.Errorf("valid self-closing element produced issues: %+v", valid.Issues)
	}
}

func TestValidateMarkupAllowsGlobalAttributes(t *testing.T) {
	markup := `<rh-button popover="auto" enterkeyhint="next" autocorrect="off" ` +
		`anchor="save" writingsuggestions="true" virtualkeyboardpolicy="auto" ` +
		`onclick="save()">Save</rh-button>`
	out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: markup,
	})
	if out.Error != nil {
		t.Fatalf("ValidateMarkup() error = %+v", out.Error)
	}
	for _, issue := range out.Issues {
		if issue.Rule == "unknown-attribute" {
			t.Errorf("global attribute flagged unknown: %+v", issue)
		}
	}
}

func TestTwigControlFlowUnbalanced(t *testing.T) {
	tests := []struct {
		name   string
		markup string
		want   bool
	}{
		{name: "if balanced", markup: `{% if x %}{% endif %}`, want: false},
		{name: "if unclosed", markup: `{% if x %}`, want: true},
		{name: "end without open", markup: `{% endif %}`, want: true},
		{name: "mismatched closer", markup: `{% if user %}{% endfor %}`, want: true},
		{name: "mismatched nested closer", markup: `{% if a %}{% for x in y %}{% endif %}{% endfor %}`, want: true},
		{name: "correct nesting", markup: `{% if a %}{% for x in y %}{% endfor %}{% endif %}`, want: false},
		{name: "else no-op", markup: `{% if x %}{% else %}{% endif %}`, want: false},
		{name: "elseif no-op", markup: `{% if x %}{% elseif y %}{% endif %}`, want: false},
		{name: "for else no-op", markup: `{% for x in y %}{% else %}{% endfor %}`, want: false},
		{name: "block", markup: `{% block title %}{% endblock %}`, want: false},
		{name: "named block", markup: `{% block title %}{% endblock title %}`, want: false},
		{name: "block shorthand", markup: `{% block title page_title|title %}`, want: false},
		{name: "scoped block", markup: `{% block title scoped %}{% endblock %}`, want: false},
		{name: "set assignment", markup: `{% set x = 1 %}`, want: false},
		{name: "set block", markup: `{% set x %}{% endset %}`, want: false},
		{name: "sandbox", markup: `{% sandbox %}{% endsandbox %}`, want: false},
		{name: "cache", markup: `{% cache "key" %}{% endcache %}`, want: false},
		{name: "spaceless", markup: `{% spaceless %}{% endspaceless %}`, want: false},
		{name: "unknown tag pair", markup: `{% guard foo %}{% endguard %}`, want: false},
		{name: "unknown closer", markup: `{% endguard %}`, want: false},
		{name: "comment containing tag", markup: `{# {% if x %} #}`, want: false},
		{name: "raw content", markup: `{% raw %}{% if x %}{% endraw %}`, want: false},
		{name: "unclosed raw", markup: `{% raw %}{% if x %}`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := twigControlFlowUnbalanced(tt.markup); got != tt.want {
				t.Errorf("twigControlFlowUnbalanced(%q) = %t, want %t", tt.markup, got, tt.want)
			}
		})
	}
}

func TestValidateMarkupTwigBlockShorthandIsBalanced(t *testing.T) {
	out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: `{% block title page_title|title %}<rh-button variant="huge"></rh-button>`,
		Format: "twig",
	})
	if out.Error != nil {
		t.Fatalf("ValidateMarkup() error = %+v", out.Error)
	}
	for _, warning := range out.Warnings {
		if strings.Contains(warning, "unbalanced") {
			t.Errorf("block shorthand reported as unbalanced: %q", warning)
		}
	}
	issue, ok := findIssue(out.Issues, "invalid-attribute-value")
	if !ok {
		t.Fatalf("missing invalid-attribute-value issue: %+v", out.Issues)
	}
	if issue.Severity != "error" || issue.Confidence != "high" {
		t.Errorf("severity/confidence = %q/%q, want error/high",
			issue.Severity, issue.Confidence)
	}
}

func TestNormalizeMarkupCasePreservesValuesAndOffsets(t *testing.T) {
	input := `<RH-BUTTON VARIANT="HuGe" DATA-X='Y'>Text {{ Some.Value }}</RH-BUTTON>{% IF x %}`
	want := `<rh-button variant="HuGe" data-x='Y'>Text {{ Some.Value }}</rh-button>{% IF x %}`
	got := normalizeMarkupCase(input)
	if got != want {
		t.Errorf("normalizeMarkupCase() = %q, want %q", got, want)
	}
	if len(got) != len(input) {
		t.Errorf("normalized length = %d, want %d", len(got), len(input))
	}
}

func TestValidateMarkupRejectsUnknownFormat(t *testing.T) {
	out := fixtureService(t).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: "<rh-button></rh-button>",
		Format: "jsx",
	})
	if out.Error == nil {
		t.Fatal("error = nil, want invalid_argument")
	}
	if out.Error.Code != "invalid_argument" {
		t.Errorf("error code = %q, want invalid_argument", out.Error.Code)
	}
}

func TestValidateMarkupWithoutDataReturnsStructuredError(t *testing.T) {
	out := New(nil, data.ErrNoData).ValidateMarkup(context.Background(), ValidateMarkupInput{
		Markup: "<rh-button></rh-button>",
	})
	if out.Error == nil || out.Error.Code != "no_bundled_data" {
		t.Fatalf("error = %+v, want no_bundled_data", out.Error)
	}
}

func FuzzValidateMarkup(f *testing.F) {
	seeds := []struct {
		markup string
		format string
	}{
		{`<rh-button variant="primary">Save</rh-button>`, "html"},
		{`<RH-BUTTON VARIANT="huge"></RH-BUTTON>`, "html"},
		{`<rh-alert>bogus</rh-alert>`, ""},
		{`{% if user %}<rh-card><rh-button bogus="x"></rh-button></rh-card>`, "twig"},
		{`{% if user %}<rh-button>{{ value }}</rh-button>{% endif %}`, "twig"},
		{``, "html"},
	}
	for _, seed := range seeds {
		f.Add(seed.markup, seed.format)
	}

	store, err := data.Open(os.DirFS("testdata"))
	if err != nil {
		f.Fatalf("opening fixture store: %v", err)
	}
	svc := New(store, nil)

	f.Fuzz(func(t *testing.T, markup, format string) {
		if len(markup) > 64<<10 {
			t.Skip()
		}
		out := svc.ValidateMarkup(context.Background(), ValidateMarkupInput{
			Markup: markup,
			Format: format,
		})
		if out.Error != nil {
			if out.Error.Code != "invalid_argument" {
				t.Fatalf("unexpected error for format %q: %+v", format, out.Error)
			}
			return
		}
		for _, issue := range out.Issues {
			switch issue.Confidence {
			case "high", "low":
			default:
				t.Errorf("invalid confidence %q in %+v", issue.Confidence, issue)
			}
			switch issue.Severity {
			case "error", "warning", "info":
			default:
				t.Errorf("invalid severity %q in %+v", issue.Severity, issue)
			}
			if issue.Line < 1 || issue.Column < 1 {
				t.Errorf("invalid position %d:%d in %+v", issue.Line, issue.Column, issue)
			}
		}
	})
}
