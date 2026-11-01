// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/html"
)

const (
	scopeComponents = "components"
	scopeTokens     = "tokens"
	scopeDocs       = "docs"
	scopeExamples   = "examples"
	scopeAll        = "all"
)

const (
	defaultSearchLimit = 10
	maxSearchLimit     = 50
	maxExcerptRunes    = 180
)

// SearchInput is the input for rhds_search.
type SearchInput struct {
	Query   string `json:"query" jsonschema:"Case-insensitive text to find in components, tokens, docs, or examples"`
	Scope   string `json:"scope,omitempty" jsonschema:"Search scope: components, tokens, docs, examples, or all (default)"`
	Version string `json:"version,omitempty" jsonschema:"RHDS elements version; defaults to the workspace resolution, then the latest bundled version"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum hits; default 10, maximum 50"`
}

// SearchHit is one ranked search result.
type SearchHit struct {
	Type       string `json:"type"`
	Title      string `json:"title"`
	Path       string `json:"path,omitempty"`
	Version    string `json:"version,omitempty"`
	Excerpt    string `json:"excerpt,omitempty"`
	Score      int    `json:"score"`
	Deprecated bool   `json:"deprecated,omitempty"`
	// Truncated reports that the budget layer shortened at least one field in
	// this hit. The warning names each shortened field and its original rune
	// length.
	Truncated bool `json:"truncated,omitempty"`
}

// SearchOutput is the output for rhds_search.
type SearchOutput struct {
	Resolved  ResolvedVersion `json:"resolved"`
	Scope     string          `json:"scope"`
	Hits      []SearchHit     `json:"hits"`
	Total     int             `json:"total"`
	Truncated bool            `json:"truncated"`
	Error     *ToolError      `json:"error,omitempty"`
}

type docRecord struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Source   string `json:"source"`
	Artifact string `json:"artifact"`
	Kind     string `json:"kind"`
}

type patternRecord struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	HTML   string `json:"html"`
}

// Search implements rhds_search for direct service calls.
func (s *Service) Search(ctx context.Context, input SearchInput) SearchOutput {
	resolved, failure := s.bundledResolution(input.Version, "")
	if failure != nil {
		out := SearchOutput{Hits: []SearchHit{}, Error: failure}
		fitSearch(&out, s.responseBudget(false))
		return out
	}
	return s.search(resolved, input)
}

func (s *Service) searchWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input SearchInput,
) (*mcp.CallToolResult, SearchOutput) {
	// Validate before roots negotiation, workspace scans, or elicitation. An
	// invalid query must never prompt the user or read the workspace.
	if _, failure := validateSearchInput(input); failure != nil {
		out := SearchOutput{Hits: []SearchHit{}, Error: failure}
		fitSearch(&out, s.responseBudget(false))
		return nil, out
	}
	resolved, required, failure := s.prepareResolution(
		ctx, newCallEnv(request), input.Version, "", true)
	if required != nil {
		return required, SearchOutput{}
	}
	if failure != nil {
		out := SearchOutput{Hits: []SearchHit{}, Error: failure}
		fitSearch(&out, s.responseBudget(false))
		return nil, out
	}
	return nil, s.search(resolved, input)
}

// searchOptions is validated search input. Validation is separate so
// searchWithEnv can reject bad input before any resolution work.
type searchOptions struct {
	query string
	scope string
	limit int
}

func validateSearchInput(input SearchInput) (searchOptions, *ToolError) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return searchOptions{}, invalidArgument("query is required")
	}
	scope := input.Scope
	if scope == "" {
		scope = scopeAll
	}
	switch scope {
	case scopeComponents, scopeTokens, scopeDocs, scopeExamples, scopeAll:
	default:
		return searchOptions{}, invalidArgument("scope must be components, tokens, docs, examples, or all")
	}
	if input.Limit < 0 {
		return searchOptions{}, invalidArgument("limit must be zero or positive")
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	return searchOptions{query: query, scope: scope, limit: limit}, nil
}

