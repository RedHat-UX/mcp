// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/cem"
)

const (
	defaultPageSize = 25
	maxPageSize     = 100
)

// ListComponentsInput is the input for rhds_list_components.
type ListComponentsInput struct {
	Version           string `json:"version,omitempty" jsonschema:"RHDS elements version; defaults to the workspace resolution, then the latest bundled version"`
	IncludeDeprecated bool   `json:"includeDeprecated,omitempty" jsonschema:"Include deprecated custom elements"`
	Cursor            string `json:"cursor,omitempty" jsonschema:"Opaque cursor from a previous rhds_list_components call"`
	PageSize          int    `json:"pageSize,omitempty" jsonschema:"Maximum components per page; default 25, maximum 100"`
}

// ComponentSummary is one row in rhds_list_components output.
type ComponentSummary struct {
	TagName        string `json:"tagName"`
	Summary        string `json:"summary,omitempty"`
	ModulePath     string `json:"modulePath"`
	PackageSubpath string `json:"packageSubpath,omitempty"`
	Deprecated     bool   `json:"deprecated"`
	DocsAvailable  bool   `json:"docsAvailable"`
}

// ListComponentsOutput is the output for rhds_list_components.
type ListComponentsOutput struct {
	Resolved   ResolvedVersion    `json:"resolved"`
	Components []ComponentSummary `json:"components"`
	Total      int                `json:"total"`
	NextCursor string             `json:"nextCursor,omitempty"`
	Error      *ToolError         `json:"error,omitempty"`
}

type cursorPayload struct {
	Version           string `json:"v"`
	Offset            int    `json:"o"`
	IncludeDeprecated bool   `json:"d,omitempty"`
}

func encodeCursor(version string, offset int, includeDeprecated bool) string {
	payload, err := json.Marshal(cursorPayload{
		Version:           version,
		Offset:            offset,
		IncludeDeprecated: includeDeprecated,
	})
	if err != nil {
		// json.Marshal cannot fail for this struct.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func invalidCursor(message string) *ToolError {
	return &ToolError{Code: "invalid_cursor", Message: message}
}

func decodeCursor(cursor string) (cursorPayload, *ToolError) {
	var payload cursorPayload
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return payload, invalidCursor("cursor is not valid base64")
	}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return payload, invalidCursor("cursor is not a valid pagination token")
	}
	if payload.Version == "" || payload.Offset < 0 {
		return payload, invalidCursor("cursor is missing a version or offset")
	}
	return payload, nil
}

type componentEntry struct {
	tagName        string
	summary        string
	modulePath     string
	packageSubpath string
	deprecated     bool
}

// ListComponents implements rhds_list_components for direct service calls.
func (s *Service) ListComponents(_ context.Context, input ListComponentsInput) ListComponentsOutput {
	resolved, failure := s.bundledResolution(input.Version, "")
	if failure != nil {
		return ListComponentsOutput{Components: []ComponentSummary{}, Error: failure}
	}
	return s.listComponents(resolved, input)
}

// listComponentsWithEnv implements rhds_list_components for an MCP call,
// including workspace resolution and elicitation.
func (s *Service) listComponentsWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input ListComponentsInput,
) (*mcp.CallToolResult, ListComponentsOutput) {
	resolved, required, failure := s.prepareResolution(
		ctx, newCallEnv(request), input.Version, "", true)
	if required != nil {
		return required, ListComponentsOutput{}
	}
	if failure != nil {
		return nil, ListComponentsOutput{Components: []ComponentSummary{}, Error: failure}
	}
	return nil, s.listComponents(resolved, input)
}

func (s *Service) listComponents(resolved resolution, input ListComponentsInput) ListComponentsOutput {
	out := ListComponentsOutput{
		Resolved:   resolved.Resolved,
		Components: []ComponentSummary{},
	}
	if input.PageSize < 0 {
		out.Error = invalidArgument("pageSize must be zero or positive")
		return out
	}

	version := resolved.Resolved.Elements
	model, failure := s.loadModel(version)
	if failure != nil {
		out.Error = failure
		return out
	}

	entries := collectComponents(model, input.IncludeDeprecated)
	docsAvailable := resolved.Resolved.Docs != "" &&
		s.store.Has(packageDocs, resolved.Resolved.Docs, kindDocs)

	pageSize := input.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("pageSize was capped at %d", maxPageSize))
	}

	offset := 0
	if input.Cursor != "" {
		payload, cursorErr := decodeCursor(input.Cursor)
		if cursorErr != nil {
			out.Error = cursorErr
			return out
		}
		if payload.Version != version {
			out.Error = &ToolError{
				Code: "cursor_version_mismatch",
				Message: fmt.Sprintf(
					"cursor resolves elements@%s but this call resolves elements@%s",
					payload.Version, version),
				Hint: "restart pagination without a cursor or use the same version",
			}
			return out
		}
		if payload.IncludeDeprecated != input.IncludeDeprecated {
			out.Error = &ToolError{
				Code: "cursor_filter_mismatch",
				Message: fmt.Sprintf(
					"cursor was created with includeDeprecated=%t but this call uses includeDeprecated=%t",
					payload.IncludeDeprecated, input.IncludeDeprecated),
				Hint: "restart pagination without a cursor or keep the same filter",
			}
			return out
		}
		if payload.Offset > len(entries) {
			out.Error = invalidCursor("cursor offset is past the end of the result set")
			return out
		}
		offset = payload.Offset
	}

	end := min(offset+pageSize, len(entries))
	for _, entry := range entries[offset:end] {
		out.Components = append(out.Components, ComponentSummary{
			TagName:        entry.tagName,
			Summary:        entry.summary,
			ModulePath:     entry.modulePath,
			PackageSubpath: entry.packageSubpath,
			Deprecated:     entry.deprecated,
			DocsAvailable:  docsAvailable,
		})
	}
	out.Total = len(entries)
	if end < len(entries) {
		out.NextCursor = encodeCursor(version, end, input.IncludeDeprecated)
	}
	return out
}

func collectComponents(model *cem.Model, includeDeprecated bool) []componentEntry {
	if model == nil {
		return nil
	}
	entries := make([]componentEntry, 0)
	for _, component := range model.Components {
		isDeprecated := component.Deprecated != nil
		if isDeprecated && !includeDeprecated {
			continue
		}
		entries = append(entries, componentEntry{
			tagName:        component.TagName,
			summary:        component.Summary,
			modulePath:     component.ModulePath,
			packageSubpath: component.PackageSubpath,
			deprecated:     isDeprecated,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].tagName < entries[j].tagName
	})
	return entries
}
