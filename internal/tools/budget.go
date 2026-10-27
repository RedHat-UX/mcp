// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"encoding/json"
	"fmt"
	"strings"

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
	if len(warnings) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(warnings))
	out := make([]string, 0, min(len(warnings), maxResponseWarnings))
	dropped := 0
	for _, warning := range warnings {
		warning = truncateResponseText(strings.TrimSpace(warning), maxWarningRunes)
		if warning == "" || seen[warning] {
			continue
		}
		seen[warning] = true
		if len(out) >= maxResponseWarnings {
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
	for i := range out.Components {
		component := &out.Components[i]
		component.TagName = truncateResponseText(component.TagName, 100)
		component.Summary = truncateResponseText(component.Summary, 500)
		component.ModulePath = truncateResponseText(component.ModulePath, 300)
		component.PackageSubpath = truncateResponseText(component.PackageSubpath, 300)
	}
	baseWarnings := sanitizeWarnings(out.Resolved.Warnings)
	if estimatedResponseBytes(out) > budget {
		baseWarnings = summarizeWarnings(baseWarnings)
	}
	out.Resolved.Warnings = baseWarnings

	const trimWarning = "response limited to fit the budget; continue with nextCursor"
	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Components) > 1 {
		out.Components = out.Components[:len(out.Components)-1]
		trimmed++
	}
	if trimmed > 0 {
		out.Resolved.Warnings = sanitizeWarnings(append(baseWarnings, trimWarning))
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
		}
	}
	if estimatedResponseBytes(out) > budget && len(out.Components) == 1 {
		out.Components[0].Summary = truncateResponseText(out.Components[0].Summary, 100)
		out.Components[0].ModulePath = truncateResponseText(out.Components[0].ModulePath, 150)
		out.Components[0].PackageSubpath = truncateResponseText(out.Components[0].PackageSubpath, 150)
	}
	if estimatedResponseBytes(out) > budget {
		// A single component cannot be served within the budget. Never return
		// an empty page with no cursor, because that silently ends pagination.
		out.Components = nil
		out.NextCursor = ""
		out.Resolved.Warnings = []string{"response trimmed to fit the budget"}
		out.Error = responseTooLarge("the component page", budget)
	}
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
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
	for i := range out.Hits {
		hit := &out.Hits[i]
		hit.Title = truncateResponseText(hit.Title, 200)
		hit.Path = truncateResponseText(hit.Path, 300)
		hit.Excerpt = truncateResponseText(hit.Excerpt, maxExcerptRunes)
	}
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if estimatedResponseBytes(out) > budget {
		out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
	}

	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Hits) > 0 {
		out.Hits = out.Hits[:len(out.Hits)-1]
		trimmed++
	}
	if trimmed > 0 {
		out.Resolved.Warnings = sanitizeWarnings(append(out.Resolved.Warnings,
			fmt.Sprintf("results trimmed from %d to %d to fit the response budget",
				out.Total, len(out.Hits))))
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
