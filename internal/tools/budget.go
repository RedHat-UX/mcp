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

// markCandidateTruncation shortens one candidate's fields with the fixed caps
// and marks it when anything changed.
func markCandidateTruncation(candidate *resolve.Candidate) bool {
	truncated := false
	if after := truncateResponseText(candidate.Version, maxCandidateRunes); after != candidate.Version {
		candidate.Version = after
		truncated = true
	}
	if after := truncateResponseText(candidate.Spec, maxCandidateRunes); after != candidate.Spec {
		candidate.Spec = after
		truncated = true
	}
	if after := truncateResponseText(candidate.Source, 300); after != candidate.Source {
		candidate.Source = after
		truncated = true
	}
	candidate.Truncated = candidate.Truncated || truncated
	return truncated
}

// boundedCandidates caps the count and shortens fields for bounded error
// details. The shortened candidates are marked so the caller can report it.
func boundedCandidates(candidates []resolve.Candidate, limit int) []resolve.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	bounded := make([]resolve.Candidate, 0, min(len(candidates), limit))
	for i, candidate := range candidates {
		if i >= limit {
			break
		}
		markCandidateTruncation(&candidate)
		bounded = append(bounded, candidate)
	}
	return bounded
}

// sanitizeCandidates caps the candidate count. Field shortening is a budget
// measure and happens later, only when the response is actually over budget.
func sanitizeCandidates(candidates []resolve.Candidate, warnings *[]string) []resolve.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	dropped := 0
	if len(candidates) > maxOutputCandidates {
		dropped = len(candidates) - maxOutputCandidates
		candidates = candidates[:maxOutputCandidates]
	}
	if dropped > 0 {
		*warnings = append(*warnings,
			fmt.Sprintf("%d additional candidates omitted; pass a more specific workspace root or version argument", dropped))
	}
	return candidates
}

// truncateCandidateFields shortens the surviving candidates and returns how
// many were changed.
func truncateCandidateFields(candidates []resolve.Candidate) int {
	count := 0
	for i := range candidates {
		if markCandidateTruncation(&candidates[i]) {
			count++
		}
	}
	return count
}

