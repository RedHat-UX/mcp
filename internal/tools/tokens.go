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
)

const maxTokenResults = 100

// QueryTokensInput is the input for rhds_query_tokens.
type QueryTokensInput struct {
	Query   string `json:"query,omitempty" jsonschema:"Case-insensitive substring to search across token names and descriptions"`
	Token   string `json:"token,omitempty" jsonschema:"Exact token name, dot path, or CSS custom property"`
	Version string `json:"version,omitempty" jsonschema:"RHDS tokens version; defaults to the latest bundled version"`
	Theme   string `json:"theme,omitempty" jsonschema:"Theme to resolve: light, dark, or auto"`
	Format  string `json:"format,omitempty" jsonschema:"Value format: value, css, or chain. chain puts the alias path in value"`
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
}

// QueryTokensOutput is the output for rhds_query_tokens.
type QueryTokensOutput struct {
	Resolved ResolvedVersion `json:"resolved"`
	Tokens   []TokenResult   `json:"tokens"`
	Total    int             `json:"total"`
	Error    *ToolError      `json:"error,omitempty"`
}

// QueryTokens implements rhds_query_tokens.
func (s *Service) QueryTokens(_ context.Context, input QueryTokensInput) (out QueryTokensOutput) {
	defer func() { out = fitQueryTokens(out, defaultResponseBudget) }()
	out = QueryTokensOutput{Tokens: []TokenResult{}}
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

	theme := input.Theme
	switch theme {
	case "", "light", "dark", "auto":
	default:
		out.Error = invalidArgument("theme must be light, dark, or auto")
		return out
	}

	version, failure := s.resolveVersion(packageTokens, kindTokens, input.Version)
	if failure != nil {
		out.Error = failure
		return out
	}
	out.Resolved = ResolvedVersion{Tokens: version, Source: "bundled"}

	tokens, tokenMap, failure := s.loadTokens(version)
	if failure != nil {
		out.Error = failure
		return out
	}

	var themes map[string]map[string]string
	if theme != "" {
		themes, failure = s.loadThemes(version)
		if failure != nil {
			out.Error = failure
			return out
		}
	}

	var selected []*token.Token
	if input.Token != "" {
		found, ok := tokenMap.Get(input.Token)
		if !ok {
			out.Error = &ToolError{
				Code:    "token_not_found",
				Message: fmt.Sprintf("token %q is not bundled in tokens@%s", input.Token, version),
				Hint:    "use query to search by name or description",
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
	if len(selected) > maxTokenResults {
		selected = selected[:maxTokenResults]
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("results were capped at %d tokens; narrow the query", maxTokenResults))
	}

	effectiveTheme := theme
	if theme == "auto" {
		effectiveTheme = "light"
		if themes != nil {
			out.Resolved.Warnings = append(out.Resolved.Warnings,
				"theme auto used the light theme; resolve dark values on the client")
		}
	}
	if theme != "" && themes == nil {
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("no theme data is bundled for tokens@%s; themeValue is omitted", version))
	}

	for _, tok := range selected {
		result := TokenResult{
			Name:        tok.Name,
			CSSVariable: tok.CSSVariableName(),
			Type:        tok.Type,
			RawValue:    rawTokenValue(tok),
			AliasChain:  slices.Clone(tok.ResolutionChain),
			Description: tok.Description,
			Deprecated:  tok.Deprecated,
			Theme:       theme,
		}
		if tok.Deprecated && tok.DeprecationMessage != "" {
			result.Deprecation = tok.DeprecationMessage
		}
		switch format {
		case "css":
			result.Value = fmt.Sprintf("%s: %s;", tok.CSSVariableName(), tok.DisplayValue())
		case "chain":
			if len(tok.ResolutionChain) > 0 {
				result.Value = strings.Join(tok.ResolutionChain, " -> ")
			} else {
				result.Value = tok.DisplayValue()
			}
		default:
			result.Value = tok.DisplayValue()
		}
		result.Source = "json"
		if themeValue, ok := lookupThemeValue(themes, effectiveTheme, tok); ok {
			result.ThemeValue = themeValue
			result.Source = "css/default-theme.css"
		}
		out.Tokens = append(out.Tokens, result)
	}
	return out
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
