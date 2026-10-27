// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/redhat-ux/mcp/internal/resolve"
)

const (
	defaultResponseBudget = 8 * 1024
	hardResponseBudget    = 32 * 1024

	maxResponseWarnings = 12
	maxWarningRunes     = 400
	maxOutputCandidates = 100
	maxCandidateRunes   = 256
)

// estimatedResponseBytes measures the serialized tool output. The MCP SDK
// sends structured output twice: structuredContent carries the JSON value and
// a text content block carries the same JSON escaped as a string. This
// computes both exact sizes plus an envelope allowance, instead of assuming a
// fixed multiplier.
func estimatedResponseBytes(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	escaped, err := json.Marshal(string(encoded))
	if err != nil {
		return len(encoded)*3 + 512
	}
	return len(encoded) + len(escaped) + 256
}

func truncateResponseText(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

// sanitizeWarnings truncates, deduplicates, and caps warning lists so a
// hostile workspace cannot inflate a response with one warning per file.
func sanitizeWarnings(warnings []string) []string {
	return sanitizeWarningsLimit(warnings, maxResponseWarnings)
}

func sanitizeWarningsLimit(warnings []string, limit int) []string {
	if len(warnings) == 0 {
		return nil
	}
	if limit < 0 {
		limit = 0
	}
	seen := make(map[string]bool, len(warnings))
	out := make([]string, 0, min(len(warnings), limit))
	dropped := 0
	for _, raw := range warnings {
		warning := truncateResponseText(strings.TrimSpace(raw), maxWarningRunes)
		if warning == "" || seen[warning] {
			continue
		}
		seen[warning] = true
		if len(out) >= limit {
			dropped++
			continue
		}
		out = append(out, warning)
	}
	if dropped > 0 {
		out = append(out, fmt.Sprintf("%d additional warnings omitted", dropped))
	}
	return out
}

// sanitizeWarningsWithReserved keeps every reserved notice, even when the
// ordinary warning list fills the cap. Reserved notices come from shortened
// response fields, so they are bounded by the payload and are never treated as
// hostile input. Ordinary warnings are capped to the remaining slots.
func sanitizeWarningsWithReserved(warnings, reserved []string) []string {
	if len(reserved) == 0 {
		return sanitizeWarnings(warnings)
	}
	reserved = sanitizeWarningNotices(reserved)
	limit := maxResponseWarnings - len(reserved)
	if limit < 0 {
		limit = 0
	}
	base := sanitizeWarningsLimit(warnings, limit)
	return sanitizeWarningNotices(append(reserved, base...))
}

// sanitizeWarningNotices truncates and deduplicates notices without dropping
// any of them.
func sanitizeWarningNotices(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(warnings))
	out := make([]string, 0, len(warnings))
	for _, raw := range warnings {
		warning := truncateResponseText(strings.TrimSpace(raw), maxWarningRunes)
		if warning == "" || seen[warning] {
			continue
		}
		seen[warning] = true
		out = append(out, warning)
	}
	return out
}

func summarizeWarnings(warnings []string) []string {
	if len(warnings) <= 1 {
		return warnings
	}
	return []string{fmt.Sprintf("%d warnings omitted to fit the response budget", len(warnings))}
}

func boundedCandidates(candidates []resolve.Candidate, limit int) []resolve.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	bounded := make([]resolve.Candidate, 0, min(len(candidates), limit))
	for i, candidate := range candidates {
		if i >= limit {
			break
		}
		candidate.Version = truncateResponseText(candidate.Version, maxCandidateRunes)
		candidate.Spec = truncateResponseText(candidate.Spec, maxCandidateRunes)
		candidate.Source = truncateResponseText(candidate.Source, 300)
		bounded = append(bounded, candidate)
	}
	return bounded
}

func sanitizeCandidates(candidates []resolve.Candidate, warnings *[]string) []resolve.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	dropped := 0
	if len(candidates) > maxOutputCandidates {
		dropped = len(candidates) - maxOutputCandidates
	}
	bounded := boundedCandidates(candidates, maxOutputCandidates)
	if dropped > 0 {
		*warnings = append(*warnings,
			fmt.Sprintf("%d additional candidates omitted; pass a more specific workspace root or version argument", dropped))
	}
	return bounded
}

func responseTooLarge(what string, budget int) *ToolError {
	return &ToolError{
		Code:    "response_too_large",
		Message: fmt.Sprintf("%s exceeds the %d byte response budget", what, budget),
		Hint:    "narrow the request (sections, query, pageSize, or version) and call again; there is no silent truncation",
	}
}

