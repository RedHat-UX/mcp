// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/cem"
	"github.com/redhat-ux/mcp/internal/resolve"
)

// wireResponseBytes reproduces the bytes the SDK puts on the wire for a tool
// output: the structured JSON, the same JSON re-encoded as a TextContent
// string, and the JSON-RPC envelope. applySchema re-marshals object outputs
// through map[string]any, which preserves length but sorts keys, so the test
// round-trips objects the same way before measuring.
func wireResponseBytes(value any) int {
	return wireResponseBytesForID(value, 1)
}

func wireResponseBytesForID(value any, id any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	if len(encoded) > 0 && encoded[0] == '{' {
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err == nil {
			if remarshaled, err := json.Marshal(decoded); err == nil {
				encoded = remarshaled
			}
		}
	}
	result := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: json.RawMessage(encoded),
	}
	envelope, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	if err != nil {
		return 0
	}
	return len(envelope)
}

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
	wire := wireResponseBytes(value)
	if got := estimatedResponseBytes(value); got < wire {
		t.Errorf("estimated = %d, want at least the %d byte wire size", got, wire)
	}
}

// TestEstimatedResponseBytesIsUpperBoundOnWire guards the estimator against
// undercounting the escaped TextContent copy. Budget tests that only compare
// the estimator to itself pass even when the estimator is wrong.
func TestEstimatedResponseBytesIsUpperBoundOnWire(t *testing.T) {
	payloads := map[string]any{
		"plain":           map[string]string{"value": strings.Repeat("a", 4000)},
		"quote-heavy":     map[string]string{"value": strings.Repeat(`"quoted" `, 500)},
		"backslash-heavy": map[string]string{"value": strings.Repeat(`a\b\\c`, 800)},
		"html-heavy":      map[string]string{"value": strings.Repeat(`<div class="x">&</div>`, 400)},
		"mixed-nested": map[string]any{"items": []map[string]string{
			{"path": `docs/"quoted"/<tag>.md`, "text": strings.Repeat(`\n"x"<`, 300)},
		}},
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			estimate := estimatedResponseBytes(payload)
			wire := wireResponseBytes(payload)
			t.Logf("estimate = %d, wire = %d", estimate, wire)
			if estimate < wire {
				t.Errorf("estimate = %d, want an upper bound on wire = %d", estimate, wire)
			}
		})
	}

	t.Run("uuid-request-id", func(t *testing.T) {
		payload := map[string]string{"value": strings.Repeat(`"quoted" `, 200)}
		const id = "123e4567-e89b-12d3-a456-426614174000"
		estimate := estimatedResponseBytes(payload)
		wire := wireResponseBytesForID(payload, id)
		t.Logf("estimate = %d, wire with 36-character string id = %d", estimate, wire)
		if estimate < wire {
			t.Errorf("estimate = %d, want an upper bound on wire = %d", estimate, wire)
		}
	})
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
	if got := wireResponseBytes(out); got > defaultResponseBudget {
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
	if got := wireResponseBytes(out); got > defaultResponseBudget {
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
	if got := wireResponseBytes(out); got > defaultResponseBudget {
		t.Errorf("estimated response = %d, want <= %d", got, defaultResponseBudget)
	}
	if len(out.Hits) == 0 || len(out.Hits) >= 100 {
		t.Errorf("hits = %d, want a trimmed non-empty list", len(out.Hits))
	}
}

func TestFitListComponentsCursorRoundTrip(t *testing.T) {
	out := ListComponentsOutput{Resolved: ResolvedVersion{Elements: "5.0.0"}, Total: 10}
	for i := 0; i < 10; i++ {
		out.Components = append(out.Components, ComponentSummary{
			TagName: fmt.Sprintf("rh-component-%02d", i),
			Summary: strings.Repeat("summary ", 100),
		})
	}
	fitListComponents(&out, 0, false, 4000)
	if len(out.Components) == 0 || len(out.Components) >= 10 {
		t.Fatalf("components = %d, want a trimmed page", len(out.Components))
	}
	payload, toolErr := decodeCursor(out.NextCursor)
	if toolErr != nil {
		t.Fatalf("decodeCursor() error = %+v", toolErr)
	}
	if payload.Offset != len(out.Components) {
		t.Errorf("cursor offset = %d, want %d", payload.Offset, len(out.Components))
	}
	if payload.IncludeDeprecated {
		t.Error("cursor includeDeprecated = true, want false")
	}
	if estimatedResponseBytes(out) > 4000 {
		t.Errorf("estimated response = %d, want <= 4000", estimatedResponseBytes(out))
	}
}

// TestFitListComponentsMarksAndWarnsTruncation proves that the list budget
// path never shortens a field silently: the row carries truncated=true and a
// warning names the field and its original rune length.
func TestFitListComponentsMarksAndWarnsTruncation(t *testing.T) {
	longPath := strings.Repeat("p", 400)
	out := ListComponentsOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0"},
		Components: []ComponentSummary{{
			TagName:        "rh-long",
			Summary:        strings.Repeat("s", 600),
			ModulePath:     longPath,
			PackageSubpath: longPath,
		}},
		Total: 1,
	}

	fitListComponents(&out, 0, false, hardResponseBudget)
	if out.Error != nil {
		t.Fatalf("fitListComponents() error = %+v", out.Error)
	}
	if len(out.Components) != 1 {
		t.Fatalf("components = %d, want the single row", len(out.Components))
	}
	component := out.Components[0]
	if !component.Truncated {
		t.Error("truncated = false, want true")
	}
	if got := utf8.RuneCountInString(component.Summary); got != 503 {
		t.Errorf("summary runes = %d, want 503 (500 plus the marker)", got)
	}
	if got := utf8.RuneCountInString(component.ModulePath); got != 303 {
		t.Errorf("module path runes = %d, want 303 (300 plus the marker)", got)
	}
	for _, want := range []struct {
		field    string
		original int
		result   int
	}{
		{"summary", 600, 503},
		{"modulePath", 400, 303},
		{"packageSubpath", 400, 303},
	} {
		found := false
		for _, warning := range out.Resolved.Warnings {
			if strings.Contains(warning, want.field) &&
				strings.Contains(warning, fmt.Sprintf("from %d to %d", want.original, want.result)) {
				found = true
			}
		}
		if !found {
			t.Errorf("warnings = %v, want one naming %s, its original %d runes, and the result %d runes",
				out.Resolved.Warnings, want.field, want.original, want.result)
		}
	}
}

