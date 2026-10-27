// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/cem"
)

// Component API sections accepted by rhds_get_component_api.
const (
	sectionAttributes    = "attributes"
	sectionProperties    = "properties"
	sectionEvents        = "events"
	sectionSlots         = "slots"
	sectionCSSParts      = "cssParts"
	sectionCSSProperties = "cssProperties"
	sectionExamples      = "examples"
)

var componentAPISections = []string{
	sectionAttributes,
	sectionProperties,
	sectionEvents,
	sectionSlots,
	sectionCSSParts,
	sectionCSSProperties,
	sectionExamples,
}

// GetComponentAPIInput is the input for rhds_get_component_api.
type GetComponentAPIInput struct {
	TagName  string   `json:"tagName" jsonschema:"Custom element tag name, for example rh-button"`
	Version  string   `json:"version,omitempty" jsonschema:"RHDS elements version; defaults to the workspace resolution, then the latest bundled version"`
	Sections []string `json:"sections,omitempty" jsonschema:"Sections to return: attributes, properties, events, slots, cssParts, cssProperties, examples"`
	Detail   string   `json:"detail,omitempty" jsonschema:"Detail level: summary (default) or full"`
}

// ComponentAPIOutput is the output for rhds_get_component_api.
type ComponentAPIOutput struct {
	Resolved       ResolvedVersion   `json:"resolved"`
	TagName        string            `json:"tagName"`
	Name           string            `json:"name,omitempty"`
	Summary        string            `json:"summary,omitempty"`
	Description    string            `json:"description,omitempty"`
	ModulePath     string            `json:"modulePath,omitempty"`
	PackageSubpath string            `json:"packageSubpath,omitempty"`
	Deprecated     *cem.Deprecation  `json:"deprecated,omitempty"`
	Sections       []string          `json:"sections"`
	Attributes     []cem.Attribute   `json:"attributes,omitempty"`
	Properties     []cem.Property    `json:"properties,omitempty"`
	Events         []cem.Event       `json:"events,omitempty"`
	Slots          []cem.Slot        `json:"slots,omitempty"`
	CSSParts       []cem.CSSPart     `json:"cssParts,omitempty"`
	CSSProperties  []cem.CSSProperty `json:"cssProperties,omitempty"`
	Examples       []cem.Example     `json:"examples,omitempty"`
	Error          *ToolError        `json:"error,omitempty"`
}

// GetComponentAPI implements rhds_get_component_api for direct service calls.
func (s *Service) GetComponentAPI(ctx context.Context, input GetComponentAPIInput) ComponentAPIOutput {
	resolved, failure := s.bundledResolution(input.Version, "")
	if failure != nil {
		return ComponentAPIOutput{Error: failure}
	}
	return s.componentAPI(resolved, input)
}

func (s *Service) getComponentAPIWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input GetComponentAPIInput,
) (*mcp.CallToolResult, ComponentAPIOutput) {
	resolved, required, failure := s.prepareResolution(
		ctx, newCallEnv(request), input.Version, "", true)
	if required != nil {
		return required, ComponentAPIOutput{}
	}
	if failure != nil {
		return nil, ComponentAPIOutput{Error: failure}
	}
	return nil, s.componentAPI(resolved, input)
}

func (s *Service) componentAPI(resolved resolution, input GetComponentAPIInput) ComponentAPIOutput {
	out := ComponentAPIOutput{Resolved: resolved.Resolved}
	tagName := strings.ToLower(strings.TrimSpace(input.TagName))
	if tagName == "" {
		out.Error = invalidArgument("tagName is required")
		return out
	}

	detail := input.Detail
	if detail == "" {
		detail = "summary"
	}
	if detail != "summary" && detail != "full" {
		out.Error = invalidArgument("detail must be summary or full")
		return out
	}

	sections, failure := normalizeSections(input.Sections)
	if failure != nil {
		out.Error = failure
		return out
	}
	out.Sections = sectionList(sections)

	model, failure := s.loadModel(resolved.Resolved.Elements)
	if failure != nil {
		out.Error = failure
		return out
	}
	component, ok := model.Component(tagName)
	if !ok {
		out.Error = &ToolError{
			Code:    "component_not_found",
			Message: fmt.Sprintf("%q is not a custom element in elements@%s", tagName, resolved.Resolved.Elements),
			Hint:    "call rhds_list_components to list bundled tag names",
		}
		return out
	}

	full := detail == "full"
	out.TagName = component.TagName
	out.Name = component.Name
	out.Summary = component.Summary
	out.ModulePath = component.ModulePath
	out.PackageSubpath = component.PackageSubpath
	out.Deprecated = component.Deprecated
	if full {
		out.Description = component.Description
	}

	if sections[sectionAttributes] {
		out.Attributes = summarizeAttributes(component.Attributes, full)
	}
	if sections[sectionProperties] {
		out.Properties = summarizeProperties(component.Properties, full)
	}
	if sections[sectionEvents] {
		out.Events = summarizeEvents(component.Events, full)
	}
	if sections[sectionSlots] {
		out.Slots = summarizeSlots(component.Slots, full)
	}
	if sections[sectionCSSParts] {
		out.CSSParts = summarizeParts(component.CSSParts, full)
	}
	if sections[sectionCSSProperties] {
		out.CSSProperties = summarizeCSSProperties(component.CSSProperties, full)
	}
	if sections[sectionExamples] {
		out.Examples = summarizeExamples(component.Examples, full)
	}

	budget := defaultResponseBudget
	if full {
		budget = hardResponseBudget
	}
	if s.maxResponseBytes > 0 && s.maxResponseBytes < budget {
		budget = s.maxResponseBytes
	}
	if encoded, err := json.Marshal(out); err == nil && len(encoded) > budget {
		return ComponentAPIOutput{
			Resolved: resolved.Resolved,
			TagName:  component.TagName,
			Sections: sectionList(sections),
			Error: &ToolError{
				Code: "response_too_large",
				Message: fmt.Sprintf(
					"the %s response for %s is %d bytes, over the %d byte cap",
					detail, component.TagName, len(encoded), budget),
				Hint: "call this tool again with a subset of sections and detail=summary; " +
					"there is no silent truncation",
			},
		}
	}
	return out
}