// boundError strips untrusted input echoed into an error and compacts
// ambiguity details. A 200k-character tagName or selector must not become a
// 400KB error payload.
func boundError(err *ToolError) *ToolError {
	if err == nil {
		return nil
	}
	err.Message = truncateResponseText(err.Message, maxWarningRunes)
	err.Hint = truncateResponseText(err.Hint, maxWarningRunes)
	err.Details = compactDetails(err.Details)
	return err
}

func compactDetails(details any) any {
	switch typed := details.(type) {
	case map[string]any:
		compacted := make(map[string]any, len(typed))
		for key, value := range typed {
			switch key {
			case "candidates":
				if candidates, ok := value.([]resolve.Candidate); ok {
					compacted[key] = ambiguityCandidateSummary(candidates)
					continue
				}
			case "configOptions":
				if options, ok := value.([]string); ok {
					compacted[key] = compactStrings(options, 5, 120)
					continue
				}
			}
			compacted[key] = value
		}
		return compacted
	case *resolve.Ambiguity:
		if typed == nil {
			return nil
		}
		return map[string]any{
			"level":      typed.Level,
			"majors":     typed.Majors,
			"candidates": ambiguityCandidateSummary(typed.Candidates),
		}
	case resolve.Ambiguity:
		return map[string]any{
			"level":      typed.Level,
			"majors":     typed.Majors,
			"candidates": ambiguityCandidateSummary(typed.Candidates),
		}
	default:
		return details
	}
}

func ambiguityCandidateSummary(candidates []resolve.Candidate) []string {
	if len(candidates) == 0 {
		return nil
	}
	const limit = 5
	summary := make([]string, 0, limit+1)
	for i, candidate := range candidates {
		if i >= limit {
			summary = append(summary,
				fmt.Sprintf("and %d more candidates", len(candidates)-limit))
			break
		}
		summary = append(summary, fmt.Sprintf("%s (%s)",
			truncateResponseText(candidate.Version, 96),
			truncateResponseText(candidate.Source, 160)))
	}
	return summary
}

func ambiguityCandidateSamples(candidates []resolve.Candidate) []resolve.Candidate {
	const limit = 5
	if len(candidates) <= limit {
		samples := make([]resolve.Candidate, len(candidates))
		copy(samples, candidates)
		return samples
	}
	samples := make([]resolve.Candidate, 0, limit)
	for _, candidate := range candidates[:limit] {
		candidate.Version = truncateResponseText(candidate.Version, 96)
		candidate.Spec = ""
		candidate.Source = truncateResponseText(candidate.Source, 160)
		samples = append(samples, candidate)
	}
	return samples
}

func compactStrings(values []string, limit, runes int) []string {
	if len(values) == 0 {
		return nil
	}
	compacted := make([]string, 0, min(len(values), limit))
	for i, value := range values {
		if i >= limit {
			compacted = append(compacted, fmt.Sprintf("and %d more", len(values)-limit))
			break
		}
		compacted = append(compacted, truncateResponseText(value, runes))
	}
	return compacted
}

// fieldTruncation tracks the original length and latest notice for each
// shortened field of one response entry. The original is retained across
// multiple shortening passes, so a second pass reports the true original.
type fieldTruncation struct {
	originals map[string]int
	notices   map[string]string
	order     []string
}

func newFieldTruncation() *fieldTruncation {
	return &fieldTruncation{
		originals: make(map[string]int),
		notices:   make(map[string]string),
	}
}

func (t *fieldTruncation) record(subject, field string, current, result int) {
	original, ok := t.originals[field]
	if !ok {
		original = current
		t.originals[field] = original
		t.order = append(t.order, field)
	}
	t.notices[field] = fmt.Sprintf(
		"%s %s truncated from %d to %d runes", subject, field, original, result)
}

func (t *fieldTruncation) list() []string {
	if len(t.order) == 0 {
		return nil
	}
	notices := make([]string, 0, len(t.order))
	for _, field := range t.order {
		notices = append(notices, t.notices[field])
	}
	return notices
}

func truncationNotices(states []*fieldTruncation, count int) []string {
	var notices []string
	for i := 0; i < count && i < len(states); i++ {
		notices = append(notices, states[i].list()...)
	}
	return notices
}