// TestMarkComponentTruncationReportsTrueOriginalAcrossPasses covers the
// single-row fallback: the second shortening pass must report the true
// original length, not the already-truncated one, and must replace rather than
// duplicate the notice.
func TestMarkComponentTruncationReportsTrueOriginalAcrossPasses(t *testing.T) {
	component := ComponentSummary{
		TagName:        "rh-long",
		Summary:        strings.Repeat("s", 600),
		ModulePath:     strings.Repeat("p", 400),
		PackageSubpath: strings.Repeat("p", 400),
	}
	state := newFieldTruncation()
	markComponentTruncation(&component, state, 100, 500, 300, 300)
	markComponentTruncation(&component, state, 0, 100, 150, 150)

	notices := state.list()
	if len(notices) != 3 {
		t.Fatalf("notices = %v, want one per field", notices)
	}
	for _, want := range []struct {
		field    string
		original int
		result   int
	}{
		{"summary", 600, 103},
		{"modulePath", 400, 153},
		{"packageSubpath", 400, 153},
	} {
		found := false
		for _, notice := range notices {
			if strings.Contains(notice, want.field) &&
				strings.Contains(notice, fmt.Sprintf("from %d to %d", want.original, want.result)) {
				found = true
			}
		}
		if !found {
			t.Errorf("notices = %v, want %s from its true original %d to %d",
				notices, want.field, want.original, want.result)
		}
	}
}

