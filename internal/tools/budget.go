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
// sends structured output twice (structuredContent and a text content block),
// so the estimate doubles the JSON size rather than measuring only the struct.
func estimatedResponseBytes(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)*2 + 128
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

func fitListComponents(
	out *ListComponentsOutput,
	startOffset int,
	includeDeprecated bool,
	budget int,
) {
	if out.Error != nil {
		out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
		return
	}
	for i := range out.Components {
		component := &out.Components[i]
		component.TagName = truncateResponseText(component.TagName, 100)
		component.Summary = truncateResponseText(component.Summary, 500)
		component.ModulePath = truncateResponseText(component.ModulePath, 300)
		component.PackageSubpath = truncateResponseText(component.PackageSubpath, 300)
	}
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if estimatedResponseBytes(out) > budget {
		out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
	}

	trimmed := 0
	for estimatedResponseBytes(out) > budget && len(out.Components) > 1 {
		out.Components = out.Components[:len(out.Components)-1]
		trimmed++
	}
	if trimmed > 0 {
		end := startOffset + len(out.Components)
		if end < out.Total {
			out.NextCursor = encodeCursor(out.Resolved.Elements, end, includeDeprecated)
		}
		out.Resolved.Warnings = sanitizeWarnings(append(out.Resolved.Warnings,
			fmt.Sprintf("response limited to %d components to fit the budget; continue with nextCursor",
				len(out.Components))))
	}
	if estimatedResponseBytes(out) > budget {
		out.Components = nil
		out.NextCursor = ""
		out.Error = responseTooLarge("the component page", budget)
	}
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
}

func fitSearch(out *SearchOutput, budget int) {
	if out.Error != nil {
		out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
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
	if estimatedResponseBytes(out) > budget {
		out.Resolved.Warnings = summarizeWarnings(out.Resolved.Warnings)
		out.Candidates = boundedCandidates(out.Candidates, maxOutputCandidates)
	}
	if out.Ambiguity != nil {
		if len(out.Ambiguity.Candidates) > maxOutputCandidates {
			out.Resolved.Warnings = append(out.Resolved.Warnings,
				fmt.Sprintf("%d ambiguity candidates omitted from details",
					len(out.Ambiguity.Candidates)-maxOutputCandidates))
		}
		out.Ambiguity.Candidates = boundedCandidates(out.Ambiguity.Candidates, maxOutputCandidates)
	}
	if out.Error != nil {
		out.Error.Message = truncateResponseText(out.Error.Message, maxWarningRunes)
		out.Error.Hint = truncateResponseText(out.Error.Hint, maxWarningRunes)
		if details, ok := out.Error.Details.(map[string]any); ok {
			if candidates, ok := details["candidates"].([]resolve.Candidate); ok {
				details["candidates"] = boundedCandidates(candidates, maxOutputCandidates)
			}
		}
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
	if estimatedResponseBytes(out) > budget {
		out.Resolved.Warnings = []string{"response trimmed to fit the budget"}
	}
	if estimatedResponseBytes(out) > budget {
		out.Error = responseTooLarge("the resolve_version response", budget)
	}
}

func fitComponentAPI(out ComponentAPIOutput, budget int) ComponentAPIOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if out.Error != nil {
		return out
	}
	if estimatedResponseBytes(out) > budget {
		return ComponentAPIOutput{
			Resolved: out.Resolved,
			TagName:  out.TagName,
			Sections: out.Sections,
			Error: &ToolError{
				Code: "response_too_large",
				Message: fmt.Sprintf(
					"the component API response for %s is over the %d byte cap",
					out.TagName, budget),
				Hint: "call this tool again with a subset of sections and detail=summary; " +
					"there is no silent truncation",
			},
		}
	}
	return out
}

func fitQueryTokens(out QueryTokensOutput, budget int) QueryTokensOutput {
	out.Resolved.Warnings = sanitizeWarnings(out.Resolved.Warnings)
	if out.Error != nil {
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
