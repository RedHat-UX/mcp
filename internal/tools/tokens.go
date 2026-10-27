// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"bennypowers.dev/asimonim/token"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxTokenResults  = 100
	defaultTokenPage = 100
)

// QueryTokensInput is the input for rhds_query_tokens.
type QueryTokensInput struct {
	Query    string `json:"query,omitempty" jsonschema:"Case-insensitive substring to search across token names and descriptions"`
	Token    string `json:"token,omitempty" jsonschema:"Exact token name, dot path, or CSS custom property"`
	Version  string `json:"version,omitempty" jsonschema:"RHDS tokens version; defaults to the workspace resolution, then the latest bundled version"`
	Theme    string `json:"theme,omitempty" jsonschema:"Theme to resolve: light, dark, or auto"`
	Format   string `json:"format,omitempty" jsonschema:"Value format: value, css, or chain"`
	Detail   string `json:"detail,omitempty" jsonschema:"Detail level: summary (default, 8k budget) or full (32k budget)"`
	PageSize int    `json:"pageSize,omitempty" jsonschema:"Maximum tokens per page; default 100, maximum 100"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"Opaque cursor from a previous rhds_query_tokens call"`
}

// TokenResult is one token in rhds_query_tokens output.
type TokenResult struct {
	Name        string   `json:"name"`
	CSSVariable string   `json:"cssVariable"`
	Type        string   `json:"type,omitempty"`
	Value       string   `json:"value"`
	RawValue    string   `json:"rawValue,omitempty"`
	AliasChain  []string `json:"aliasChain,omitempty"`
	Theme       string   `json:"theme,omitempty"`
	ThemeValue  string   `json:"themeValue,omitempty"`
	Source      string   `json:"source"`
	Description string   `json:"description,omitempty"`
	Deprecated  bool     `json:"deprecated"`
	Deprecation string   `json:"deprecationMessage,omitempty"`
	// Truncated reports that at least one field or the alias chain was
	// shortened by a fixed per-field cap. The warning list names the field,
	// the cap, and the longest original value.
	Truncated bool `json:"truncated,omitempty"`
}

// QueryTokensOutput is the output for rhds_query_tokens.
type QueryTokensOutput struct {
	Resolved   ResolvedVersion `json:"resolved"`
	Tokens     []TokenResult   `json:"tokens"`
	Total      int             `json:"total"`
	Detail     string          `json:"detail"`
	NextCursor string          `json:"nextCursor,omitempty"`
	Truncated  bool            `json:"truncated"`
	Error      *ToolError      `json:"error,omitempty"`
}

// Source values for token results. The theme map is extracted from the
// generated css/default-theme.css at data generation time, so tokens that only
// exist there must not claim to come from the JSON document.
const (
	tokenSourceJSON = "json"
	tokenSourceCSS  = "css/default-theme.css"
)

// QueryTokens implements rhds_query_tokens.
func (s *Service) QueryTokens(_ context.Context, input QueryTokensInput) QueryTokensOutput {
	if failure := s.dataFailure(); failure != nil {
		return QueryTokensOutput{Tokens: []TokenResult{}, Error: failure}
	}
	version, fromElements, failure := s.resolveTokensVersion(input.Version)
	if failure != nil {
		return QueryTokensOutput{Tokens: []TokenResult{}, Error: failure}
	}
	resolved := ResolvedVersion{Tokens: version, Source: "bundled"}
	if fromElements != "" {
		resolved.Elements = fromElements
		resolved.Warnings = append(resolved.Warnings,
			fmt.Sprintf("version %s is an elements version; using its coupled tokens@%s", fromElements, version))
	}
	return s.queryTokens(resolved, input)
}

// queryTokensWithEnv runs the normal workspace cascade when the caller does
// not pass a version. The resolved block keeps the elements, tokens, docs, and
// resolution source from that cascade.
func (s *Service) queryTokensWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input QueryTokensInput,
) (*mcp.CallToolResult, QueryTokensOutput) {
	if input.Version != "" {
		return nil, s.QueryTokens(ctx, input)
	}
	// A cursor carries the tokens version that produced it. Use that version
	// instead of re-running the workspace cascade, which could be ambiguous on
	// a follow-up page.
	if input.Cursor != "" {
		payload, failure := decodeCursor(input.Cursor)
		if failure != nil {
			return nil, QueryTokensOutput{Tokens: []TokenResult{}, Error: failure}
		}
		return nil, s.queryTokens(ResolvedVersion{
			Tokens: payload.Version,
			Source: "cursor",
		}, input)
	}
	resolved, required, failure := s.prepareResolution(ctx, newCallEnv(request), "", "", true)
	if required != nil {
		return required, QueryTokensOutput{}
	}
	if failure != nil {
		return nil, QueryTokensOutput{Tokens: []TokenResult{}, Error: failure}
	}
	return nil, s.queryTokens(resolved.Resolved, input)
}