// TestFitListComponentsKeepsEveryTruncationNoticeBeyondWarningCap proves that
// the ordinary warning cap cannot hide a truncation notice, even with more
// notices than maxResponseWarnings and a full base warning list.
func TestFitListComponentsKeepsEveryTruncationNoticeBeyondWarningCap(t *testing.T) {
	components := make([]ComponentSummary, 5)
	for i := range components {
		components[i] = ComponentSummary{
			TagName:        fmt.Sprintf("rh-%d", i),
			Summary:        strings.Repeat("s", 600),
			ModulePath:     strings.Repeat("p", 400),
			PackageSubpath: strings.Repeat("p", 400),
		}
	}
	out := ListComponentsOutput{
		Resolved:   ResolvedVersion{Elements: "5.0.0", Warnings: manyWarnings(20)},
		Components: components,
		Total:      len(components),
	}

	fitListComponents(&out, 0, false, 1<<20)
	if len(out.Components) != len(components) {
		t.Fatalf("components = %d, want %d", len(out.Components), len(components))
	}
	counts := make(map[string]int)
	for _, warning := range out.Resolved.Warnings {
		if !strings.Contains(warning, "truncated from") {
			continue
		}
		for _, field := range []string{"summary", "modulePath", "packageSubpath"} {
			if strings.Contains(warning, " "+field+" truncated from") {
				counts[field]++
			}
		}
	}
	for _, field := range []string{"summary", "modulePath", "packageSubpath"} {
		if counts[field] != len(components) {
			t.Errorf("%s notices = %d, want %d; warnings = %v",
				field, counts[field], len(components), out.Resolved.Warnings)
		}
	}
	for _, component := range out.Components {
		if !component.Truncated {
			t.Errorf("%s truncated = false, want true", component.TagName)
		}
	}
}

// TestFitSearchMarksAndWarnsTruncation applies the same no-silent-truncation
// contract to search rows.
func TestFitSearchMarksAndWarnsTruncation(t *testing.T) {
	out := SearchOutput{
		Hits: []SearchHit{{
			Type:    "doc",
			Title:   strings.Repeat("t", 250),
			Path:    strings.Repeat("p", 350),
			Excerpt: strings.Repeat("e", 200),
		}},
		Total: 1,
	}
	fitSearch(&out, hardResponseBudget)
	if out.Error != nil {
		t.Fatalf("fitSearch() error = %+v", out.Error)
	}
	if len(out.Hits) != 1 {
		t.Fatalf("hits = %d, want the single hit", len(out.Hits))
	}
	hit := out.Hits[0]
	if !hit.Truncated {
		t.Error("truncated = false, want true")
	}
	if got := utf8.RuneCountInString(hit.Title); got != 203 {
		t.Errorf("title runes = %d, want 203", got)
	}
	if got := utf8.RuneCountInString(hit.Path); got != 303 {
		t.Errorf("path runes = %d, want 303", got)
	}
	if got := utf8.RuneCountInString(hit.Excerpt); got != 183 {
		t.Errorf("excerpt runes = %d, want 183", got)
	}
	for _, want := range []struct {
		field    string
		original int
		result   int
	}{
		{"title", 250, 203},
		{"path", 350, 303},
		{"excerpt", 200, 183},
	} {
		found := false
		for _, warning := range out.Resolved.Warnings {
			if strings.Contains(warning, want.field) &&
				strings.Contains(warning, fmt.Sprintf("from %d to %d", want.original, want.result)) {
				found = true
			}
		}
		if !found {
			t.Errorf("warnings = %v, want one naming %s, its original %d runes, and the result %d runes",
				out.Resolved.Warnings, want.field, want.original, want.result)
		}
	}
}

func TestFitComponentAPIKeepsOmissionWarnings(t *testing.T) {
	examples := make([]cem.Example, 200)
	for i := range examples {
		examples[i] = cem.Example{Description: strings.Repeat("x", 40)}
	}
	warnings := make([]string, 0, maxResponseWarnings)
	for i := 0; i < maxResponseWarnings; i++ {
		warnings = append(warnings, fmt.Sprintf("resolution warning %d", i))
	}
	out := ComponentAPIOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0", Warnings: warnings},
		TagName:  "rh-huge",
		Sections: componentAPISections,
		Examples: examples,
	}

	fitted := fitComponentAPI(out, 4096, true)
	if slices.Contains(fitted.Sections, sectionExamples) {
		t.Errorf("sections = %v, want examples omitted", fitted.Sections)
	}
	if len(fitted.Examples) != 0 {
		t.Errorf("examples = %d, want omitted", len(fitted.Examples))
	}
	if !warnedForSection(fitted.Resolved.Warnings, sectionExamples) {
		t.Errorf("warnings = %v, want one naming %s", fitted.Resolved.Warnings, sectionExamples)
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
	fitted := fitComponentAPI(out, hardResponseBudget, false)
	if fitted.Error == nil || fitted.Error.Code != "response_too_large" {
		t.Fatalf("error = %+v, want response_too_large", fitted.Error)
	}
	if fitted.Attributes != nil {
		t.Error("partial attributes returned alongside the error")
	}
}

