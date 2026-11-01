// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
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

func TestEstimatedResponseBytesIncludesEscaping(t *testing.T) {
	value := map[string]string{"value": strings.Repeat("\"\\\n", 500)}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if got, doubled := estimatedResponseBytes(value), len(encoded)*2; got <= doubled {
		t.Errorf("estimated = %d, want more than the naive double %d for escaped text", got, doubled)
	}
}

func TestEstimatedResponseBytesIsAWireUpperBound(t *testing.T) {
	value := map[string]string{"text": strings.Repeat("\"\\\n<>&", 200)}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	result := mcp.CallToolResult{
		StructuredContent: json.RawMessage(encoded),
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if estimate := estimatedResponseBytes(value); estimate < len(wire) {
		t.Errorf("estimate = %d, wire = %d; estimate must be an upper bound", estimate, len(wire))
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
	if !out.Truncated {
		t.Error("Truncated = false, want true when candidates are dropped")
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
	if !out.Truncated {
		t.Error("Truncated = false, want true when components are dropped")
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
	if !out.Truncated {
		t.Error("Truncated = false, want true when hits are dropped")
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
	if !out.Truncated {
		t.Error("output Truncated = false, want true")
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
	if !out.Truncated {
		t.Error("output Truncated = false, want true")
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
	if !out.Truncated {
		t.Error("output Truncated = false, want true")
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

	fitted := fitComponentAPI(out, 4096, true, nil)
	if slices.Contains(fitted.Sections, sectionExamples) {
		t.Errorf("sections = %v, want examples omitted", fitted.Sections)
	}
	if len(fitted.Examples) != 0 {
		t.Errorf("examples = %d, want omitted", len(fitted.Examples))
	}
	if !warnedForSection(fitted.Resolved.Warnings, sectionExamples) {
		t.Errorf("warnings = %v, want one naming %s", fitted.Resolved.Warnings, sectionExamples)
	}
	if !hasSectionLink(fitted, sectionExamples) {
		t.Errorf("section links = %v, want one for %s", fitted.sectionLinks, sectionExamples)
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
	// A large optional section is dropped and replaced by a resource link
	// before the response is declared too large.
	for i := 0; i < 100; i++ {
		out.Examples = append(out.Examples, cem.Example{
			Description: strings.Repeat("example ", 100),
		})
	}
	fitted := fitComponentAPI(out, hardResponseBudget, true, nil)
	if fitted.Error == nil || fitted.Error.Code != "response_too_large" {
		t.Fatalf("error = %+v, want response_too_large", fitted.Error)
	}
	if fitted.Attributes != nil {
		t.Error("partial attributes returned alongside the error")
	}
	uri := componentSectionURI("5.0.0", "rh-huge", sectionExamples)
	if uri == "" {
		t.Fatal("componentSectionURI() = \"\", want the examples resource")
	}
	if !strings.Contains(fitted.Error.Hint, uri) {
		t.Errorf("hint = %q, want the omitted section URI %q", fitted.Error.Hint, uri)
	}
}

// wireResponseBytes reproduces the bytes the SDK puts on the wire for a tool
// output: the structured JSON, the same JSON re-encoded as a TextContent
// string, and the JSON-RPC envelope.
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

func hasWarning(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, needle) {
			return true
		}
	}
	return false
}

func hasSectionLink(out ComponentAPIOutput, section string) bool {
	for _, link := range out.sectionLinks {
		if link.section == section && link.uri != "" {
			return true
		}
	}
	return false
}

func TestFitSearchNoticeDoesNotEmptyHits(t *testing.T) {
	hits := make([]SearchHit, 50)
	for i := range hits {
		hits[i] = SearchHit{
			Type:    "docs",
			Title:   fmt.Sprintf("Button result %02d", i),
			Path:    fmt.Sprintf("docs/button-%02d.md", i),
			Version: "v5.0.0",
			Excerpt: strings.Repeat("excerpt ", 8),
			Score:   10,
		}
	}
	// Choose a budget where 12 hits fit without the trim notice, so the
	// notice itself pushes the payload over and must trigger one more trim.
	const keep = 12
	baseline := SearchOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0"},
		Hits:     append([]SearchHit(nil), hits[:keep]...),
		Total:    len(hits),
	}
	budget := estimatedResponseBytes(baseline) + 10
	out := SearchOutput{Resolved: ResolvedVersion{Elements: "5.0.0"}, Hits: hits, Total: len(hits)}
	fitSearch(&out, budget)
	if out.Error != nil && out.Error.Code != "response_too_large" {
		t.Fatalf("unexpected error = %+v", out.Error)
	}
	if len(out.Hits) == 0 {
		t.Fatalf("hits = 0, want a non-empty page; warnings = %v", out.Resolved.Warnings)
	}
	if !out.Truncated {
		t.Error("Truncated = false, want true when hits are dropped")
	}
	if size := wireResponseBytes(out); size > budget {
		t.Errorf("response = %d bytes, over the %d byte budget", size, budget)
	}
}

func TestFitResolveVersionNoticeDoesNotEmptyCandidates(t *testing.T) {
	out := ResolveVersionOutput{Resolved: ResolvedVersion{Elements: "5.0.0"}, Candidates: manyCandidates(100)}
	fitResolveVersion(&out, defaultResponseBudget)
	if len(out.Candidates) == 0 {
		t.Fatalf("candidates = 0, want a non-empty page; warnings = %v error = %+v",
			out.Resolved.Warnings, out.Error)
	}
	if !out.Truncated {
		t.Error("Truncated = false, want true when candidates are dropped")
	}
	if size := wireResponseBytes(out); size > defaultResponseBudget {
		t.Errorf("response = %d bytes, over the %d byte default", size, defaultResponseBudget)
	}
}

func TestFitValidateMarkupMarksMessageTruncation(t *testing.T) {
	out := ValidateMarkupOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0"},
		Issues: []MarkupIssue{{
			Rule:     "unknown-attribute",
			Severity: "error",
			Message:  strings.Repeat("m", 600),
			Element:  "rh-button",
			Line:     1,
		}},
	}
	fitted := fitValidateMarkup(out, defaultResponseBudget)
	if len(fitted.Issues) != 1 {
		t.Fatalf("issues = %d, want one", len(fitted.Issues))
	}
	if !fitted.Issues[0].Truncated {
		t.Error("issue Truncated = false, want true")
	}
	if !fitted.Truncated {
		t.Error("output Truncated = false, want true")
	}
	if !hasWarning(fitted.Warnings, "message truncated from 600") {
		t.Errorf("warnings = %v, want a message truncation notice", fitted.Warnings)
	}
	if size := wireResponseBytes(fitted); size > defaultResponseBudget {
		t.Errorf("response = %d bytes, over the %d byte default", size, defaultResponseBudget)
	}
}

func TestFitValidateMarkupNoticeDoesNotEmptyIssues(t *testing.T) {
	issues := make([]MarkupIssue, 20)
	for i := range issues {
		issues[i] = MarkupIssue{
			Rule:     "unknown-attribute",
			Severity: "error",
			Message:  strings.Repeat("m", 600),
			Element:  "rh-button",
			Line:     i + 1,
		}
	}
	out := ValidateMarkupOutput{Resolved: ResolvedVersion{Elements: "5.0.0"}, Issues: issues}
	fitted := fitValidateMarkup(out, 3000)
	if len(fitted.Issues) == 0 {
		t.Fatalf("issues = 0, want a non-empty page; warnings = %v", fitted.Warnings)
	}
	if !fitted.Truncated {
		t.Error("Truncated = false, want true when issues are dropped")
	}
	if size := wireResponseBytes(fitted); size > 3000 {
		t.Errorf("response = %d bytes, over 3000", size)
	}
}

func TestFitResolveVersionDoesNotShortenCandidatesUnderBudget(t *testing.T) {
	long := strings.Repeat("v", 400)
	out := ResolveVersionOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0"},
		Candidates: []resolve.Candidate{{
			Version: long,
			Spec:    long,
			Source:  long,
			Level:   resolve.LevelBundled,
		}},
	}
	fitResolveVersion(&out, defaultResponseBudget)
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %d, want one", len(out.Candidates))
	}
	if out.Candidates[0].Version != long {
		t.Error("candidate version shortened even though the response fits")
	}
	if out.Candidates[0].Truncated {
		t.Error("candidate marked truncated even though the response fits")
	}
}

func TestMarkCandidateTruncationSetsFlag(t *testing.T) {
	candidate := resolve.Candidate{
		Version: strings.Repeat("v", 400),
		Spec:    strings.Repeat("s", 400),
		Source:  strings.Repeat("p", 400),
		Level:   resolve.LevelBundled,
	}
	if !markCandidateTruncation(&candidate) {
		t.Fatal("markCandidateTruncation() = false, want true")
	}
	if !candidate.Truncated {
		t.Error("candidate Truncated = false, want true")
	}
	if len(candidate.Version) > maxCandidateRunes+3 {
		t.Errorf("version length = %d, want the cap plus ellipsis", len(candidate.Version))
	}
}

func TestFitComponentAPIKeepsExplicitlyRequestedSection(t *testing.T) {
	examples := make([]cem.Example, 200)
	for i := range examples {
		examples[i] = cem.Example{Description: strings.Repeat("x", 40)}
	}
	out := ComponentAPIOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0"},
		TagName:  "rh-huge",
		Sections: []string{sectionExamples},
		Examples: examples,
	}

	fitted := fitComponentAPI(out, 4096, true, map[string]bool{sectionExamples: true})
	if fitted.Error != nil {
		t.Fatalf("fitComponentAPI() error = %+v", fitted.Error)
	}
	if len(fitted.Examples) == 0 {
		t.Fatalf("examples were dropped although explicitly requested; links = %v", fitted.sectionLinks)
	}
	if !fitted.Truncated {
		t.Error("Truncated = false, want true for a shortened requested section")
	}
	if !hasWarning(fitted.Resolved.Warnings, "examples truncated") {
		t.Errorf("warnings = %v, want a truncation notice naming examples", fitted.Resolved.Warnings)
	}
	for _, warning := range fitted.Resolved.Warnings {
		if strings.Contains(warning, `sections=["examples"]`) {
			t.Errorf("warning suggests a no-op retry: %q", warning)
		}
	}
	if size := estimatedResponseBytes(fitted); size > 4096 {
		t.Errorf("response = %d bytes, want <= 4096", size)
	}
}

func TestFitComponentAPIOmitsUnrequestedSectionWithLink(t *testing.T) {
	examples := make([]cem.Example, 200)
	for i := range examples {
		examples[i] = cem.Example{Description: strings.Repeat("x", 40)}
	}
	out := ComponentAPIOutput{
		Resolved:   ResolvedVersion{Elements: "5.0.0"},
		TagName:    "rh-huge",
		Sections:   []string{sectionAttributes, sectionExamples},
		Attributes: []cem.Attribute{{Name: "variant", Type: "string"}},
		Examples:   examples,
	}

	fitted := fitComponentAPI(out, 4096, true, nil)
	if len(fitted.Examples) != 0 {
		t.Errorf("examples = %d, want the unrequested section omitted", len(fitted.Examples))
	}
	if !hasSectionLink(fitted, sectionExamples) {
		t.Errorf("section links = %v, want %s", fitted.sectionLinks, sectionExamples)
	}
	if !warnedForSection(fitted.Resolved.Warnings, sectionExamples) {
		t.Errorf("warnings = %v, want one naming the omitted section", fitted.Resolved.Warnings)
	}
	for _, warning := range fitted.Resolved.Warnings {
		if strings.Contains(warning, `sections=["examples"]`) {
			t.Errorf("warning suggests a no-op retry: %q", warning)
		}
	}
}

func TestFitComponentAPIOmitsRequestedSectionOnlyWhenTruncationFails(t *testing.T) {
	out := ComponentAPIOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0"},
		TagName:  "rh-huge",
		Sections: []string{sectionExamples},
		Examples: []cem.Example{{
			Description: strings.Repeat("x", 10_000),
		}},
	}

	fitted := fitComponentAPI(out, 1024, true, map[string]bool{sectionExamples: true})
	if len(fitted.Examples) != 0 {
		t.Errorf("examples = %d, want the impossible section omitted", len(fitted.Examples))
	}
	if !hasSectionLink(fitted, sectionExamples) {
		t.Errorf("section links = %v, want %s", fitted.sectionLinks, sectionExamples)
	}
	if !warnedForSection(fitted.Resolved.Warnings, sectionExamples) {
		t.Errorf("warnings = %v, want one naming the omitted section", fitted.Resolved.Warnings)
	}
	for _, warning := range fitted.Resolved.Warnings {
		if strings.Contains(warning, `sections=["examples"]`) {
			t.Errorf("warning suggests a no-op retry: %q", warning)
		}
	}
}

func TestErrorResponsesStayWithinBudgetWithLongWarnings(t *testing.T) {
	warnings := make([]string, 12)
	for i := range warnings {
		warnings[i] = strings.Repeat("w", 400)
	}
	const budget = 4096
	resolved := ResolvedVersion{Warnings: warnings}
	failure := &ToolError{Code: "artifact_not_found", Message: "no data"}

	components := ListComponentsOutput{Resolved: resolved, Error: failure}
	fitListComponents(&components, 0, false, budget)
	if size := estimatedResponseBytes(components); size > budget {
		t.Errorf("component error response = %d bytes, want <= %d", size, budget)
	}

	search := SearchOutput{Resolved: resolved, Error: failure}
	fitSearch(&search, budget)
	if size := estimatedResponseBytes(search); size > budget {
		t.Errorf("search error response = %d bytes, want <= %d", size, budget)
	}

	tokens := QueryTokensOutput{Resolved: resolved, Error: failure}
	tokens = fitQueryTokens(tokens, budget)
	if size := estimatedResponseBytes(tokens); size > budget {
		t.Errorf("tokens error response = %d bytes, want <= %d", size, budget)
	}

	markup := ValidateMarkupOutput{Resolved: resolved, Warnings: warnings, Error: failure}
	markup = fitValidateMarkup(markup, budget)
	if size := estimatedResponseBytes(markup); size > budget {
		t.Errorf("markup error response = %d bytes, want <= %d", size, budget)
	}

	api := ComponentAPIOutput{Resolved: resolved, Error: failure}
	api = fitComponentAPI(api, budget, true, nil)
	if size := estimatedResponseBytes(api); size > budget {
		t.Errorf("component API error response = %d bytes, want <= %d", size, budget)
	}
}