func countTruncatedCandidates(candidates []resolve.Candidate) int {
	count := 0
	for _, candidate := range candidates {
		if candidate.Truncated {
			count++
		}
	}
	return count
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
	summary := make([]string, 0, limit)
	for i, candidate := range candidates {
		if i == limit-1 && len(candidates) > limit {
			summary = append(summary,
				fmt.Sprintf("and %d more candidates", len(candidates)-i))
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
		if after := truncateResponseText(candidate.Version, 96); after != candidate.Version {
			candidate.Version = after
			candidate.Truncated = true
		}
		if candidate.Spec != "" {
			candidate.Spec = ""
			candidate.Truncated = true
		}
		if after := truncateResponseText(candidate.Source, 160); after != candidate.Source {
			candidate.Source = after
			candidate.Truncated = true
		}
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
	t.recordUnit(subject, field, current, result, "runes")
}

// recordUnit is record with an explicit unit, so the alias-chain entry cap can
// report entries rather than runes.
func (t *fieldTruncation) recordUnit(subject, field string, current, result int, unit string) {
	original, ok := t.originals[field]
	if !ok {
		original = current
		t.originals[field] = original
		t.order = append(t.order, field)
	}
	t.notices[field] = fmt.Sprintf(
		"%s %s truncated from %d to %d %s", subject, field, original, result, unit)
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
			out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
		}
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = nil
		}
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
	if len(truncationNotices(states, len(out.Components))) > 0 {
		out.Truncated = true
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
		out.Truncated = true
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
		out.Truncated = true
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
		out.Truncated = true
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
			out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
		}
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = nil
		}
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
		if out.Hits[i].Truncated {
			out.Truncated = true
		}
	}
	baseWarnings := sanitizeWarnings(out.Resolved.Warnings)
	setWarnings := func(extra ...string) {
		reserved := truncationNotices(states, len(out.Hits))
		reserved = append(reserved, extra...)
		out.Resolved.Warnings = sanitizeWarningsWithReserved(baseWarnings, reserved)
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
		out.Truncated = true
		// The trim notice adds bytes. Keep trimming with it included so the
		// notice never turns a trimmable page into an empty one.
		for {
			setWarnings(fmt.Sprintf("results trimmed from %d to %d to fit the response budget",
				out.Total, len(out.Hits)))
			if estimatedResponseBytes(out) <= budget || len(out.Hits) == 0 {
				break
			}
			out.Hits = out.Hits[:len(out.Hits)-1]
		}
		if estimatedResponseBytes(out) > budget {
			out.Hits = nil
			setWarnings("results trimmed to fit the response budget")
		}
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

	baseWarnings := out.Resolved.Warnings
	setWarnings := func(extra ...string) {
		out.Resolved.Warnings = sanitizeWarningsWithReserved(baseWarnings, extra)
	}
	setWarnings()
	if estimatedResponseBytes(out) > budget {
		baseWarnings = summarizeWarnings(baseWarnings)
		setWarnings()
	}

	// Field shortening is itself a budget measure: apply it only when over.
	if estimatedResponseBytes(out) > budget {
		if changed := truncateCandidateFields(out.Candidates); changed > 0 {
			out.Truncated = true
		}
	}

	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Candidates) > 0 {
		out.Candidates = out.Candidates[:len(out.Candidates)-1]
		trimmed++
	}
	if trimmed > 0 {
		out.Truncated = true
	}
	// Recompute notices and re-trim if they push the payload over the cap.
	for {
		var extra []string
		if shortened := countTruncatedCandidates(out.Candidates); shortened > 0 {
			extra = append(extra, fmt.Sprintf(
				"%d candidates had fields shortened to fit the response budget", shortened))
		}
		if trimmed > 0 {
			extra = append(extra, fmt.Sprintf(
				"%d candidates omitted to fit the response budget", trimmed))
		}
		setWarnings(extra...)
		if estimatedResponseBytes(out) <= budget || len(out.Candidates) == 0 {
			break
		}
		out.Candidates = out.Candidates[:len(out.Candidates)-1]
		trimmed++
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
		out.Ambiguity = nil
	}
	if estimatedResponseBytes(out) > budget {
		out.Candidates = nil
		out.Ambiguity = nil
		out.Error = responseTooLarge("the resolve_version response", budget)
	}
}

func fitComponentAPI(out ComponentAPIOutput, budget int, summary bool) ComponentAPIOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if out.Error != nil {
		out.Error = boundError(out.Error)
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
		}
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = nil
		}
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

// fitDesignGuidelines bounds every return path of rhds_get_design_guidelines.
// It caps the caller-supplied topic and element echoed in the output, bounds
// errors, and trims chunks until the response fits.
func fitDesignGuidelines(out DesignGuidelinesOutput, budget int) DesignGuidelinesOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	out.Topic = truncateResponseText(out.Topic, maxCandidateRunes)
	out.Element = truncateResponseText(out.Element, maxCandidateRunes)
	if out.Error != nil {
		out.Error = boundError(out.Error)
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
		}
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = nil
		}
		if estimatedResponseBytes(out) > budget {
			out.Error = responseTooLarge("the design guidelines response", budget)
			out.Chunks = nil
		}
		return out
	}
	if estimatedResponseBytes(out) <= budget {
		return out
	}

	hadChunks := len(out.Chunks) > 0
	out.Truncated = true
	out.Resolved.Warnings = append(out.Resolved.Warnings,
		fmt.Sprintf("the response budget of %d bytes was reached; narrow the topic or element", budget))
	for estimatedResponseBytes(out) > budget && len(out.Chunks) > 0 {
		out.Chunks = out.Chunks[:len(out.Chunks)-1]
	}
	if estimatedResponseBytes(out) > budget {
		out.Error = responseTooLarge("the design guidelines response", budget)
		out.Chunks = nil
		return out
	}
	if hadChunks && len(out.Chunks) == 0 {
		out.Error = &ToolError{
			Code:    "response_too_large",
			Message: fmt.Sprintf("no guideline chunk fits the %d byte response budget", budget),
			Hint:    "narrow the topic or element",
		}
	}
	return out
}