func TestNewToolBudgets(t *testing.T) {
	svc := New(nil, nil)
	if got := svc.responseBudget(false); got != defaultResponseBudget {
		t.Errorf("summary budget = %d, want %d", got, defaultResponseBudget)
	}
	if got := svc.responseBudget(true); got != hardResponseBudget {
		t.Errorf("full budget = %d, want %d", got, hardResponseBudget)
	}
	svc.maxResponseBytes = 1000
	if got := svc.responseBudget(false); got != 1000 {
		t.Errorf("overridden summary budget = %d, want 1000", got)
	}
	if got := svc.responseBudget(true); got != 1000 {
		t.Errorf("overridden full budget = %d, want 1000", got)
	}
}

// TestExistingToolsRespectServiceBudget checks the section 7 tools that have
// their own pagination or follow-up controls. When a response is over the
// configured wire budget it must either be trimmed with an explicit control or
// return a structured response_too_large error.
func TestExistingToolsRespectServiceBudget(t *testing.T) {
	svc := fixtureService(t)
	svc.maxResponseBytes = 1400

	t.Run("resolve_version", func(t *testing.T) {
		out := svc.ResolveVersion(context.Background(), ResolveVersionInput{Version: "5.0.0"})
		assertWithinBudget(t, out, 1400, out.Truncated, out.Error)
	})

	t.Run("list_components", func(t *testing.T) {
		out := svc.ListComponents(context.Background(), ListComponentsInput{
			Version:  "5.0.0",
			PageSize: 100,
		})
		assertWithinBudget(t, out, 1400, out.Truncated || out.NextCursor != "", out.Error)
	})

	t.Run("search", func(t *testing.T) {
		out := svc.Search(context.Background(), SearchInput{
			Query:   "button",
			Version: "5.0.0",
			Limit:   50,
		})
		assertWithinBudget(t, out, 1400, out.Truncated, out.Error)
	})

	t.Run("validate_markup", func(t *testing.T) {
		out := svc.ValidateMarkup(context.Background(), ValidateMarkupInput{
			Markup:  `<rh-button ` + strings.Repeat(`bogus="x" `, 40) + `></rh-button>`,
			Version: "5.0.0",
		})
		assertWithinBudget(t, out, 1400, out.Truncated, out.Error)
	})
}

func assertWithinBudget(t *testing.T, value any, budget int, hasControl bool, toolErr *ToolError) {
	t.Helper()
	size := wireResponseBytes(value)
	estimate := estimatedResponseBytes(value)
	t.Logf("wire size = %d bytes, estimate = %d (test budget %d)", size, estimate, budget)
	if size > budget {
		t.Errorf("response = %d bytes, over the %d byte budget", size, budget)
	}
	if size > budget && !hasControl && (toolErr == nil || toolErr.Code != "response_too_large") {
		t.Error("oversized response has no explicit follow-up control")
	}
	if toolErr != nil && toolErr.Code != "response_too_large" {
		t.Errorf("unexpected tool error: %+v", toolErr)
	}
}

func TestFitResolveVersionTinyBudgetReturnsStructuredError(t *testing.T) {
	out := ResolveVersionOutput{
		Resolved:   ResolvedVersion{Warnings: manyWarnings(100)},
		Candidates: manyCandidates(10),
	}
	fitResolveVersion(&out, 64)
	if out.Error == nil || out.Error.Code != "response_too_large" {
		t.Fatalf("error = %+v, want response_too_large", out.Error)
	}
}

