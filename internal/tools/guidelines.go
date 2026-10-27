// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/markdown"
)

const (
	defaultGuidelineLimit = 20
	maxGuidelineLimit     = 100
)

// DesignGuidelinesInput is the input for rhds_get_design_guidelines.
type DesignGuidelinesInput struct {
	Topic   string `json:"topic,omitempty" jsonschema:"Topic text to match against docs titles, headings, and prose"`
	Element string `json:"element,omitempty" jsonschema:"Element tag name or short name, for example rh-button or button"`
	Version string `json:"version,omitempty" jsonschema:"RHDS elements version or docs snapshot tag; defaults to the workspace resolution, then the newest bundled docs snapshot"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum heading chunks; default 20, maximum 100"`
}

// GuidelineChunk is one heading-delimited documentation chunk.
type GuidelineChunk struct {
	Title      string `json:"title,omitempty"`
	Heading    string `json:"heading,omitempty"`
	Level      int    `json:"level,omitempty"`
	Text       string `json:"text"`
	Path       string `json:"path"`
	Source     string `json:"source"`
	ReleaseTag string `json:"releaseTag"`
}

// DesignGuidelinesOutput is the output for rhds_get_design_guidelines.
type DesignGuidelinesOutput struct {
	Resolved  ResolvedVersion  `json:"resolved"`
	Topic     string           `json:"topic,omitempty"`
	Element   string           `json:"element,omitempty"`
	Chunks    []GuidelineChunk `json:"chunks"`
	Total     int              `json:"total"`
	Truncated bool             `json:"truncated"`
	Error     *ToolError       `json:"error,omitempty"`
}

// GetDesignGuidelines implements rhds_get_design_guidelines for direct
// service calls.
func (s *Service) GetDesignGuidelines(_ context.Context, input DesignGuidelinesInput) DesignGuidelinesOutput {
	resolved, failure := s.guidelinesResolution(input.Version)
	if failure != nil {
		return DesignGuidelinesOutput{Chunks: []GuidelineChunk{}, Error: failure}
	}
	return s.designGuidelines(resolved, input)
}

func (s *Service) guidelinesWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input DesignGuidelinesInput,
) (*mcp.CallToolResult, DesignGuidelinesOutput) {
	if input.Version != "" && s.store != nil && s.store.Has(packageDocs, input.Version, kindDocs) {
		resolved, failure := s.guidelinesResolution(input.Version)
		if failure != nil {
			return nil, DesignGuidelinesOutput{Chunks: []GuidelineChunk{}, Error: failure}
		}
		return nil, s.designGuidelines(resolved, input)
	}
	resolved, required, failure := s.prepareResolution(
		ctx, newCallEnv(request), input.Version, "", true)
	if required != nil {
		return required, DesignGuidelinesOutput{}
	}
	if failure != nil {
		return nil, DesignGuidelinesOutput{Chunks: []GuidelineChunk{}, Error: failure}
	}
	return nil, s.designGuidelines(resolved, input)
}

func (s *Service) guidelinesResolution(explicit string) (resolution, *ToolError) {
	if failure := s.dataFailure(); failure != nil {
		return resolution{}, failure
	}
	if explicit != "" && s.store.Has(packageDocs, explicit, kindDocs) {
		resolved := ResolvedVersion{Docs: explicit, Source: "bundled"}
		if elements, ok := s.elementsVersionForDocsTag(explicit); ok {
			resolved.Elements = elements
			if tokens, ok := s.store.Index().TokensForElements(elements); ok {
				resolved.Tokens = tokens
			}
		}
		return resolution{Resolved: resolved}, nil
	}
	return s.bundledResolution(explicit, "")
}