func normalizeSections(requested []string) (map[string]bool, *ToolError) {
	if len(requested) == 0 {
		all := make(map[string]bool, len(componentAPISections))
		for _, section := range componentAPISections {
			all[section] = true
		}
		return all, nil
	}
	selected := make(map[string]bool, len(requested))
	valid := make(map[string]bool, len(componentAPISections))
	for _, section := range componentAPISections {
		valid[section] = true
	}
	for _, section := range requested {
		if !valid[section] {
			return nil, invalidArgument(fmt.Sprintf(
				"unknown section %q; valid sections: %s",
				section, strings.Join(componentAPISections, ", ")))
		}
		selected[section] = true
	}
	return selected, nil
}

func sectionList(selected map[string]bool) []string {
	sections := make([]string, 0, len(selected))
	for _, section := range componentAPISections {
		if selected[section] {
			sections = append(sections, section)
		}
	}
	return sections
}

func summarizeAttributes(attributes []cem.Attribute, full bool) []cem.Attribute {
	if attributes == nil {
		return nil
	}
	result := make([]cem.Attribute, len(attributes))
	copy(result, attributes)
	if full {
		return result
	}
	for i := range result {
		result[i].Summary = ""
		result[i].Description = ""
	}
	return result
}

func summarizeProperties(properties []cem.Property, full bool) []cem.Property {
	if properties == nil {
		return nil
	}
	result := make([]cem.Property, len(properties))
	copy(result, properties)
	if full {
		return result
	}
	for i := range result {
		result[i].Summary = ""
		result[i].Description = ""
	}
	return result
}

func summarizeEvents(events []cem.Event, full bool) []cem.Event {
	if events == nil {
		return nil
	}
	result := make([]cem.Event, len(events))
	copy(result, events)
	if full {
		return result
	}
	for i := range result {
		result[i].Summary = ""
		result[i].Description = ""
	}
	return result
}

func summarizeSlots(slots []cem.Slot, full bool) []cem.Slot {
	if slots == nil {
		return nil
	}
	result := make([]cem.Slot, len(slots))
	copy(result, slots)
	if full {
		return result
	}
	for i := range result {
		result[i].Summary = ""
		result[i].Description = ""
	}
	return result
}

func summarizeParts(parts []cem.CSSPart, full bool) []cem.CSSPart {
	if parts == nil {
		return nil
	}
	result := make([]cem.CSSPart, len(parts))
	copy(result, parts)
	if full {
		return result
	}
	for i := range result {
		result[i].Summary = ""
		result[i].Description = ""
	}
	return result
}

func summarizeCSSProperties(properties []cem.CSSProperty, full bool) []cem.CSSProperty {
	if properties == nil {
		return nil
	}
	result := make([]cem.CSSProperty, len(properties))
	copy(result, properties)
	if full {
		return result
	}
	for i := range result {
		result[i].Summary = ""
		result[i].Description = ""
	}
	return result
}

func summarizeExamples(examples []cem.Example, full bool) []cem.Example {
	if examples == nil {
		return nil
	}
	result := make([]cem.Example, len(examples))
	copy(result, examples)
	if full {
		return result
	}
	for i := range result {
		result[i].URL = ""
		result[i].Source = ""
	}
	return result
}