// markComponentTruncation shortens the budget-controlled fields of one list
// row. Every shortening sets Truncated and records a warning naming the field,
// its true original rune length, and the actual resulting rune length.
// Limits of zero leave the corresponding field untouched.
func markComponentTruncation(
	component *ComponentSummary,
	state *fieldTruncation,
	tagLimit, summaryLimit, moduleLimit, packageLimit int,
) {
	subject := "component " + truncateResponseText(component.TagName, 100)
	fields := []struct {
		name  string
		value *string
		limit int
	}{
		{"tagName", &component.TagName, tagLimit},
		{"summary", &component.Summary, summaryLimit},
		{"modulePath", &component.ModulePath, moduleLimit},
		{"packageSubpath", &component.PackageSubpath, packageLimit},
	}
	for _, field := range fields {
		if field.limit <= 0 {
			continue
		}
		before := utf8.RuneCountInString(*field.value)
		after := truncateResponseText(*field.value, field.limit)
		if after == *field.value {
			continue
		}
		*field.value = after
		component.Truncated = true
		state.record(subject, field.name, before, utf8.RuneCountInString(after))
	}
}

// markSearchHitTruncation applies the same contract to one search row.
func markSearchHitTruncation(hit *SearchHit, state *fieldTruncation) {
	subject := "search hit " + truncateResponseText(hit.Title, 200)
	fields := []struct {
		name  string
		value *string
		limit int
	}{
		{"title", &hit.Title, 200},
		{"path", &hit.Path, 300},
		{"excerpt", &hit.Excerpt, maxExcerptRunes},
	}
	for _, field := range fields {
		before := utf8.RuneCountInString(*field.value)
		after := truncateResponseText(*field.value, field.limit)
		if after == *field.value {
			continue
		}
		*field.value = after
		hit.Truncated = true
		state.record(subject, field.name, before, utf8.RuneCountInString(after))
	}
}

func fitListComponents(
	out *ListComponentsOutput,
	startOffset int,
	includeDeprecated bool,
	budget int,
) {
	if out.Error != nil {
		out.Error = boundError(out.Error)
		out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
		if estimatedResponseBytes(out) > budget {
			out.Error = responseTooLarge("the component page", budget)
		}
		return
	}
	states := make([]*fieldTruncation, len(out.Components))
	for i := range out.Components {
		states[i] = newFieldTruncation()
		markComponentTruncation(&out.Components[i], states[i], 100, 500, 300, 300)
	}
	baseWarnings := sanitizeWarnings(out.Resolved.Warnings)
	if estimatedResponseBytes(out) > budget {
		baseWarnings = summarizeWarnings(baseWarnings)
	}
	setWarnings := func(extra ...string) {
		base := append([]string(nil), baseWarnings...)
		base = append(base, extra...)
		out.Resolved.Warnings = sanitizeWarningsWithReserved(
			base, truncationNotices(states, len(out.Components)))
	}
	setWarnings()

	const trimWarning = "response limited to fit the budget; continue with nextCursor"
	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Components) > 1 {
		out.Components = out.Components[:len(out.Components)-1]
		trimmed++
		setWarnings()
	}
	if trimmed > 0 {
		setWarnings(trimWarning)
		// Re-size with the trim warning and cursor included. The cursor length
		// depends on the trimmed length, so update it on every iteration.
		for {
			end := startOffset + len(out.Components)
			if end < out.Total {
				out.NextCursor = encodeCursor(out.Resolved.Elements, end, includeDeprecated)
			} else {
				out.NextCursor = ""
			}
			if estimatedResponseBytes(out) <= budget || len(out.Components) <= 1 {
				break
			}
			out.Components = out.Components[:len(out.Components)-1]
			trimmed++
			setWarnings(trimWarning)
		}
	}
	if estimatedResponseBytes(out) > budget && len(out.Components) == 1 {
		markComponentTruncation(&out.Components[0], states[0], 0, 100, 150, 150)
		if trimmed > 0 {
			setWarnings(trimWarning)
		} else {
			setWarnings()
		}
	}
	if estimatedResponseBytes(out) > budget {
		// A single component cannot be served within the budget. Never return
		// an empty page with no cursor, because that silently ends pagination.
		out.Components = nil
		out.NextCursor = ""
		out.Resolved.Warnings = []string{"response trimmed to fit the budget"}
		out.Error = responseTooLarge("the component page", budget)
	}
}