func (s *Service) designGuidelines(resolved resolution, input DesignGuidelinesInput) DesignGuidelinesOutput {
	out := DesignGuidelinesOutput{
		Resolved: resolved.Resolved,
		Topic:    strings.TrimSpace(input.Topic),
		Element:  strings.TrimSpace(input.Element),
		Chunks:   []GuidelineChunk{},
	}
	if input.Limit < 0 {
		out.Error = invalidArgument("limit must be zero or positive")
		return out
	}
	docsVersion := resolved.Resolved.Docs
	if docsVersion == "" {
		out.Error = &ToolError{
			Code:    "artifact_not_found",
			Message: "no bundled docs snapshots",
			Hint:    "build with generated data: make data build",
		}
		return out
	}
	out.Resolved.Docs = docsVersion

	records, failure := s.docsRecords(docsVersion)
	if failure != nil {
		out.Error = failure
		return out
	}

	tag := normalizeElementTag(input.Element)
	topic := strings.ToLower(out.Topic)
	var chunks []GuidelineChunk
	elementDocs := 0
	for _, record := range records {
		if tag != "" && !elementDocPath(record.Path, tag) {
			continue
		}
		if tag != "" {
			elementDocs++
		}
		pathMatches := topic == "" ||
			strings.Contains(strings.ToLower(record.Path), topic) ||
			strings.Contains(strings.ToLower(record.Title), topic)
		if topic != "" && !pathMatches {
			// The topic may match a heading or body. Read the page only when
			// the cheap path/title match failed.
			content, err := s.store.Read(packageDocs, docsVersion, record.Kind)
			if err != nil {
				continue
			}
			if !pageMatchesTopic(content, topic) {
				continue
			}
		}
		content, err := s.store.Read(packageDocs, docsVersion, record.Kind)
		if err != nil {
			continue
		}
		for _, chunk := range markdown.Chunks(content) {
			text := stripSourceComment(chunk.Body)
			if topic != "" && !pathMatches && !chunkMatchesTopic(chunk.Heading, text, topic) {
				continue
			}
			if chunk.Heading == "" && text == "" {
				continue
			}
			heading := chunk.Heading
			if heading == "" {
				heading = record.Title
			}
			chunks = append(chunks, GuidelineChunk{
				Title:      record.Title,
				Heading:    heading,
				Level:      chunk.Level,
				Text:       text,
				Path:       record.Path,
				Source:     record.Source,
				ReleaseTag: docsVersion,
			})
		}
	}
	if tag != "" && elementDocs == 0 {
		out.Error = &ToolError{
			Code: "docs_not_found",
			Message: fmt.Sprintf(
				"no documentation for %s in the %s snapshot", tag, docsVersion),
			Hint: "check the element name or try a different docs snapshot",
		}
		return out
	}
	if tag != "" && len(chunks) == 0 {
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("no chunks matched topic %q for %s; the element has documentation", out.Topic, tag))
		return out
	}

	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].Path != chunks[j].Path {
			return chunks[i].Path < chunks[j].Path
		}
		return false
	})
	out.Total = len(chunks)

	limit := input.Limit
	if limit == 0 {
		limit = defaultGuidelineLimit
	}
	if limit > maxGuidelineLimit {
		limit = maxGuidelineLimit
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("limit was capped at %d", maxGuidelineLimit))
	}
	if len(chunks) > limit {
		chunks = chunks[:limit]
		out.Truncated = true
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("results were limited to %d chunks; narrow the topic or element, or raise the limit", limit))
	}
	out.Chunks = chunks

	budget := s.responseBudget(false)
	if estimatedResponseBytes(out) > budget {
		out.Truncated = true
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("the response budget of %d bytes was reached; narrow the topic or element", budget))
		for len(out.Chunks) > 0 && estimatedResponseBytes(out) > budget {
			out.Chunks = out.Chunks[:len(out.Chunks)-1]
		}
		if len(out.Chunks) == 0 && len(chunks) > 0 {
			out.Error = &ToolError{
				Code: "response_too_large",
				Message: fmt.Sprintf(
					"no guideline chunk fits the %d byte response budget", budget),
				Hint: "narrow the topic or element",
			}
		}
	}
	return out
}

func normalizeElementTag(name string) string {
	tag := strings.ToLower(strings.TrimSpace(name))
	if tag == "" {
		return ""
	}
	if !strings.HasPrefix(tag, "rh-") {
		tag = "rh-" + tag
	}
	return tag
}

func elementDocPath(path, tag string) bool {
	return strings.HasPrefix(path, "elements/"+tag+"/docs/")
}

func pageMatchesTopic(content []byte, topic string) bool {
	for _, chunk := range markdown.Chunks(content) {
		if chunkMatchesTopic(chunk.Heading, chunk.Body, topic) {
			return true
		}
	}
	return false
}

func chunkMatchesTopic(heading, body, topic string) bool {
	return strings.Contains(strings.ToLower(heading), topic) ||
		strings.Contains(strings.ToLower(body), topic)
}

// stripSourceComment removes the generated "<!-- source: ... -->" header from
// a docs page. The source is returned as a structured field instead.
func stripSourceComment(text string) string {
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "<!-- source:") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