// fitUsageExamples bounds every return path of rhds_get_usage_examples. It
// caps the echoed tag name, bounds errors, and trims examples until the
// response fits.
func fitUsageExamples(out UsageExamplesOutput, budget int) UsageExamplesOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	out.TagName = truncateResponseText(out.TagName, maxCandidateRunes)
	if out.Error != nil {
		out.Error = boundError(out.Error)
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
		}
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = nil
		}
		if estimatedResponseBytes(out) > budget {
			out.Error = responseTooLarge("the usage examples response", budget)
			out.Examples = nil
		}
		return out
	}
	if estimatedResponseBytes(out) <= budget {
		return out
	}

	hadExamples := len(out.Examples) > 0
	out.Truncated = true
	out.Resolved.Warnings = append(out.Resolved.Warnings,
		fmt.Sprintf("the response budget of %d bytes was reached; use kind to request one example kind or detail=full", budget))
	for estimatedResponseBytes(out) > budget && len(out.Examples) > 0 {
		out.Examples = out.Examples[:len(out.Examples)-1]
	}
	if estimatedResponseBytes(out) > budget {
		out.Error = responseTooLarge("the usage examples response", budget)
		out.Examples = nil
		return out
	}
	if hadExamples && len(out.Examples) == 0 {
		out.Error = &ToolError{
			Code:    "response_too_large",
			Message: fmt.Sprintf("no usage example fits the %d byte response budget", budget),
			Hint:    "request one kind at a time or use detail=full",
		}
	}
	return out
}

func fitValidateMarkup(out ValidateMarkupOutput, budget int) ValidateMarkupOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	out.Warnings = sanitizeWarnings(out.Warnings)
	if out.Error != nil {
		out.Error = boundError(out.Error)
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
			out.Warnings = summarizeWarnings(out.Warnings)
		}
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = nil
			out.Warnings = nil
		}
		if estimatedResponseBytes(out) > budget {
			out.Error = responseTooLarge("the markup validation response", budget)
			out.Issues = nil
		}
		return out
	}
	states := make([]*fieldTruncation, len(out.Issues))
	for i := range out.Issues {
		states[i] = newFieldTruncation()
		issue := &out.Issues[i]
		before := utf8.RuneCountInString(issue.Message)
		after := truncateResponseText(issue.Message, 500)
		if after == issue.Message {
			continue
		}
		issue.Message = after
		issue.Truncated = true
		out.Truncated = true
		states[i].record("markup issue "+issue.Rule, "message", before, utf8.RuneCountInString(after))
	}
	baseWarnings := out.Warnings
	baseResolved := out.Resolved.Warnings
	setWarnings := func(extra ...string) {
		reserved := truncationNotices(states, len(out.Issues))
		reserved = append(reserved, extra...)
		out.Warnings = sanitizeWarningsWithReserved(baseWarnings, reserved)
		out.Resolved.Warnings = sanitizeWarningsWithReserved(baseResolved, nil)
	}
	setWarnings()
	if estimatedResponseBytes(out) > budget {
		baseWarnings = summarizeWarnings(baseWarnings)
		baseResolved = summarizeWarnings(baseResolved)
		setWarnings()
	}

	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Issues) > 0 {
		out.Issues = out.Issues[:len(out.Issues)-1]
		trimmed++
		setWarnings()
	}
	if trimmed > 0 {
		out.Truncated = true
		for {
			setWarnings(fmt.Sprintf("%d issues omitted to fit the response budget", trimmed))
			if estimatedResponseBytes(out) <= budget || len(out.Issues) == 0 {
				break
			}
			out.Issues = out.Issues[:len(out.Issues)-1]
			trimmed++
		}
		if estimatedResponseBytes(out) > budget {
			out.Issues = nil
			setWarnings("issues trimmed to fit the response budget")
		}
	}
	if estimatedResponseBytes(out) > budget {
		out.Error = responseTooLarge("the markup validation response", budget)
	}
	return out
}

// responseBudget returns the NFR-03 budget for a tool call. Summary responses
// use the 8k default; full responses may use the 32k hard cap. Tests lower
// maxResponseBytes to exercise follow-up behavior with small fixtures.
func (s *Service) responseBudget(full bool) int {
	budget := defaultResponseBudget
	if full {
		budget = hardResponseBudget
	}
	if s != nil && s.maxResponseBytes > 0 && s.maxResponseBytes < budget {
		budget = s.maxResponseBytes
	}
	return budget
}

