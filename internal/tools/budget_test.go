// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/redhat-ux/mcp/internal/cem"
	"github.com/redhat-ux/mcp/internal/resolve"
)

func manyWarnings(count int) []string {
	warnings := make([]string, 0, count)
	for i := 0; i < count; i++ {
		warnings = append(warnings, fmt.Sprintf("warning %d: %s", i, strings.Repeat("x", 500)))
	}
	return warnings
}

func manyCandidates(count int) []resolve.Candidate {
	candidates := make([]resolve.Candidate, 0, count)
	for i := 0; i < count; i++ {
		candidates = append(candidates, resolve.Candidate{
			Version:    fmt.Sprintf("5.0.%d", i),
			Spec:       fmt.Sprintf("^5.0.%d", i),
			Source:     fmt.Sprintf("package-%03d/package.json", i),
			Precedence: resolve.PrecedencePackageJSON,
			Level:      resolve.LevelPackageJSON,
		})
	}
	return candidates
}

func TestEstimatedResponseBytesCountsTextDuplication(t *testing.T) {
	value := map[string]string{"value": strings.Repeat("a", 1000)}
	if got := estimatedResponseBytes(value); got < 2000 {
		t.Errorf("estimated = %d, want at least the doubled JSON size", got)
	}
}

func TestFitResolveVersionBoundsOutput(t *testing.T) {
	out := ResolveVersionOutput{
		Resolved: ResolvedVersion{
			Elements: "5.0.0",
			Tokens:   "3.2.0",
			Docs:     "v5.0.0",
			Warnings: manyWarnings(100),
		},
		Candidates: manyCandidates(500),
	}
	fitResolveVersion(&out, defaultResponseBudget)
	if got := estimatedResponseBytes(out); got > defaultResponseBudget {
		t.Errorf("estimated response = %d, want <= %d", got, defaultResponseBudget)
	}
	if len(out.Resolved.Warnings) > maxResponseWarnings+1 {
		t.Errorf("warnings = %d, want at most %d", len(out.Resolved.Warnings), maxResponseWarnings+1)
	}
	if len(out.Candidates) > maxOutputCandidates {
		t.Errorf("candidates = %d, want at most %d", len(out.Candidates), maxOutputCandidates)
	}
}

func TestFitListComponentsTrimsAndPreservesCursor(t *testing.T) {
	out := ListComponentsOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0", Warnings: manyWarnings(50)},
		Total:    100,
	}
	for i := 0; i < 100; i++ {
		out.Components = append(out.Components, ComponentSummary{
			TagName:    fmt.Sprintf("rh-component-%03d", i),
			Summary:    strings.Repeat("summary ", 100),
			ModulePath: "./rhds/elements/rh-component/rh-component.js",
		})
	}
	out.NextCursor = encodeCursor("5.0.0", 0, false)
	fitListComponents(&out, 0, false, defaultResponseBudget)
	if got := estimatedResponseBytes(out); got > defaultResponseBudget {
		t.Errorf("estimated response = %d, want <= %d", got, defaultResponseBudget)
	}
	if len(out.Components) == 0 || len(out.Components) >= 100 {
		t.Errorf("components = %d, want a trimmed non-empty page", len(out.Components))
	}
	if out.NextCursor == "" {
		t.Error("next cursor is empty after trimming; the caller cannot continue")
	}
}

func TestFitSearchTrimsOutput(t *testing.T) {
	out := SearchOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0", Warnings: manyWarnings(50)},
		Total:    200,
	}
	for i := 0; i < 100; i++ {
		out.Hits = append(out.Hits, SearchHit{
			Type:    "doc",
			Title:   fmt.Sprintf("Document %03d", i),
			Path:    strings.Repeat("path/", 50),
			Version: "v5.0.0",
			Excerpt: strings.Repeat("excerpt ", 100),
		})
	}
	fitSearch(&out, defaultResponseBudget)
	if got := estimatedResponseBytes(out); got > defaultResponseBudget {
		t.Errorf("estimated response = %d, want <= %d", got, defaultResponseBudget)
	}
	if len(out.Hits) == 0 || len(out.Hits) >= 100 {
		t.Errorf("hits = %d, want a trimmed non-empty list", len(out.Hits))
	}
}

func TestFitComponentAPIRejectsOversizedResponse(t *testing.T) {
	out := ComponentAPIOutput{
		Resolved:   ResolvedVersion{Elements: "5.0.0"},
		TagName:    "rh-huge",
		Sections:   componentAPISections,
		Attributes: make([]cem.Attribute, 0),
	}
	// Fill enough data to exceed even the hard cap.
	for i := 0; i < 500; i++ {
		out.Attributes = append(out.Attributes, cem.Attribute{
			Name:        fmt.Sprintf("attribute-%03d", i),
			Description: strings.Repeat("description ", 100),
		})
	}
	fitted := fitComponentAPI(out, hardResponseBudget)
	if fitted.Error == nil || fitted.Error.Code != "response_too_large" {
		t.Fatalf("error = %+v, want response_too_large", fitted.Error)
	}
	if fitted.Attributes != nil {
		t.Error("partial attributes returned alongside the error")
	}
}
