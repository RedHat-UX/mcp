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
	"golang.org/x/mod/semver"

	"github.com/redhat-ux/mcp/internal/cem"
	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/resolve"
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
	kindPatterns = "patterns"
)

// ToolError is a structured error returned inside a tool result.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Details any    `json:"details,omitempty"`
}

// ResolvedVersion reports the versions a tool used.
type ResolvedVersion struct {
	Elements string   `json:"elements,omitempty"`
	Tokens   string   `json:"tokens,omitempty"`
	Docs     string   `json:"docs,omitempty"`
	Source   string   `json:"source,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// resolution is the internal form of a resolved version plus the evidence
// that produced it.
type resolution struct {
	Resolved   ResolvedVersion
	Candidates []resolve.Candidate
	Ambiguity  *resolve.Ambiguity
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

	models   sync.Map // elements version -> *cem.Model
	docsTags sync.Map // docs tag -> map[tagName]bool

	// maxResponseBytes is the NFR-03 hard cap. Tests set a smaller value to
	// exercise the follow-up-call path.
	maxResponseBytes int
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
		store:            store,
		dataErr:          dataErr,
		logger:           slog.New(slog.DiscardHandler),
		manifests:        make(map[string]*manifest.Package),
		tokenSets:        make(map[string]*tokenSet),
		themes:           make(map[string]map[string]map[string]string),
		maxResponseBytes: hardResponseBudget,
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

func (s *Service) loadModel(version string) (*cem.Model, *ToolError) {
	if cached, ok := s.models.Load(version); ok {
		return cached.(*cem.Model), nil
	}
	pkg, failure := s.loadManifest(version)
	if failure != nil {
		return nil, failure
	}
	model, err := cem.Normalize(pkg)
	if err != nil {
		return nil, &ToolError{
			Code:    "invalid_manifest",
			Message: fmt.Sprintf("could not normalize the CEM for elements@%s", version),
			Hint:    err.Error(),
		}
	}
	actual, _ := s.models.LoadOrStore(version, model)
	return actual.(*cem.Model), nil
}

func (s *Service) bundledElements() []string {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Index().Versions(packageElements, kindManifest)
}

func (s *Service) bundledTokens() []string {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Index().Versions(packageTokens, kindTokens)
}

// docsVersionFor maps a resolved elements version to the newest bundled docs
// snapshot for the same major. If that major has no snapshot, the newest
// snapshot overall is used, matching the docs-only behavior in section 5.5.
func (s *Service) docsVersionFor(elementsVersion string) string {
	if s == nil || s.store == nil {
		return ""
	}
	versions := s.store.Index().Versions(packageDocs, kindDocs)
	if len(versions) == 0 {
		return ""
	}
	target := semver.Canonical("v" + strings.TrimPrefix(elementsVersion, "v"))
	if target == "" {
		return versions[0]
	}
	targetMajor := semver.Major(target)
	best := ""
	for _, tag := range versions {
		canonical := semver.Canonical("v" + strings.TrimPrefix(tag, "v"))
		if canonical == "" || semver.Major(canonical) != targetMajor {
			continue
		}
		if best == "" || semver.Compare(canonical, "v"+strings.TrimPrefix(best, "v")) > 0 {
			best = tag
		}
	}
	if best != "" {
		return best
	}
	return versions[0]
}

// bundledResolution resolves without touching the workspace. It is used by
// direct service calls in tests and when a tool has an explicit version.
func (s *Service) bundledResolution(explicit, tokensExplicit string) (resolution, *ToolError) {
	if failure := s.dataFailure(); failure != nil {
		return resolution{}, failure
	}
	if explicit != "" {
		result, err := resolve.Resolve(resolve.Options{
			Explicit:        explicit,
			BundledElements: s.bundledElements(),
		})
		if err != nil {
			return resolution{}, toolErrorFrom(err)
		}
		return s.resolutionFrom(result, tokensExplicit, true)
	}
	versions := s.bundledElements()
	if len(versions) == 0 {
		return resolution{}, &ToolError{
			Code:    "artifact_not_found",
			Message: "no bundled elements data",
			Hint:    "build with generated data: make data build",
		}
	}
	result := resolve.Result{
		Elements: versions[0],
		Source:   "bundled",
		Candidates: []resolve.Candidate{{
			Version:    versions[0],
			Source:     "bundled",
			Precedence: resolve.PrecedenceBundled,
			Level:      resolve.LevelBundled,
		}},
		Warnings: []string{
			fmt.Sprintf("no workspace version evidence was read; using latest bundled elements@%s", versions[0]),
		},
	}
	return s.resolutionFrom(result, tokensExplicit, true)
}

func (s *Service) resolutionFrom(
	result resolve.Result,
	tokensExplicit string,
	requireBundled bool,
) (resolution, *ToolError) {
	if failure := s.dataFailure(); failure != nil {
		return resolution{}, failure
	}
	elementsVersion := result.Elements
	if elementsVersion == "" {
		return resolution{}, &ToolError{
			Code:    "ambiguous_version",
			Message: "the workspace resolution is ambiguous",
			Hint:    "pass the version argument",
			Details: result.Ambiguity,
		}
	}
	warnings := append([]string(nil), result.Warnings...)
	bundled := s.store.Has(packageElements, elementsVersion, kindManifest)
	if !bundled {
		if requireBundled {
			return resolution{}, &ToolError{
				Code: "artifact_not_found",
				Message: fmt.Sprintf(
					"workspace resolution selected elements@%s, but this binary does not bundle that version",
					elementsVersion),
				Hint: "pass a bundled version with the version argument; " +
					"call rhds_resolve_version to list candidates and bundled versions",
				Details: map[string]any{
					"candidates": boundedCandidates(result.Candidates, maxOutputCandidates),
					"source":     result.Source,
				},
			}
		}
		warnings = append(warnings,
			fmt.Sprintf("elements@%s is not bundled; data lookups will require a bundled version", elementsVersion))
	}

	tokensVersion := ""
	coupled, coupledOK := s.store.Index().TokensForElements(elementsVersion)
	switch {
	case tokensExplicit != "":
		if !s.store.Has(packageTokens, tokensExplicit, kindTokens) {
			return resolution{}, &ToolError{
				Code: "artifact_not_found",
				Message: fmt.Sprintf(
					"no tokens data for tokens@%s", tokensExplicit),
				Hint: "call rhds_resolve_version to list bundled tokens versions",
			}
		}
		if coupledOK && coupled != tokensExplicit {
			warnings = append(warnings,
				fmt.Sprintf("tokensVersion %s overrides tokens@%s coupled to elements@%s",
					tokensExplicit, coupled, elementsVersion))
		}
		tokensVersion = tokensExplicit
	case coupledOK:
		tokensVersion = coupled
	default:
		if versions := s.bundledTokens(); len(versions) > 0 {
			tokensVersion = versions[0]
			warnings = append(warnings,
				fmt.Sprintf("elements@%s has no recorded tokens coupling; using latest bundled tokens@%s",
					elementsVersion, tokensVersion))
		}
	}

	return resolution{
		Resolved: ResolvedVersion{
			Elements: elementsVersion,
			Tokens:   tokensVersion,
			Docs:     s.docsVersionFor(elementsVersion),
			Source:   result.Source,
			Warnings: warnings,
		},
		Candidates: result.Candidates,
		Ambiguity:  result.Ambiguity,
	}, nil
}

// prepareResolution performs roots negotiation, the workspace cascade, and
// elicitation for one tool call. A non-nil CallToolResult is an
// input-required result that the caller must return unchanged.
func (s *Service) prepareResolution(
	ctx context.Context,
	env *callEnv,
	explicit string,
	tokensExplicit string,
	requireBundled bool,
) (resolution, *mcp.CallToolResult, *ToolError) {
	if env == nil {
		resolved, failure := s.bundledResolution(explicit, tokensExplicit)
		return resolved, nil, failure
	}
	if failure := s.dataFailure(); failure != nil {
		return resolution{}, nil, failure
	}
	if explicit != "" {
		result, err := resolve.Resolve(resolve.Options{
			Explicit:        explicit,
			BundledElements: s.bundledElements(),
		})
		if err != nil {
			return resolution{}, nil, toolErrorFrom(err)
		}
		resolved, failure := s.resolutionFrom(result, tokensExplicit, requireBundled)
		return resolved, nil, failure
	}

	state, _ := decodeRequestState(env.requestState)
	root, rootWarning, required, failure := s.acquireRoot(ctx, env, state)
	if failure != nil {
		return resolution{}, nil, failure
	}
	if required != nil {
		return resolution{}, required, nil
	}

	result, err := resolve.Resolve(resolve.Options{
		Root:            root,
		BundledElements: s.bundledElements(),
		Limits:          resolve.DefaultLimits(),
	})
	if err != nil {
		if errors.Is(err, resolve.ErrNoBundledElements) {
			return resolution{}, nil, &ToolError{
				Code:    "artifact_not_found",
				Message: "no bundled elements data",
				Hint:    "build with generated data: make data build",
			}
		}
		return resolution{}, nil, &ToolError{
			Code:    "workspace_unavailable",
			Message: "could not inspect the workspace for an @rhds/elements version",
			Hint:    err.Error(),
		}
	}
	if rootWarning != "" {
		result.Warnings = append([]string{rootWarning}, result.Warnings...)
	}

	if result.Ambiguity != nil {
		// A 2026-07-28 elicitation retry must not ask for roots again.
		state.Root = root
		choice, required, failure := s.resolveAmbiguity(ctx, env, state, result.Ambiguity)
		if failure != nil {
			if failure.Details == nil {
				failure.Details = ambiguityDetails(result.Ambiguity, explicit)
			}
			return resolution{
				Candidates: result.Candidates,
				Ambiguity:  result.Ambiguity,
			}, nil, failure
		}
		if required != nil {
			return resolution{}, required, nil
		}
		result.Elements = choice
		result.Source = "elicitation"
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("selected elements@%s via elicitation", choice))
	}

	resolved, failure := s.resolutionFrom(result, tokensExplicit, requireBundled)
	return resolved, nil, failure
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

// Register adds the RHDS tools to the MCP server.
func Register(server *mcp.Server, svc *Service) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rhds_resolve_version",
		Title:       "Resolve RHDS versions",
		Description: "Runs the version cascade without a data query: explicit version, lockfiles, installed packages, package.json, templates, then the latest bundled version. Reports every candidate with its source and precedence.",
		Annotations: readOnly,
	}, func(ctx context.Context, request *mcp.CallToolRequest, input ResolveVersionInput) (*mcp.CallToolResult, ResolveVersionOutput, error) {
		required, out := svc.resolveVersionWithEnv(ctx, request, input)
		if required != nil {
			return required, ResolveVersionOutput{}, nil
		}
		if out.Error != nil {
			return &mcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rhds_list_components",
		Title:       "List RHDS components",
		Description: "Lists bundled RHDS custom elements with summaries, module paths, and deprecation state. Results are paginated with an opaque, version-bound cursor.",
		Annotations: readOnly,
	}, func(ctx context.Context, request *mcp.CallToolRequest, input ListComponentsInput) (*mcp.CallToolResult, ListComponentsOutput, error) {
		required, out := svc.listComponentsWithEnv(ctx, request, input)
		if required != nil {
			return required, ListComponentsOutput{}, nil
		}
		if out.Error != nil {
			return &mcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rhds_get_component_api",
		Title:       "Get an RHDS component API",
		Description: "Returns one custom element's normalized API: attributes, properties, events, slots, CSS parts, CSS custom properties, and examples. Use sections and detail to keep the response small; oversized responses ask for a follow-up call instead of truncating.",
		Annotations: readOnly,
	}, func(ctx context.Context, request *mcp.CallToolRequest, input GetComponentAPIInput) (*mcp.CallToolResult, ComponentAPIOutput, error) {
		required, out := svc.getComponentAPIWithEnv(ctx, request, input)
		if required != nil {
			return required, ComponentAPIOutput{}, nil
		}
		if out.Error != nil {
			return &mcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rhds_search",
		Title:       "Search RHDS",
		Description: "Ranked discovery across components, tokens, docs, and examples. Returns the hit type, title, source path, version, and a short excerpt.",
		Annotations: readOnly,
	}, func(ctx context.Context, request *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		required, out := svc.searchWithEnv(ctx, request, input)
		if required != nil {
			return required, SearchOutput{}, nil
		}
		if out.Error != nil {
			return &mcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, nil
	})

	// TODO(cem#458): replace the interim lsp/document path with
	// bennypowers.dev/cem/validate/markup once cem#458 is merged and released
	// in a cem tag. This is a Gate: do not add a replace directive or fork.
	mcp.AddTool(server, &mcp.Tool{
		Name:        "rhds_validate_markup",
		Title:       "Validate RHDS markup",
		Description: "Checks HTML or Twig markup against bundled CEM data for unknown elements, unknown attributes, invalid enum values, and deprecated APIs. It is not full schema validation.",
		Annotations: readOnly,
	}, func(ctx context.Context, request *mcp.CallToolRequest, input ValidateMarkupInput) (*mcp.CallToolResult, ValidateMarkupOutput, error) {
		_ = request
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
	}, func(ctx context.Context, request *mcp.CallToolRequest, input QueryTokensInput) (*mcp.CallToolResult, QueryTokensOutput, error) {
		_ = request
		out := svc.QueryTokens(ctx, input)
		if out.Error != nil {
			return &mcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, nil
	})
}
