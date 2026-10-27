// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Package tools implements the RHDS MCP tool surface used by the Phase B
// spike. The tools read bundled data lazily and never touch the network.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"bennypowers.dev/asimonim/parser"
	"bennypowers.dev/asimonim/resolver"
	"bennypowers.dev/asimonim/schema"
	"bennypowers.dev/asimonim/token"
	cemtypes "bennypowers.dev/cem/lsp/types"
	"bennypowers.dev/cem/manifest"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
)

// Data package names used in the index.
const (
	packageElements = "elements"
	packageTokens   = "tokens"
	packageDocs     = "docs"

	kindManifest = "manifest"
	kindTokens   = "tokens"
	kindThemes   = "themes"
	kindDocs     = "docs"
)

// ToolError is a structured error returned inside a tool result.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// ResolvedVersion reports the versions a tool used.
type ResolvedVersion struct {
	Elements string   `json:"elements,omitempty"`
	Tokens   string   `json:"tokens,omitempty"`
	Source   string   `json:"source,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Service owns the lazy data store and parser state shared by the tools.
type Service struct {
	store   *data.Store
	dataErr error
	logger  *slog.Logger

	documentsOnce sync.Once
	documents     cemtypes.Manager
	documentsErr  error
	docSeq        atomic.Int64
	manifestMu    sync.Mutex
	manifests     map[string]*manifest.Package
	tokenMu       sync.Mutex
	tokenSets     map[string]*tokenSet
	themeMu       sync.Mutex
	themes        map[string]map[string]map[string]string

	manifestParses atomic.Int64
	tokenParses    atomic.Int64
	themeParses    atomic.Int64
}

type tokenSet struct {
	tokens []*token.Token
	byName *token.Map
}

// New returns a tool service. A nil store with a nil error is treated as
// missing bundled data.
func New(store *data.Store, dataErr error) *Service {
	if store == nil && dataErr == nil {
		dataErr = data.ErrNoData
	}
	return &Service{
		store:     store,
		dataErr:   dataErr,
		logger:    slog.New(slog.DiscardHandler),
		manifests: make(map[string]*manifest.Package),
		tokenSets: make(map[string]*tokenSet),
		themes:    make(map[string]map[string]map[string]string),
	}
}

// WithLogger sets the service logger. Diagnostics go to stderr through the
// logger configured by main.
func (s *Service) WithLogger(logger *slog.Logger) *Service {
	if logger != nil {
		s.logger = logger
	}
	return s
}

func (s *Service) dataFailure() *ToolError {
	if s != nil && s.store != nil {
		return nil
	}
	err := s.dataErr
	if err == nil {
		err = data.ErrNoData
	}
	return toolErrorFrom(err)
}

func toolErrorFrom(err error) *ToolError {
	if err == nil {
		return nil
	}
	var structured *data.Error
	if errors.As(err, &structured) {
		return &ToolError{
			Code:    structured.Code,
			Message: structured.Message,
			Hint:    structured.Hint,
		}
	}
	return &ToolError{Code: "data_unavailable", Message: err.Error()}
}

func invalidArgument(message string) *ToolError {
	return &ToolError{Code: "invalid_argument", Message: message}
}

// resolveVersion picks an explicit version when given, otherwise the latest
// version that has an artifact of the requested kind.
func (s *Service) resolveVersion(pkg, kind, requested string) (string, *ToolError) {
	if s.store == nil {
		return "", s.dataFailure()
	}
	if requested != "" {
		if !s.store.Has(pkg, requested, kind) {
			return "", &ToolError{
				Code: "artifact_not_found",
				Message: fmt.Sprintf(
					"no %s data for %s@%s", kind, pkg, requested),
				Hint: "call rhds_resolve_version to list bundled versions",
			}
		}
		return requested, nil
	}

	versions := s.store.Index().Versions(pkg, kind)
	if len(versions) == 0 {
		return "", &ToolError{
			Code:    "artifact_not_found",
			Message: fmt.Sprintf("no bundled %s data for %s", kind, pkg),
			Hint:    "build with generated data: make data build",
		}
	}
	return versions[0], nil
}

func (s *Service) loadManifest(version string) (*manifest.Package, *ToolError) {
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()

	if cached, ok := s.manifests[version]; ok {
		return cached, nil
	}

	payload, err := s.store.Read(packageElements, version, kindManifest)
	if err != nil {
		return nil, toolErrorFrom(err)
	}
	var pkg manifest.Package
	if err := json.Unmarshal(payload, &pkg); err != nil {
		return nil, &ToolError{
			Code:    "invalid_manifest",
			Message: fmt.Sprintf("could not parse the CEM for elements@%s", version),
			Hint:    err.Error(),
		}
	}
	s.manifestParses.Add(1)
	s.manifests[version] = &pkg
	return &pkg, nil
}

func (s *Service) loadTokens(version string) ([]*token.Token, *token.Map, *ToolError) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()

	if cached, ok := s.tokenSets[version]; ok {
		return cached.tokens, cached.byName, nil
	}

	payload, err := s.store.Read(packageTokens, version, kindTokens)
	if err != nil {
		return nil, nil, toolErrorFrom(err)
	}

	tokens, err := parser.NewJSONParser().Parse(payload, parser.Options{
		Prefix:        "rh",
		SkipPositions: true,
	})
	if err != nil {
		return nil, nil, &ToolError{
			Code:    "invalid_tokens",
			Message: fmt.Sprintf("could not parse tokens@%s", version),
			Hint:    err.Error(),
		}
	}

	detected, detectErr := schema.DetectVersion(payload, nil)
	if detectErr != nil || detected == schema.Unknown {
		detected = schema.Draft
	}
	if err := resolver.ResolveAliases(tokens, detected); err != nil {
		return nil, nil, &ToolError{
			Code:    "invalid_tokens",
			Message: fmt.Sprintf("could not resolve token aliases for tokens@%s", version),
			Hint:    err.Error(),
		}
	}

	// Light-dark() composites are generated into the theme map instead of the
	// DTCG document. Add them back as lookup tokens so rhds_query_tokens can
	// find them and report their theme value.
	themes, themeErr := s.loadThemes(version)
	if themeErr != nil {
		return nil, nil, themeErr
	}
	if themes != nil {
		tokens = addThemeTokens(tokens, themes)
	}

	set := &tokenSet{tokens: tokens, byName: token.NewMap(tokens, "rh")}
	s.tokenParses.Add(1)
	s.tokenSets[version] = set
	return set.tokens, set.byName, nil
}

// addThemeTokens synthesizes tokens for theme-map entries that are not in the
// DTCG document. The light value is the token's base value; the dark value is
// reported through TokenResult.ThemeValue.
func addThemeTokens(tokens []*token.Token, themes map[string]map[string]string) []*token.Token {
	existing := make(map[string]bool, len(tokens))
	for _, tok := range tokens {
		existing[tok.Name] = true
	}
	result := tokens
	for _, cssName := range sortedKeys(themes["light"]) {
		name := strings.TrimPrefix(strings.TrimPrefix(cssName, "--rh-"), "--")
		if name == "" || existing[name] {
			continue
		}
		value := themes["light"][cssName]
		result = append(result, &token.Token{
			Name:          name,
			Value:         value,
			RawValue:      value,
			ResolvedValue: value,
			IsResolved:    true,
			Type:          token.TypeColor,
			Prefix:        "rh",
			Extensions: map[string]any{
				"com.redhat.ux": map[string]any{"themeComposite": true},
			},
		})
		existing[name] = true
	}
	return result
}

func (s *Service) loadThemes(version string) (map[string]map[string]string, *ToolError) {
	s.themeMu.Lock()
	defer s.themeMu.Unlock()

	if cached, ok := s.themes[version]; ok {
		return cached, nil
	}
	if !s.store.Has(packageTokens, version, kindThemes) {
		return nil, nil
	}
	payload, err := s.store.Read(packageTokens, version, kindThemes)
	if err != nil {
		return nil, toolErrorFrom(err)
	}
	themes := make(map[string]map[string]string)
	if err := json.Unmarshal(payload, &themes); err != nil {
		return nil, &ToolError{
			Code:    "invalid_themes",
			Message: fmt.Sprintf("could not parse the theme map for tokens@%s", version),
			Hint:    err.Error(),
		}
	}
	s.themeParses.Add(1)
	s.themes[version] = themes
	return themes, nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func trimPackagePath(modulePath string) string {
	return strings.TrimPrefix(modulePath, "./")
}

// Register adds the Phase B tools to the MCP server.
func Register(server *mcp.Server, svc *Service) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rhds_list_components",
		Title:       "List RHDS components",
		Description: "Lists bundled RHDS custom elements with summaries, module paths, and deprecation state. Results are paginated with an opaque cursor.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ListComponentsInput) (*mcp.CallToolResult, ListComponentsOutput, error) {
		out := svc.ListComponents(ctx, input)
		if out.Error != nil {
			return &mcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rhds_validate_markup",
		Title:       "Validate RHDS markup",
		Description: "Checks HTML or Twig markup against bundled CEM data for unknown elements, unknown attributes, invalid enum values, and deprecated APIs. It is not full schema validation.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ValidateMarkupInput) (*mcp.CallToolResult, ValidateMarkupOutput, error) {
		out := svc.ValidateMarkup(ctx, input)
		if out.Error != nil {
			return &mcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rhds_query_tokens",
		Title:       "Query RHDS design tokens",
		Description: "Looks up bundled RHDS design tokens by name or search query. Returns the CSS custom property, type, resolved value, alias chain, and theme value.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input QueryTokensInput) (*mcp.CallToolResult, QueryTokensOutput, error) {
		out := svc.QueryTokens(ctx, input)
		if out.Error != nil {
			return &mcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, nil
	})
}