func (s *Service) queryTokens(resolved ResolvedVersion, input QueryTokensInput) QueryTokensOutput {
	out := QueryTokensOutput{Tokens: []TokenResult{}, Resolved: resolved}
	if failure := s.dataFailure(); failure != nil {
		out.Error = failure
		return out
	}

	format := input.Format
	if format == "" {
		format = "value"
	}
	switch format {
	case "value", "css", "chain":
	default:
		out.Error = invalidArgument("format must be one of value, css, or chain")
		return out
	}

	detail := input.Detail
	if detail == "" {
		detail = "summary"
	}
	switch detail {
	case "summary", "full":
	default:
		out.Error = invalidArgument("detail must be summary or full")
		return out
	}
	out.Detail = detail

	theme := input.Theme
	switch theme {
	case "", "light", "dark", "auto":
	default:
		out.Error = invalidArgument("theme must be light, dark, or auto")
		return out
	}

	if input.PageSize < 0 {
		out.Error = invalidArgument("pageSize must be zero or positive")
		return out
	}

	version := out.Resolved.Tokens
	if version == "" {
		out.Error = &ToolError{
			Code:    "artifact_not_found",
			Message: "no bundled tokens data",
			Hint:    "build with generated data: make data build",
		}
		return out
	}
	if input.Query != "" && input.Token != "" {
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			"both query and token were supplied; token took precedence and query was ignored")
	}

	tokens, tokenMap, failure := s.loadTokens(version)
	if failure != nil {
		out.Error = failure
		return out
	}

	themes, failure := s.loadThemes(version)
	if failure != nil {
		out.Error = failure
		return out
	}

	var selected []*token.Token
	if input.Token != "" {
		found, ok := tokenMap.Get(input.Token)
		if !ok {
			out.Error = &ToolError{
				Code: "token_not_found",
				Message: fmt.Sprintf("token %q is not bundled in tokens@%s",
					truncateResponseText(input.Token, maxCandidateRunes), version),
				Hint: "use query to search by name or description",
			}
			return out
		}
		selected = []*token.Token{found}
	} else {
		needle := strings.ToLower(strings.TrimSpace(input.Query))
		for _, tok := range tokens {
			if needle == "" || tokenMatches(tok, needle) {
				selected = append(selected, tok)
			}
		}
	}

	sort.Slice(selected, func(i, j int) bool {
		return selected[i].Name < selected[j].Name
	})
	out.Total = len(selected)

	pageSize := input.PageSize
	if pageSize == 0 {
		pageSize = defaultTokenPage
	}
	if pageSize > maxTokenResults {
		pageSize = maxTokenResults
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("pageSize was capped at %d", maxTokenResults))
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
					"cursor resolves tokens@%s but this call resolves tokens@%s",
					payload.Version, version),
				Hint: "restart pagination without a cursor or use the same version",
			}
			return out
		}
		if payload.Offset > len(selected) {
			out.Error = invalidCursor("cursor offset is past the end of the result set")
			return out
		}
		offset = payload.Offset
	}

	effectiveTheme := theme
	if theme == "auto" {
		effectiveTheme = "light"
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			"theme auto used the light theme; resolve dark values on the client")
	}
	if theme != "" && len(themes["light"]) == 0 && len(themes["dark"]) == 0 {
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("tokens@%s has no light/dark theme map", version))
	}

	end := min(offset+pageSize, len(selected))
	out.Tokens = make([]TokenResult, 0, end-offset)
	for _, tok := range selected[offset:end] {
		out.Tokens = append(out.Tokens, tokenResultFor(tok, themes, effectiveTheme, theme, format, detail))
	}
	if end < len(selected) {
		out.NextCursor = encodeCursor(version, end, false)
	}
	return fitQueryTokensPage(out, s.responseBudget(detail == "full"), offset)
}