// markTokenTruncation shortens the budget-controlled fields and alias chain of
// one token row. Every shortening sets Truncated and records a notice naming
// the field, its true original length, and the actual resulting length.
func markTokenTruncation(result *TokenResult, state *fieldTruncation) {
	subject := "token " + truncateResponseText(result.Name, 300)
	fields := []struct {
		name  string
		value *string
		limit int
	}{
		{"name", &result.Name, 300},
		{"cssVariable", &result.CSSVariable, 300},
		{"value", &result.Value, 500},
		{"rawValue", &result.RawValue, 500},
		{"description", &result.Description, 500},
		{"themeValue", &result.ThemeValue, 300},
	}
	for _, field := range fields {
		before := utf8.RuneCountInString(*field.value)
		after := truncateResponseText(*field.value, field.limit)
		if after == *field.value {
			continue
		}
		*field.value = after
		result.Truncated = true
		state.record(subject, field.name, before, utf8.RuneCountInString(after))
	}
	if len(result.AliasChain) > 20 {
		before := len(result.AliasChain)
		result.AliasChain = result.AliasChain[:20]
		result.Truncated = true
		state.recordUnit(subject, "aliasChain", before, len(result.AliasChain), "entries")
	}
	for i := range result.AliasChain {
		before := utf8.RuneCountInString(result.AliasChain[i])
		after := truncateResponseText(result.AliasChain[i], 200)
		if after == result.AliasChain[i] {
			continue
		}
		result.AliasChain[i] = after
		result.Truncated = true
		state.record(subject, fmt.Sprintf("aliasChain[%d]", i), before, utf8.RuneCountInString(after))
	}
}

// fitQueryTokensPage trims one token page to the wire budget. Truncation and
// trim notices are reserved from the warning cap and describe only the entries
// that survive. The cursor always points at the first token that did not fit,
// so trimming is never silent and every result remains reachable.
func fitQueryTokensPage(out QueryTokensOutput, budget, offset int) QueryTokensOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if out.Error != nil {
		out.Error = boundError(out.Error)
		if estimatedResponseBytes(out) > budget {
			out.Error = responseTooLarge("the token response", budget)
			out.Tokens = nil
		}
		return out
	}
	baseWarnings := out.Resolved.Warnings
	states := make([]*fieldTruncation, len(out.Tokens))
	for i := range out.Tokens {
		states[i] = newFieldTruncation()
		markTokenTruncation(&out.Tokens[i], states[i])
	}
	setWarnings := func(extra ...string) {
		reserved := truncationNotices(states, len(out.Tokens))
		reserved = append(reserved, extra...)
		out.Resolved.Warnings = sanitizeWarningsWithReserved(baseWarnings, reserved)
	}
	setWarnings()
	// Truncate first: if that alone fits, substantive warnings survive. Only
	// summarize them when the page is still over budget.
	if estimatedResponseBytes(out) > budget && len(baseWarnings) > 1 {
		baseWarnings = summarizeWarnings(baseWarnings)
		setWarnings()
	}

	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Tokens) > 0 {
		out.Tokens = out.Tokens[:len(out.Tokens)-1]
		trimmed++
		setWarnings()
	}
	if trimmed > 0 {
		out.Truncated = true
		// Recompute the warning and cursor inside the loop so both describe
		// and account for the final page.
		for {
			trimWarning := fmt.Sprintf("results trimmed from %d to %d to fit the response budget",
				out.Total, len(out.Tokens))
			setWarnings(trimWarning)
			if offset+len(out.Tokens) < out.Total {
				out.NextCursor = encodeCursor(out.Resolved.Tokens, offset+len(out.Tokens), false)
			} else {
				out.NextCursor = ""
			}
			if estimatedResponseBytes(out) <= budget || len(out.Tokens) == 0 {
				break
			}
			out.Tokens = out.Tokens[:len(out.Tokens)-1]
		}
		if len(out.Tokens) == 0 && offset < out.Total {
			out.NextCursor = ""
			setWarnings("results trimmed to fit the response budget")
			out.Error = responseTooLarge("the token response", budget)
			return out
		}
	}
	if estimatedResponseBytes(out) > budget {
		out.Tokens = nil
		out.NextCursor = ""
		setWarnings("results trimmed to fit the response budget")
		out.Error = responseTooLarge("the token response", budget)
	}
	return out
}

