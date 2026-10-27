// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/mod/semver"
	"golang.org/x/net/html"

	"github.com/redhat-ux/mcp/internal/cem"
	"github.com/redhat-ux/mcp/internal/markdown"
)

const (
	exampleKindDemo    = "demo"
	exampleKindDocs    = "docs"
	exampleKindPattern = "pattern"
	exampleKindTwig    = "twig"
	exampleKindAll     = "all"
)

var usageExampleKinds = []string{
	exampleKindDemo,
	exampleKindDocs,
	exampleKindPattern,
	exampleKindTwig,
}

// UsageExamplesInput is the input for rhds_get_usage_examples.
type UsageExamplesInput struct {
	TagName string `json:"tagName" jsonschema:"Custom element tag name, for example rh-button"`
	Version string `json:"version,omitempty" jsonschema:"RHDS elements version or docs snapshot tag; defaults to the workspace resolution, then the latest bundled version"`
	Kind    string `json:"kind,omitempty" jsonschema:"Example kind: demo, docs, pattern, or twig; defaults to all"`
}

// UsageExample is one usage example or template.
type UsageExample struct {
	Kind     string `json:"kind"`
	Title    string `json:"title,omitempty"`
	Language string `json:"language,omitempty"`
	Content  string `json:"content"`
	Path     string `json:"path,omitempty"`
	Source   string `json:"source,omitempty"`
	URL      string `json:"url,omitempty"`
	Version  string `json:"version,omitempty"`
	Note     string `json:"note,omitempty"`
}

// UsageExamplesOutput is the output for rhds_get_usage_examples.
type UsageExamplesOutput struct {
	Resolved  ResolvedVersion `json:"resolved"`
	TagName   string          `json:"tagName"`
	Kind      string          `json:"kind"`
	Examples  []UsageExample  `json:"examples"`
	Total     int             `json:"total"`
	Truncated bool            `json:"truncated"`
	Error     *ToolError      `json:"error,omitempty"`
}

// GetUsageExamples implements rhds_get_usage_examples for direct service
// calls.
func (s *Service) GetUsageExamples(_ context.Context, input UsageExamplesInput) UsageExamplesOutput {
	resolved, failure := s.examplesResolution(input.Version)
	if failure != nil {
		return UsageExamplesOutput{Examples: []UsageExample{}, Error: failure}
	}
	return s.usageExamples(resolved, input)
}

func (s *Service) usageExamplesWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input UsageExamplesInput,
) (*mcp.CallToolResult, UsageExamplesOutput) {
	if input.Version != "" && s.store != nil && s.store.Has(packageDocs, input.Version, kindDocs) {
		resolved, failure := s.examplesResolution(input.Version)
		if failure != nil {
			return nil, UsageExamplesOutput{Examples: []UsageExample{}, Error: failure}
		}
		return nil, s.usageExamples(resolved, input)
	}
	resolved, required, failure := s.prepareResolution(
		ctx, newCallEnv(request), input.Version, "", true)
	if required != nil {
		return required, UsageExamplesOutput{}
	}
	if failure != nil {
		return nil, UsageExamplesOutput{Examples: []UsageExample{}, Error: failure}
	}
	return nil, s.usageExamples(resolved, input)
}

func (s *Service) examplesResolution(explicit string) (resolution, *ToolError) {
	if failure := s.dataFailure(); failure != nil {
		return resolution{}, failure
	}
	if explicit != "" && s.store.Has(packageDocs, explicit, kindDocs) {
		elementsVersion, ok := s.elementsVersionForDocsTag(explicit)
		if !ok {
			return resolution{}, &ToolError{
				Code: "artifact_not_found",
				Message: fmt.Sprintf(
					"docs snapshot %s has no matching bundled elements version", explicit),
				Hint: "pass an @rhds/elements version instead",
			}
		}
		resolved := resolution{Resolved: ResolvedVersion{
			Elements: elementsVersion,
			Docs:     explicit,
			Source:   "bundled",
		}}
		if tokensVersion, ok := s.store.Index().TokensForElements(elementsVersion); ok {
			resolved.Resolved.Tokens = tokensVersion
		}
		return resolved, nil
	}
	return s.bundledResolution(explicit, "")
}

// elementsVersionForDocsTag maps a docs tag such as v5.0.0 to the newest
// bundled elements release with the same major.
func (s *Service) elementsVersionForDocsTag(tag string) (string, bool) {
	version := strings.TrimPrefix(tag, "v")
	versions := s.bundledElements()
	if len(versions) == 0 {
		return "", false
	}
	canonical := semver.Canonical("v" + version)
	if canonical != "" {
		for _, candidate := range versions {
			if semver.Canonical("v"+strings.TrimPrefix(candidate, "v")) == canonical {
				return candidate, true
			}
		}
		major := semver.Major(canonical)
		for _, candidate := range versions {
			candidateCanonical := semver.Canonical("v" + strings.TrimPrefix(candidate, "v"))
			if candidateCanonical != "" && semver.Major(candidateCanonical) == major {
				return candidate, true
			}
		}
	}
	return "", false
}