// resolveTokensVersion accepts a tokens version or, when the argument is not a
// bundled tokens release, an elements version whose coupled tokens release is
// used. This mirrors the coupling rule in docs/PLAN.md section 5.3.
func (s *Service) resolveTokensVersion(requested string) (version, fromElements string, failure *ToolError) {
	if requested == "" || s.store.Has(packageTokens, requested, kindTokens) {
		version, failure := s.resolveVersion(packageTokens, kindTokens, requested)
		return version, "", failure
	}
	if s.store.Has(packageElements, requested, kindManifest) {
		if coupled, ok := s.store.Index().TokensForElements(requested); ok &&
			s.store.Has(packageTokens, coupled, kindTokens) {
			return coupled, requested, nil
		}
	}
	return "", "", &ToolError{
		Code: "artifact_not_found",
		Message: fmt.Sprintf(
			"no tokens data for tokens@%s, and no coupled tokens release for elements@%s",
			requested, requested),
		Hint: "call rhds_resolve_version to list bundled tokens versions",
	}
}

func tokenResultFor(
	tok *token.Token,
	themes map[string]map[string]string,
	effectiveTheme, requestedTheme, format, detail string,
) TokenResult {
	result := TokenResult{
		Name:        tok.Name,
		CSSVariable: tok.CSSVariableName(),
		Type:        tok.Type,
		RawValue:    rawTokenValue(tok),
		AliasChain:  slices.Clone(tok.ResolutionChain),
		Source:      tokenSource(tok),
		Deprecated:  tok.Deprecated,
		Theme:       requestedTheme,
	}
	if detail == "full" {
		result.Description = tok.Description
		if tok.Deprecated && tok.DeprecationMessage != "" {
			result.Deprecation = tok.DeprecationMessage
		}
	}
	switch format {
	case "css":
		result.Value = fmt.Sprintf("%s: %s;", tok.CSSVariableName(), tok.DisplayValue())
	case "chain":
		result.Value = tokenChainValue(tok)
	default:
		result.Value = tok.DisplayValue()
	}
	if themeValue, ok := lookupThemeValue(themes, effectiveTheme, tok); ok {
		result.ThemeValue = themeValue
	}
	return result
}

// tokenSource reports where the token's value comes from. Theme composites
// that only exist in the generated default-theme.css are synthesized at load
// time and must not be reported as JSON tokens.
func tokenSource(tok *token.Token) string {
	extensions, ok := tok.Extensions["com.redhat.ux"].(map[string]any)
	if !ok {
		return tokenSourceJSON
	}
	if composite, _ := extensions["themeComposite"].(bool); composite {
		return tokenSourceCSS
	}
	return tokenSourceJSON
}

func tokenChainValue(tok *token.Token) string {
	parts := []string{tok.CSSVariableName()}
	for _, name := range tok.ResolutionChain {
		parts = append(parts, prefixedCSSName(name, tok.Prefix))
	}
	if len(parts) == 1 {
		return parts[0] + " (no alias)"
	}
	return strings.Join(parts, " -> ")
}

func prefixedCSSName(name, prefix string) string {
	if strings.HasPrefix(name, "--") {
		return name
	}
	prefix = strings.TrimLeft(prefix, "-")
	if prefix == "" {
		return "--" + strings.TrimLeft(name, "-")
	}
	name = strings.TrimLeft(name, "-")
	if strings.HasPrefix(name, prefix+"-") {
		return "--" + name
	}
	return "--" + prefix + "-" + name
}

func tokenMatches(tok *token.Token, needle string) bool {
	haystacks := []string{
		strings.ToLower(tok.Name),
		strings.ToLower(tok.CSSVariableName()),
		strings.ToLower(tok.Description),
		strings.ToLower(tok.Type),
	}
	for _, haystack := range haystacks {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func rawTokenValue(tok *token.Token) string {
	if tok.RawValue == nil {
		return tok.Value
	}
	if value, ok := tok.RawValue.(string); ok {
		return value
	}
	encoded, err := json.Marshal(tok.RawValue)
	if err != nil {
		return tok.Value
	}
	return string(encoded)
}

func lookupThemeValue(themes map[string]map[string]string, theme string, tok *token.Token) (string, bool) {
	if theme == "" || themes == nil {
		return "", false
	}
	values, ok := themes[theme]
	if !ok {
		return "", false
	}
	for _, key := range []string{
		tok.Name,
		tok.CSSVariableName(),
		strings.TrimPrefix(tok.CSSVariableName(), "--rh-"),
	} {
		if value, ok := values[key]; ok {
			return value, true
		}
	}
	return "", false
}