func TestFitListComponentsMarksFieldTruncation(t *testing.T) {
	out := ListComponentsOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0"},
		Total:    1,
		Components: []ComponentSummary{{
			TagName:        "rh-x",
			Summary:        strings.Repeat("s", 600),
			ModulePath:     strings.Repeat("m", 400),
			PackageSubpath: strings.Repeat("p", 400),
		}},
	}
	fitListComponents(&out, 0, false, defaultResponseBudget)
	if out.Error != nil {
		t.Fatalf("fitListComponents() error = %+v", out.Error)
	}
	if len(out.Components) != 1 {
		t.Fatalf("components = %d, want one", len(out.Components))
	}
	if !out.Components[0].Truncated {
		t.Error("component Truncated = false, want true")
	}
	if !hasWarning(out.Resolved.Warnings, "summary") {
		t.Errorf("warnings = %v, want a summary truncation warning", out.Resolved.Warnings)
	}
	if !hasWarning(out.Resolved.Warnings, "modulePath") {
		t.Errorf("warnings = %v, want a modulePath truncation warning", out.Resolved.Warnings)
	}
	if size := wireResponseBytes(out); size > defaultResponseBudget {
		t.Errorf("response = %d bytes, over the %d byte default", size, defaultResponseBudget)
	}
}

// TestFitListComponentsNoticesCountReturnedEntries checks that a notice
// describes the page actually returned, not entries the budget trimmed away.
func TestFitListComponentsNoticesCountReturnedEntries(t *testing.T) {
	components := make([]ComponentSummary, 40)
	for i := range components {
		components[i] = ComponentSummary{
			TagName:    fmt.Sprintf("rh-component-%03d", i),
			Summary:    strings.Repeat("s", 600),
			ModulePath: "rh-component.js",
		}
	}
	out := ListComponentsOutput{
		Resolved:   ResolvedVersion{Elements: "5.0.0"},
		Total:      len(components),
		Components: components,
	}
	fitListComponents(&out, 0, false, 2500)
	if out.Error != nil {
		t.Fatalf("fitListComponents() error = %+v", out.Error)
	}
	if len(out.Components) == 0 || len(out.Components) == len(components) {
		t.Fatalf("components = %d, want a trimmed non-empty page", len(out.Components))
	}
	want := fmt.Sprintf("component %s summary truncated", out.Components[0].TagName)
	if !hasWarning(out.Resolved.Warnings, want) {
		t.Errorf("warnings = %v, want %q", out.Resolved.Warnings, want)
	}
	last := components[len(components)-1].TagName
	if !slices.ContainsFunc(out.Components, func(c ComponentSummary) bool { return c.TagName == last }) &&
		hasWarning(out.Resolved.Warnings, "component "+last+" summary truncated") {
		t.Errorf("warnings = %v, still describe a trimmed entry (%s)", out.Resolved.Warnings, last)
	}
}

// TestFitListComponentsKeepsWarningsWhenTruncationFits checks that field
// truncation, which alone brings the response under budget, does not replace
// substantive resolution warnings with an omission summary.
func TestFitListComponentsKeepsWarningsWhenTruncationFits(t *testing.T) {
	components := make([]ComponentSummary, 3)
	for i := range components {
		components[i] = ComponentSummary{
			TagName:    fmt.Sprintf("rh-component-%03d", i),
			Summary:    strings.Repeat("s", 5000),
			ModulePath: "rh-component.js",
		}
	}
	out := ListComponentsOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0", Warnings: []string{
			"resolved @rhds/elements 5.0.0 from package-lock.json",
			"workspace root came from MCP roots",
		}},
		Total:      len(components),
		Components: components,
	}
	fitListComponents(&out, 0, false, defaultResponseBudget)
	if out.Error != nil {
		t.Fatalf("fitListComponents() error = %+v", out.Error)
	}
	if !hasWarning(out.Resolved.Warnings, "package-lock.json") {
		t.Errorf("warnings = %v, lost the lockfile resolution warning", out.Resolved.Warnings)
	}
	if !hasWarning(out.Resolved.Warnings, "MCP roots") {
		t.Errorf("warnings = %v, lost the roots warning", out.Resolved.Warnings)
	}
	if hasWarning(out.Resolved.Warnings, "warnings omitted") {
		t.Errorf("warnings = %v, summarized warnings that truncation made fit", out.Resolved.Warnings)
	}
}