func fitSearch(out *SearchOutput, budget int) {
	if out.Error != nil {
		out.Error = boundError(out.Error)
		out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
		if estimatedResponseBytes(out) > budget {
			out.Error = responseTooLarge("the search response", budget)
			out.Hits = nil
		}
		return
	}
	states := make([]*fieldTruncation, len(out.Hits))
	for i := range out.Hits {
		states[i] = newFieldTruncation()
		markSearchHitTruncation(&out.Hits[i], states[i])
	}
	baseWarnings := sanitizeWarnings(out.Resolved.Warnings)
	setWarnings := func(extra ...string) {
		base := append([]string(nil), baseWarnings...)
		base = append(base, extra...)
		out.Resolved.Warnings = sanitizeWarningsWithReserved(
			base, truncationNotices(states, len(out.Hits)))
	}
	setWarnings()
	if estimatedResponseBytes(out) > budget {
		baseWarnings = summarizeWarnings(baseWarnings)
		setWarnings()
	}

	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Hits) > 0 {
		out.Hits = out.Hits[:len(out.Hits)-1]
		trimmed++
		setWarnings()
	}
	if trimmed > 0 {
		setWarnings(fmt.Sprintf("results trimmed from %d to %d to fit the response budget",
			out.Total, len(out.Hits)))
	}
	if estimatedResponseBytes(out) > budget {
		out.Hits = nil
		out.Resolved.Warnings = []string{"results trimmed to fit the response budget"}
	}
	if estimatedResponseBytes(out) > budget {
		out.Error = responseTooLarge("the search response", budget)
	}
}

func fitResolveVersion(out *ResolveVersionOutput, budget int) {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	out.Candidates = sanitizeCandidates(out.Candidates, &out.Resolved.Warnings)
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if out.Ambiguity != nil {
		if len(out.Ambiguity.Candidates) > 5 {
			out.Resolved.Warnings = append(out.Resolved.Warnings,
				fmt.Sprintf("ambiguity details summarized to 5 of %d candidates",
					len(out.Ambiguity.Candidates)))
		}
		out.Ambiguity.Candidates = ambiguityCandidateSamples(out.Ambiguity.Candidates)
	}
	if out.Error != nil {
		out.Error = boundError(out.Error)
	}
	if estimatedResponseBytes(out) > budget {
		out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
	}

	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Candidates) > 0 {
		out.Candidates = out.Candidates[:len(out.Candidates)-1]
		trimmed++
	}
	if trimmed > 0 {
		out.Resolved.Warnings = sanitizeWarnings(append(out.Resolved.Warnings,
			fmt.Sprintf("%d candidates omitted to fit the response budget", trimmed)))
	}
	// Details and the ambiguity block are secondary to the resolved version.
	// Drop them before replacing the whole response with an error.
	if estimatedResponseBytes(out) > budget {
		if out.Error != nil && out.Ambiguity != nil {
			out.Error.Details = map[string]any{
				"level":      out.Ambiguity.Level,
				"majors":     out.Ambiguity.Majors,
				"candidates": ambiguityCandidateSummary(out.Ambiguity.Candidates),
			}
		}
		if out.Ambiguity != nil {
			out.Ambiguity.Candidates = nil
		}
	}
	if estimatedResponseBytes(out) > budget {
		if out.Error != nil {
			out.Error.Details = nil
		}
	}
	if estimatedResponseBytes(out) > budget {
		out.Resolved.Warnings = []string{"response trimmed to fit the budget"}
	}
	if estimatedResponseBytes(out) > budget {
		out.Ambiguity = nil
	}
	if estimatedResponseBytes(out) > budget {
		out.Candidates = nil
		out.Ambiguity = nil
		out.Error = responseTooLarge("the resolve_version response", budget)
	}
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
}

func fitComponentAPI(out ComponentAPIOutput, budget int, summary bool) ComponentAPIOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if out.Error != nil {
		out.Error = boundError(out.Error)
		if estimatedResponseBytes(out) > budget {
			return ComponentAPIOutput{
				Resolved: out.Resolved,
				Error:    responseTooLarge("the component API response", budget),
				Sections: []string{},
			}
		}
		return out
	}
	// Omission notices describe data that is no longer in the payload, so the
	// warning cap must never discard them. Collect them separately and prepend
	// them at the final sanitize pass, which keeps them ahead of lower-priority
	// resolution warnings.
	omissionWarnings := make([]string, 0, 3)
	if summary {
		for _, optional := range []struct {
			name   string
			drop   func()
			active bool
		}{
			{name: sectionExamples, active: len(out.Examples) > 0, drop: func() { out.Examples = nil }},
			{name: sectionCSSProperties, active: len(out.CSSProperties) > 0, drop: func() { out.CSSProperties = nil }},
			{name: sectionCSSParts, active: len(out.CSSParts) > 0, drop: func() { out.CSSParts = nil }},
		} {
			if componentAPIResponseBytes(out, omissionWarnings) <= budget || !optional.active {
				continue
			}
			optional.drop()
			out.Sections = withoutSection(out.Sections, optional.name)
			omissionWarnings = append(omissionWarnings, fmt.Sprintf(
				"%s omitted to fit the response budget; call again with sections=[\"%s\"]",
				optional.name, optional.name))
		}
	}
	if len(omissionWarnings) > 0 {
		out.Resolved.Warnings = sanitizeWarnings(
			append(omissionWarnings, out.Resolved.Warnings...))
	}
	if estimatedResponseBytes(out) > budget {
		return ComponentAPIOutput{
			Resolved: out.Resolved,
			TagName:  out.TagName,
			Error: &ToolError{
				Code: "response_too_large",
				Message: fmt.Sprintf(
					"the component API response for %s is over the %d byte cap",
					out.TagName, budget),
				Hint: "call this tool again with an explicit subset of sections " +
					"(for example sections=[\"attributes\"]) and detail=summary; " +
					"there is no silent truncation",
			},
			Sections: []string{},
		}
	}
	return out
}