func (s *Service) usageExamples(resolved resolution, input UsageExamplesInput) UsageExamplesOutput {
	out := UsageExamplesOutput{
		Resolved: resolved.Resolved,
		Examples: []UsageExample{},
	}
	kind := input.Kind
	if kind == "" {
		kind = exampleKindAll
	}
	switch kind {
	case exampleKindDemo, exampleKindDocs, exampleKindPattern, exampleKindTwig, exampleKindAll:
	default:
		out.Error = invalidArgument(fmt.Sprintf(
			"kind must be one of %s", strings.Join(usageExampleKinds, ", ")))
		return out
	}
	out.Kind = kind

	tag := normalizeElementTag(input.TagName)
	if tag == "" {
		out.Error = invalidArgument("tagName is required")
		return out
	}
	out.TagName = tag

	model, failure := s.loadModel(resolved.Resolved.Elements)
	if failure != nil {
		out.Error = failure
		return out
	}
	component, ok := model.Component(tag)
	if !ok {
		out.Error = &ToolError{
			Code:    "component_not_found",
			Message: fmt.Sprintf("%q is not a custom element in elements@%s", tag, resolved.Resolved.Elements),
			Hint:    "call rhds_list_components to list bundled tag names",
		}
		return out
	}

	if kind == exampleKindAll || kind == exampleKindDemo {
		out.Examples = append(out.Examples, demoExamples(component, resolved.Resolved.Elements)...)
	}
	if kind == exampleKindAll || kind == exampleKindDocs {
		out.Examples = append(out.Examples, s.docsExamples(resolved.Resolved.Docs, tag)...)
	}
	if kind == exampleKindAll || kind == exampleKindPattern {
		out.Examples = append(out.Examples, s.patternExamples(resolved.Resolved.Docs, tag)...)
	}
	if kind == exampleKindAll || kind == exampleKindTwig {
		out.Examples = append(out.Examples, twigExample(component, resolved.Resolved.Elements))
	}

	out.Total = len(out.Examples)
	budget := s.responseBudget(false)
	if estimatedResponseBytes(out) > budget {
		out.Truncated = true
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("the response budget of %d bytes was reached; use kind to request one example kind", budget))
		for len(out.Examples) > 0 && estimatedResponseBytes(out) > budget {
			out.Examples = out.Examples[:len(out.Examples)-1]
		}
		if len(out.Examples) == 0 && out.Total > 0 {
			out.Error = &ToolError{
				Code: "response_too_large",
				Message: fmt.Sprintf(
					"no usage example fits the %d byte response budget", budget),
				Hint: "request one kind at a time",
			}
		}
	}
	return out
}

func demoExamples(component *cem.Component, elementsVersion string) []UsageExample {
	examples := make([]UsageExample, 0, len(component.Examples))
	for _, demo := range component.Examples {
		title := demo.Description
		if title == "" {
			title = component.TagName + " demo"
		}
		examples = append(examples, UsageExample{
			Kind:    exampleKindDemo,
			Title:   title,
			URL:     demo.URL,
			Source:  demo.Source,
			Version: elementsVersion,
		})
	}
	return examples
}

func (s *Service) docsExamples(docsVersion, tag string) []UsageExample {
	if docsVersion == "" {
		return nil
	}
	records, failure := s.docsRecords(docsVersion)
	if failure != nil {
		return nil
	}
	var examples []UsageExample
	for _, record := range records {
		if !elementDocPath(record.Path, tag) {
			continue
		}
		content, err := s.store.Read(packageDocs, docsVersion, record.Kind)
		if err != nil {
			continue
		}
		for _, block := range markdown.CodeBlocks(string(content)) {
			if !strings.Contains(strings.ToLower(block.Content), tag) {
				continue
			}
			examples = append(examples, UsageExample{
				Kind:     exampleKindDocs,
				Title:    record.Title,
				Language: block.Language,
				Content:  block.Content,
				Path:     record.Path,
				Source:   record.Source,
				Version:  docsVersion,
			})
		}
	}
	return examples
}

func (s *Service) patternExamples(docsVersion, tag string) []UsageExample {
	if docsVersion == "" {
		return nil
	}
	records, failure := s.patternRecords(docsVersion)
	if failure != nil {
		return nil
	}
	var examples []UsageExample
	for _, record := range records {
		if !htmlContainsTag(record.HTML, tag) {
			continue
		}
		examples = append(examples, UsageExample{
			Kind:     exampleKindPattern,
			Title:    record.Path,
			Language: "html",
			Content:  record.HTML,
			Path:     record.Path,
			Source:   record.Source,
			Version:  docsVersion,
		})
	}
	return examples
}

func twigExample(component *cem.Component, elementsVersion string) UsageExample {
	var builder strings.Builder
	builder.WriteString("{# Generic Twig markup for " + component.TagName + ". Slot names come from the CEM. #}\n")
	builder.WriteString("<" + component.TagName + ">\n")
	if len(component.Slots) == 0 {
		builder.WriteString("  {{ content }}\n")
	}
	for _, slot := range component.Slots {
		if slot.Name == "" {
			builder.WriteString("  {% block content %}...{% endblock %}\n")
			continue
		}
		fmt.Fprintf(&builder, "  <div slot=%q>{%% block %s %%}...{%% endblock %%}</div>\n",
			slot.Name, twigBlockName(slot.Name))
	}
	builder.WriteString("</" + component.TagName + ">\n")
	return UsageExample{
		Kind:     exampleKindTwig,
		Title:    component.TagName + " Twig slots",
		Language: "twig",
		Content:  builder.String(),
		Version:  elementsVersion,
		Note:     "Generic Twig markup. Slot names come from the CEM; this is not a Drupal library key.",
	}
}

func twigBlockName(slot string) string {
	var builder strings.Builder
	builder.WriteString("slot_")
	for _, r := range slot {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

// htmlContainsTag reports whether parsed HTML contains the given custom
// element. It uses the HTML tokenizer, never a regular expression.
func htmlContainsTag(markup, tag string) bool {
	tokenizer := html.NewTokenizer(strings.NewReader(markup))
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			if strings.EqualFold(string(name), tag) {
				return true
			}
		}
	}
}