func (s *Service) search(resolved resolution, input SearchInput) (out SearchOutput) {
	defer func() { fitSearch(&out, s.responseBudget(false)) }()
	out = SearchOutput{
		Resolved: resolved.Resolved,
		Hits:     []SearchHit{},
	}
	options, failure := validateSearchInput(input)
	if failure != nil {
		out.Error = failure
		return out
	}
	query := options.query
	scope := options.scope
	out.Scope = scope
	limit := options.limit
	if input.Limit > maxSearchLimit {
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("limit was capped at %d", maxSearchLimit))
	}

	needle := strings.ToLower(query)
	hits := make([]SearchHit, 0)

	if scope == scopeComponents || scope == scopeAll {
		components, failure := s.searchComponents(resolved.Resolved.Elements, needle)
		if failure != nil {
			out.Error = failure
			return out
		}
		hits = append(hits, components...)
	}
	if scope == scopeTokens || scope == scopeAll {
		hits = append(hits, s.searchTokens(resolved.Resolved.Tokens, needle, &out.Resolved.Warnings)...)
	}
	if scope == scopeDocs || scope == scopeAll {
		hits = append(hits, s.searchDocs(resolved.Resolved.Docs, needle, &out.Resolved.Warnings)...)
	}
	if scope == scopeExamples || scope == scopeAll {
		hits = append(hits, s.searchExamples(
			resolved.Resolved.Elements, resolved.Resolved.Docs, needle, &out.Resolved.Warnings)...)
	}

	hits = dedupeHits(hits)
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Type != hits[j].Type {
			return hits[i].Type < hits[j].Type
		}
		if hits[i].Title != hits[j].Title {
			return hits[i].Title < hits[j].Title
		}
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Version < hits[j].Version
	})
	out.Total = len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out.Hits = hits

	return out
}

func (s *Service) searchComponents(version, needle string) ([]SearchHit, *ToolError) {
	model, failure := s.loadModel(version)
	if failure != nil {
		return nil, failure
	}
	hits := make([]SearchHit, 0)
	for _, component := range model.Components {
		excerpt := component.Summary
		if excerpt == "" {
			excerpt = component.Description
		}
		hit := SearchHit{
			Type:       "component",
			Title:      component.TagName,
			Path:       component.ModulePath,
			Version:    version,
			Excerpt:    excerpt,
			Deprecated: component.Deprecated != nil,
		}
		if score := scoreHit(hit, needle); score > 0 {
			hit.Score = score
			hit.Excerpt = excerptText(excerpt, needle, maxExcerptRunes)
			hits = append(hits, hit)
		}
	}
	return hits, nil
}

func (s *Service) searchTokens(version, needle string, warnings *[]string) []SearchHit {
	if version == "" {
		return nil
	}
	tokens, _, failure := s.loadTokens(version)
	if failure != nil {
		*warnings = append(*warnings, "token search skipped: "+failure.Message)
		return nil
	}
	hits := make([]SearchHit, 0)
	for _, tok := range tokens {
		excerpt := tok.Description
		if excerpt == "" {
			excerpt = tok.DisplayValue()
		}
		hit := SearchHit{
			Type:       "token",
			Title:      tok.Name,
			Path:       "tokens@" + version,
			Version:    version,
			Excerpt:    excerpt,
			Deprecated: tok.Deprecated,
		}
		if score := scoreHit(hit, needle); score > 0 {
			hit.Score = score
			hit.Excerpt = excerptText(excerpt, needle, maxExcerptRunes)
			hits = append(hits, hit)
		}
	}
	return hits
}

func (s *Service) searchDocs(version, needle string, warnings *[]string) []SearchHit {
	if version == "" {
		return nil
	}
	records, failure := s.docsRecords(version)
	if failure != nil {
		*warnings = append(*warnings, "docs search skipped: "+failure.Message)
		return nil
	}
	hits := make([]SearchHit, 0)
	unavailable := 0
	var readFailure string
	for _, record := range records {
		excerpt := ""
		if content, err := s.store.Read(packageDocs, version, record.Kind); err == nil {
			excerpt = excerptText(string(content), needle, maxExcerptRunes)
		} else {
			unavailable++
			if readFailure == "" {
				readFailure = err.Error()
			}
		}
		hit := SearchHit{
			Type:    "doc",
			Title:   record.Title,
			Path:    record.Path,
			Version: version,
			Excerpt: excerpt,
		}
		if score := scoreHit(hit, needle); score > 0 {
			hit.Score = score
			hits = append(hits, hit)
		}
	}
	if unavailable > 0 {
		*warnings = append(*warnings, fmt.Sprintf(
			"docs content unavailable for %d pages; matched on title and path only: %s",
			unavailable, readFailure))
	}
	return hits
}

