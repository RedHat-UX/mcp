// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
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