// migrationCategoryCounts holds the post-limit selection counts used to
// report what the budget dropped. These are the entries the caller asked to
// see, not the pre-limit totals.
type migrationCategoryCounts struct {
	changes int
	notes   int
	tokens  int
}

// fitMigrationGuide allocates the wire budget across the selected categories
// instead of dropping one whole category first. It computes item wire sizes
// once, keeps at least one item from every non-empty category when possible,
// then trims from the largest category until the response fits. The minimum is
// re-applied after the final trim so a late warning cannot empty a category.
func fitMigrationGuide(out MigrationGuideOutput, budget int) MigrationGuideOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if out.Error != nil {
		out.Error = boundError(out.Error)
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
		}
		if estimatedResponseBytes(out) > budget {
			out.Resolved.Warnings = nil
		}
		if estimatedResponseBytes(out) > budget {
			out.Error = responseTooLarge("the migration guide", budget)
			out.Changes = nil
			out.ReleaseNotes = nil
			out.TokenChanges = nil
		}
		return out
	}
	if estimatedResponseBytes(out) <= budget {
		return out
	}

	baseWarnings := out.Resolved.Warnings
	selected := migrationCategoryCounts{
		changes: len(out.Changes),
		notes:   len(out.ReleaseNotes),
		tokens:  len(out.TokenChanges),
	}
	out.Truncated = true

	base := out
	base.Changes = nil
	base.ReleaseNotes = nil
	base.TokenChanges = nil
	base.Resolved.Warnings = migrationGuideWarnings(baseWarnings, out, selected)
	baseWire := estimatedResponseBytes(base)
	if baseWire >= budget {
		return migrationGuideTooLarge(out, budget)
	}
	availableWire := budget - baseWire

	changeSizes := migrationItemWireSizes(out.Changes)
	noteSizes := migrationItemWireSizes(out.ReleaseNotes)
	tokenSizes := migrationItemWireSizes(out.TokenChanges)
	changeWire := sumInts(changeSizes)
	noteWire := sumInts(noteSizes)
	tokenWire := sumInts(tokenSizes)
	totalWire := changeWire + noteWire + tokenWire
	if totalWire <= 0 {
		return out
	}

	keepChanges := allocationCount(changeSizes, changeWire, totalWire, availableWire)
	keepNotes := allocationCount(noteSizes, noteWire, totalWire, availableWire)
	keepTokens := allocationCount(tokenSizes, tokenWire, totalWire, availableWire)
	if len(out.Changes) > 0 && keepChanges == 0 {
		keepChanges = 1
	}
	if len(out.ReleaseNotes) > 0 && keepNotes == 0 {
		keepNotes = 1
	}
	if len(out.TokenChanges) > 0 && keepTokens == 0 {
		keepTokens = 1
	}
	out.Changes = out.Changes[:keepChanges]
	out.ReleaseNotes = out.ReleaseNotes[:keepNotes]
	out.TokenChanges = out.TokenChanges[:keepTokens]

	for estimatedResponseBytes(out) > budget {
		if !trimLargestMigrationCategory(&out) {
			break
		}
	}
	out = enforceMigrationMinimum(out, selected)
	for {
		out.Resolved.Warnings = migrationGuideWarnings(baseWarnings, out, selected)
		if estimatedResponseBytes(out) <= budget {
			return out
		}
		if !trimMigrationAboveMinimum(&out) {
			return migrationGuideTooLarge(out, budget)
		}
	}
}

// migrationGuideWarnings reports the generic budget notice plus explicit
// omitted counts per category.
func migrationGuideWarnings(
	base []string,
	out MigrationGuideOutput,
	selected migrationCategoryCounts,
) []string {
	notices := []string{
		"the response budget was reached; use sections or limit to request one category",
	}
	if omitted := selected.changes - len(out.Changes); omitted > 0 {
		notices = append(notices,
			fmt.Sprintf("omitted %d of %d changes to fit the response budget", omitted, selected.changes))
	}
	if omitted := selected.notes - len(out.ReleaseNotes); omitted > 0 {
		notices = append(notices,
			fmt.Sprintf("omitted %d of %d release notes to fit the response budget", omitted, selected.notes))
	}
	if omitted := selected.tokens - len(out.TokenChanges); omitted > 0 {
		notices = append(notices,
			fmt.Sprintf("omitted %d of %d token changes to fit the response budget", omitted, selected.tokens))
	}
	return sanitizeWarningsWithReserved(base, notices)
}