func withoutSection(sections []string, drop string) []string {
	result := make([]string, 0, len(sections))
	for _, section := range sections {
		if section != drop {
			result = append(result, section)
		}
	}
	return result
}

// componentAPIResponseBytes measures the response as it will be sent once the
// pending omission warnings are prepended. Budget checks inside the section
// drop loop must include those warnings; otherwise the final warning can push
// an otherwise-fitting response over the cap and turn it into an error.
func componentAPIResponseBytes(out ComponentAPIOutput, pending []string) int {
	if len(pending) == 0 {
		return estimatedResponseBytes(out)
	}
	original := out.Resolved.Warnings
	out.Resolved.Warnings = append(append([]string(nil), pending...), original...)
	size := estimatedResponseBytes(out)
	out.Resolved.Warnings = original
	return size
}

func fitQueryTokens(out QueryTokensOutput, budget int) QueryTokensOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if out.Error != nil {
		out.Error = boundError(out.Error)
		if estimatedResponseBytes(out) > budget {
			out.Error = responseTooLarge("the token response", budget)
			out.Tokens = nil
		}
		return out
	}
	if estimatedResponseBytes(out) > budget {
		out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
	}
	for i := range out.Tokens {
		result := &out.Tokens[i]
		result.Name = truncateResponseText(result.Name, 300)
		result.CSSVariable = truncateResponseText(result.CSSVariable, 300)
		result.Value = truncateResponseText(result.Value, 500)
		result.RawValue = truncateResponseText(result.RawValue, 500)
		result.Description = truncateResponseText(result.Description, 500)
		result.ThemeValue = truncateResponseText(result.ThemeValue, 300)
		if len(result.AliasChain) > 20 {
			result.AliasChain = result.AliasChain[:20]
		}
		for j := range result.AliasChain {
			result.AliasChain[j] = truncateResponseText(result.AliasChain[j], 200)
		}
	}
	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Tokens) > 0 {
		out.Tokens = out.Tokens[:len(out.Tokens)-1]
		trimmed++
	}
	if trimmed > 0 {
		out.Resolved.Warnings = sanitizeWarnings(append(out.Resolved.Warnings,
			fmt.Sprintf("results trimmed from %d to %d to fit the response budget",
				out.Total, len(out.Tokens))))
	}
	if estimatedResponseBytes(out) > budget {
		out.Tokens = nil
		out.Resolved.Warnings = []string{"results trimmed to fit the response budget"}
	}
	if estimatedResponseBytes(out) > budget {
		out.Error = responseTooLarge("the token response", budget)
	}
	return out
}

func fitValidateMarkup(out ValidateMarkupOutput, budget int) ValidateMarkupOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	out.Warnings = sanitizeWarnings(out.Warnings)
	if out.Error != nil {
		out.Error = boundError(out.Error)
		if estimatedResponseBytes(out) > budget {
			out.Error = responseTooLarge("the markup validation response", budget)
			out.Issues = nil
		}
		return out
	}
	if estimatedResponseBytes(out) > budget {
		out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
		out.Warnings = summarizeWarnings(out.Warnings)
	}
	for i := range out.Issues {
		out.Issues[i].Message = truncateResponseText(out.Issues[i].Message, 500)
	}
	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Issues) > 0 {
		out.Issues = out.Issues[:len(out.Issues)-1]
		trimmed++
	}
	if trimmed > 0 {
		out.Warnings = sanitizeWarnings(append(out.Warnings,
			fmt.Sprintf("%d issues omitted to fit the response budget", trimmed)))
	}
	if estimatedResponseBytes(out) > budget {
		out.Issues = nil
		out.Warnings = []string{"issues trimmed to fit the response budget"}
	}
	if estimatedResponseBytes(out) > budget {
		out.Error = responseTooLarge("the markup validation response", budget)
	}
	return out
}