// TestTruncationAndTrimNoticesSurviveWarningCap checks that the warning cap
// cannot hide the field or trim notice behind a generic omission line.
func TestTruncationAndTrimNoticesSurviveWarningCap(t *testing.T) {
	base := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		base = append(base, fmt.Sprintf("workspace warning %02d: %s", i, strings.Repeat("w", 20)))
	}

	t.Run("field notice", func(t *testing.T) {
		out := ListComponentsOutput{
			Resolved: ResolvedVersion{Elements: "5.0.0", Warnings: base},
			Total:    1,
			Components: []ComponentSummary{{
				TagName:    "rh-x",
				Summary:    strings.Repeat("s", 600),
				ModulePath: "rh-x.js",
			}},
		}
		fitListComponents(&out, 0, false, defaultResponseBudget)
		if !hasWarning(out.Resolved.Warnings, "summary truncated") {
			t.Errorf("warnings = %v, want the field notice", out.Resolved.Warnings)
		}
		// sanitizeWarningsLimit may add one omission line after the reserved
		// notices, so the absolute ceiling is one above maxResponseWarnings.
		if len(out.Resolved.Warnings) > maxResponseWarnings+1 {
			t.Errorf("warnings = %d, want at most %d", len(out.Resolved.Warnings), maxResponseWarnings+1)
		}
	})

	t.Run("trim notice", func(t *testing.T) {
		components := make([]ComponentSummary, 40)
		for i := range components {
			components[i] = ComponentSummary{
				TagName:    fmt.Sprintf("rh-component-%03d", i),
				Summary:    strings.Repeat("s", 80),
				ModulePath: "rh-component.js",
			}
		}
		out := ListComponentsOutput{
			Resolved:   ResolvedVersion{Elements: "5.0.0", Warnings: base},
			Total:      len(components),
			Components: components,
		}
		fitListComponents(&out, 0, false, defaultResponseBudget)
		if !hasWarning(out.Resolved.Warnings, "response limited to fit the budget") {
			t.Errorf("warnings = %v, want the trim notice", out.Resolved.Warnings)
		}
		// sanitizeWarningsLimit may add one omission line after the reserved
		// notices, so the absolute ceiling is one above maxResponseWarnings.
		if len(out.Resolved.Warnings) > maxResponseWarnings+1 {
			t.Errorf("warnings = %d, want at most %d", len(out.Resolved.Warnings), maxResponseWarnings+1)
		}
	})
}

// TestFitListComponentsCountsCursorBytes sweeps budgets where a single
// component plus its cursor fits. Any larger page must trim to fit, so the
// fitter must not fail with response_too_large just because the cursor pushed
// the response over after the last trim.
func TestFitListComponentsCountsCursorBytes(t *testing.T) {
	components := make([]ComponentSummary, 40)
	for i := range components {
		components[i] = ComponentSummary{
			TagName:    fmt.Sprintf("rh-component-%03d", i),
			Summary:    strings.Repeat("s", 80),
			ModulePath: "rh-component.js",
		}
	}
	tested := 0
	for budget := 500; budget <= 4000; budget += 13 {
		// The minimum feasible page is one component plus the budget warning
		// and the cursor. Only sweep budgets where that page fits.
		single := ListComponentsOutput{
			Resolved: ResolvedVersion{
				Elements: "5.0.0",
				Warnings: []string{"response limited to fit the budget; continue with nextCursor"},
			},
			Total:      len(components),
			Components: append([]ComponentSummary(nil), components[:1]...),
			NextCursor: encodeCursor("5.0.0", 1, false),
			Truncated:  true,
		}
		if estimatedResponseBytes(single) > budget {
			continue
		}
		tested++
		out := ListComponentsOutput{
			Resolved:   ResolvedVersion{Elements: "5.0.0"},
			Total:      len(components),
			Components: append([]ComponentSummary(nil), components...),
		}
		fitListComponents(&out, 0, false, budget)
		if out.Error != nil {
			t.Fatalf("budget %d: %+v, but one component with its cursor fits", budget, out.Error)
		}
		if got := wireResponseBytes(out); got > budget {
			t.Errorf("budget %d: response = %d bytes", budget, got)
		}
		if out.NextCursor == "" {
			t.Errorf("budget %d: partial page has no cursor", budget)
		}
	}
	if tested == 0 {
		t.Fatal("no budget exercised the cursor path")
	}
}