// enforceMigrationMinimum restores the first item of every non-empty category
// after trimming. The underlying slices still hold that item, so a zero-length
// slice with spare capacity can be reopened without copying.
func enforceMigrationMinimum(out MigrationGuideOutput, selected migrationCategoryCounts) MigrationGuideOutput {
	if selected.changes > 0 && len(out.Changes) == 0 && cap(out.Changes) > 0 {
		out.Changes = out.Changes[:1]
	}
	if selected.notes > 0 && len(out.ReleaseNotes) == 0 && cap(out.ReleaseNotes) > 0 {
		out.ReleaseNotes = out.ReleaseNotes[:1]
	}
	if selected.tokens > 0 && len(out.TokenChanges) == 0 && cap(out.TokenChanges) > 0 {
		out.TokenChanges = out.TokenChanges[:1]
	}
	return out
}

func migrationGuideTooLarge(out MigrationGuideOutput, budget int) MigrationGuideOutput {
	out.Changes = nil
	out.ReleaseNotes = nil
	out.TokenChanges = nil
	out.Error = responseTooLarge("the migration guide", budget)
	return out
}

func migrationItemWireSizes[T any](items []T) []int {
	sizes := make([]int, len(items))
	for i, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			sizes[i] = 1
			continue
		}
		escaped, err := json.Marshal(string(encoded))
		if err != nil {
			sizes[i] = len(encoded) + 1
			continue
		}
		// Add one byte for the comma separating the item from its neighbors.
		sizes[i] = len(encoded) + len(escaped) + 1
	}
	return sizes
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func allocationCount(sizes []int, categoryWire, totalWire, availableWire int) int {
	if len(sizes) == 0 || totalWire <= 0 || availableWire <= 0 {
		return 0
	}
	categoryBudget := categoryWire * availableWire / totalWire
	used := 0
	keep := 0
	for _, size := range sizes {
		if used+size > categoryBudget {
			break
		}
		used += size
		keep++
	}
	return keep
}

func trimLargestMigrationCategory(out *MigrationGuideOutput) bool {
	switch {
	case len(out.Changes) >= len(out.ReleaseNotes) &&
		len(out.Changes) >= len(out.TokenChanges) &&
		len(out.Changes) > 0:
		out.Changes = out.Changes[:len(out.Changes)-1]
		return true
	case len(out.ReleaseNotes) >= len(out.Changes) &&
		len(out.ReleaseNotes) >= len(out.TokenChanges) &&
		len(out.ReleaseNotes) > 0:
		out.ReleaseNotes = out.ReleaseNotes[:len(out.ReleaseNotes)-1]
		return true
	case len(out.TokenChanges) > 0:
		out.TokenChanges = out.TokenChanges[:len(out.TokenChanges)-1]
		return true
	default:
		return false
	}
}

// trimMigrationAboveMinimum removes one item from the largest category that
// still has more than one entry, preserving the last representation of every
// category that survived allocation.
func trimMigrationAboveMinimum(out *MigrationGuideOutput) bool {
	switch {
	case len(out.Changes) > 1 &&
		len(out.Changes) >= len(out.ReleaseNotes) &&
		len(out.Changes) >= len(out.TokenChanges):
		out.Changes = out.Changes[:len(out.Changes)-1]
		return true
	case len(out.ReleaseNotes) > 1 &&
		len(out.ReleaseNotes) >= len(out.Changes) &&
		len(out.ReleaseNotes) >= len(out.TokenChanges):
		out.ReleaseNotes = out.ReleaseNotes[:len(out.ReleaseNotes)-1]
		return true
	case len(out.TokenChanges) > 1:
		out.TokenChanges = out.TokenChanges[:len(out.TokenChanges)-1]
		return true
	default:
		return false
	}
}
