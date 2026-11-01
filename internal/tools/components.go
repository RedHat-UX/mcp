// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/cem"
	"github.com/redhat-ux/mcp/internal/resolve"
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
	// Truncated reports that the budget layer shortened at least one field in
	// this row using a fixed per-field cap. The corresponding warning names
	// the field, its true original rune length, and the resulting length.
	Truncated bool `json:"truncated,omitempty"`
}

// ListComponentsOutput is the output for rhds_list_components.
type ListComponentsOutput struct {
	Resolved   ResolvedVersion    `json:"resolved"`
	Components []ComponentSummary `json:"components"`
	Total      int                `json:"total"`
	NextCursor string             `json:"nextCursor,omitempty"`
	Truncated  bool               `json:"truncated"`
	Error      *ToolError         `json:"error,omitempty"`
}

type cursorPayload struct {
	Version           string `json:"v"`
	Offset            int    `json:"o"`
	IncludeDeprecated bool   `json:"d,omitempty"`
	// Fingerprint binds a cursor to the query and token filters that produced
	// it, so an offset cannot be replayed against a different result set.
	Fingerprint string `json:"f,omitempty"`
}

func encodeCursorPayload(payload cursorPayload) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		// json.Marshal cannot fail for this struct.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func encodeCursor(version string, offset int, includeDeprecated bool) string {
	return encodeCursorPayload(cursorPayload{
		Version:           version,
		Offset:            offset,
		IncludeDeprecated: includeDeprecated,
	})
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
	resolved, failure := s.resolutionForListInput(input)
	if failure != nil {
		out := ListComponentsOutput{Components: []ComponentSummary{}, Error: failure}
		fitListComponents(&out, 0, input.IncludeDeprecated, s.responseBudget(false))
		return out
	}
	return s.listComponents(resolved, input)
}

// listComponentsWithEnv implements rhds_list_components for an MCP call.
// A cursor is decoded before resolution so page two uses the cursor-bound
// version and never re-runs the cascade or prompts again.
func (s *Service) listComponentsWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input ListComponentsInput,
) (*mcp.CallToolResult, ListComponentsOutput) {
	fail := func(failure *ToolError) (*mcp.CallToolResult, ListComponentsOutput) {
		out := ListComponentsOutput{Components: []ComponentSummary{}, Error: failure}
		fitListComponents(&out, 0, input.IncludeDeprecated, s.responseBudget(false))
		return nil, out
	}
	if input.Cursor != "" {
		payload, cursorErr := decodeCursor(input.Cursor)
		if cursorErr != nil {
			return fail(cursorErr)
		}
		if payload.IncludeDeprecated != input.IncludeDeprecated {
			return fail(cursorOptionsMismatch())
		}
		if input.Version != "" {
			requested, failure := s.bundledResolution(input.Version, "")
			if failure != nil {
				return fail(failure)
			}
			if requested.Resolved.Elements != payload.Version {
				return fail(cursorVersionMismatch(payload.Version, input.Version))
			}
		}
		resolved, failure := s.resolutionForBundledVersion(payload.Version)
		if failure != nil {
			return fail(failure)
		}
		return nil, s.listComponents(resolved, input)
	}

	resolved, required, failure := s.prepareResolution(
		ctx, newCallEnv(request), input.Version, "", true)
	if required != nil {
		return required, ListComponentsOutput{}
	}
	if failure != nil {
		return fail(failure)
	}
	return nil, s.listComponents(resolved, input)
}

// resolutionForListInput uses a cursor-bound version when present. Direct
// service calls have no protocol session, so they do not run the workspace
// cascade.
func (s *Service) resolutionForListInput(input ListComponentsInput) (resolution, *ToolError) {
	if input.Cursor != "" && input.Version == "" {
		payload, cursorErr := decodeCursor(input.Cursor)
		if cursorErr != nil {
			return resolution{}, cursorErr
		}
		return s.resolutionForBundledVersion(payload.Version)
	}
	return s.bundledResolution(input.Version, "")
}

func (s *Service) resolutionForBundledVersion(version string) (resolution, *ToolError) {
	result := resolve.Result{
		Elements: version,
		Source:   "cursor",
		Candidates: []resolve.Candidate{{
			Version:    version,
			Source:     "cursor",
			Precedence: resolve.PrecedenceBundled,
			Level:      resolve.LevelBundled,
		}},
	}
	return s.resolutionFrom(result, "", true)
}

func cursorOptionsMismatch() *ToolError {
	return &ToolError{
		Code:    "cursor_options_mismatch",
		Message: "includeDeprecated changed between pages",
		Hint:    "use the same includeDeprecated value for every page of one listing",
	}
}

func cursorVersionMismatch(cursorVersion, requestedVersion string) *ToolError {
	return &ToolError{
		Code: "cursor_version_mismatch",
		Message: fmt.Sprintf(
			"cursor resolves elements@%s but the call requested elements@%s",
			truncateResponseText(cursorVersion, maxCandidateRunes),
			truncateResponseText(requestedVersion, maxCandidateRunes)),
		Hint: "restart pagination without a cursor or use the same version",
	}
}

func (s *Service) listComponents(resolved resolution, input ListComponentsInput) ListComponentsOutput {
	return s.listComponentsWithBudget(resolved, input, s.responseBudget(false))
}

func (s *Service) listComponentsWithBudget(
	resolved resolution,
	input ListComponentsInput,
	budget int,
) (out ListComponentsOutput) {
	startOffset := 0
	defer func() {
		fitListComponents(&out, startOffset, input.IncludeDeprecated, budget)
	}()
	out = ListComponentsOutput{
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
	docsTags := s.docsTagsFor(resolved.Resolved.Docs)

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
			out.Error = cursorVersionMismatch(payload.Version, version)
			return out
		}
		if payload.IncludeDeprecated != input.IncludeDeprecated {
			out.Error = cursorOptionsMismatch()
			return out
		}
		if payload.Offset > len(entries) {
			out.Error = invalidCursor("cursor offset is past the end of the result set")
			return out
		}
		offset = payload.Offset
	}
	startOffset = offset

	end := min(offset+pageSize, len(entries))
	for _, entry := range entries[offset:end] {
		out.Components = append(out.Components, ComponentSummary{
			TagName:        entry.tagName,
			Summary:        entry.summary,
			ModulePath:     entry.modulePath,
			PackageSubpath: entry.packageSubpath,
			Deprecated:     entry.deprecated,
			DocsAvailable:  docsTags[entry.tagName],
		})
	}
	out.Total = len(entries)
	if end < len(entries) {
		out.NextCursor = encodeCursor(version, end, input.IncludeDeprecated)
	}

	return out
}

// docsTagsFor returns the element tags that have a page in a docs snapshot.
// Real docs paths are elements/<tag>/docs/*.md.
func (s *Service) docsTagsFor(version string) map[string]bool {
	if version == "" || !s.store.Has(packageDocs, version, kindDocs) {
		return nil
	}
	if cached, ok := s.docsTags.Load(version); ok {
		return cached.(map[string]bool)
	}
	records, failure := s.docsRecords(version)
	if failure != nil {
		return nil
	}
	tags := make(map[string]bool)
	for _, record := range records {
		const prefix = "elements/"
		if !strings.HasPrefix(record.Path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(record.Path, prefix)
		tag, _, found := strings.Cut(rest, "/")
		if found && tag != "" {
			tags[tag] = true
		}
	}
	actual, _ := s.docsTags.LoadOrStore(version, tags)
	return actual.(map[string]bool)
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