func (s *Service) searchExamples(elementsVersion, docsVersion, needle string, warnings *[]string) []SearchHit {
	hits := make([]SearchHit, 0)

	if docsVersion != "" {
		records, failure := s.patternRecords(docsVersion)
		if failure != nil {
			*warnings = append(*warnings, "pattern search skipped: "+failure.Message)
		}
		for _, record := range records {
			hit := SearchHit{
				Type:    "example",
				Title:   record.Path,
				Path:    record.Path,
				Version: docsVersion,
				Excerpt: htmlText(record.HTML),
			}
			if score := scoreHit(hit, needle); score > 0 {
				hit.Score = score
				hit.Excerpt = excerptText(hit.Excerpt, needle, maxExcerptRunes)
				hits = append(hits, hit)
			}
		}
	}

	model, failure := s.loadModel(elementsVersion)
	if failure != nil {
		*warnings = append(*warnings, "component example search skipped: "+failure.Message)
		return hits
	}
	for _, component := range model.Components {
		for _, example := range component.Examples {
			title := example.Description
			if title == "" {
				title = component.TagName + " example"
			}
			hit := SearchHit{
				Type:    "example",
				Title:   title,
				Path:    example.URL,
				Version: elementsVersion,
				Excerpt: example.Description,
			}
			if hit.Path == "" {
				hit.Path = example.Source
			}
			if score := scoreHit(hit, needle); score > 0 {
				hit.Score = score
				hit.Excerpt = excerptText(hit.Excerpt, needle, maxExcerptRunes)
				hits = append(hits, hit)
			}
		}
	}
	return hits
}

func (s *Service) docsRecords(version string) ([]docRecord, *ToolError) {
	payload, err := s.store.Read(packageDocs, version, kindDocs)
	if err != nil {
		return nil, toolErrorFrom(err)
	}
	var records []docRecord
	if err := json.Unmarshal(payload, &records); err != nil {
		return nil, &ToolError{
			Code:    "invalid_docs_index",
			Message: fmt.Sprintf("could not parse the docs index for %s", version),
			Hint:    err.Error(),
		}
	}
	return records, nil
}

func (s *Service) patternRecords(version string) ([]patternRecord, *ToolError) {
	payload, err := s.store.Read(packageDocs, version, kindPatterns)
	if err != nil {
		return nil, toolErrorFrom(err)
	}
	var records []patternRecord
	if err := json.Unmarshal(payload, &records); err != nil {
		return nil, &ToolError{
			Code:    "invalid_patterns",
			Message: fmt.Sprintf("could not parse patterns for %s", version),
			Hint:    err.Error(),
		}
	}
	return records, nil
}

func scoreHit(hit SearchHit, needle string) int {
	title := strings.ToLower(hit.Title)
	path := strings.ToLower(hit.Path)
	excerpt := strings.ToLower(hit.Excerpt)
	switch {
	case title == needle:
		return 100
	case strings.HasPrefix(title, needle):
		return 90
	case strings.Contains(title, needle):
		return 80
	case strings.Contains(path, needle):
		return 60
	case strings.Contains(excerpt, needle):
		return 50
	default:
		return 0
	}
}

func dedupeHits(hits []SearchHit) []SearchHit {
	seen := make(map[string]bool, len(hits))
	result := hits[:0]
	for _, hit := range hits {
		key := hit.Type + "\x00" + hit.Title + "\x00" + hit.Path + "\x00" + hit.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, hit)
	}
	return result
}

// htmlText extracts visible text with a real HTML parser and collapses
// whitespace. It is used only for excerpts, never for validation.
func htmlText(markup string) string {
	document, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		return collapseWhitespace(markup)
	}
	var builder strings.Builder
	var collect func(*html.Node)
	collect = func(node *html.Node) {
		if node.Type == html.TextNode {
			builder.WriteString(node.Data)
			builder.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(document)
	return collapseWhitespace(builder.String())
}

func excerptText(text, needle string, maxRunes int) string {
	text = collapseWhitespace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	center := findRuneIndex(runes, []rune(strings.ToLower(needle)))
	if center < 0 {
		return string(runes[:maxRunes]) + "..."
	}
	start := max(0, center-maxRunes/3)
	end := min(len(runes), start+maxRunes)
	if end-start < maxRunes {
		start = max(0, end-maxRunes)
	}
	excerpt := string(runes[start:end])
	if start > 0 {
		excerpt = "..." + excerpt
	}
	if end < len(runes) {
		excerpt += "..."
	}
	return excerpt
}

// findRuneIndex performs a case-insensitive rune search without converting the
// whole document to lower case. Lower-casing can change byte and rune lengths
// (for example, U+0130), so a byte index from strings.Index is not safe.
func findRuneIndex(text, query []rune) int {
	if len(query) == 0 {
		return -1
	}
	for i := 0; i+len(query) <= len(text); i++ {
		matched := true
		for j := range query {
			if unicode.ToLower(text[i+j]) != query[j] {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

func collapseWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
