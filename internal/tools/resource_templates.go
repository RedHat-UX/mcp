// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 8.1 requires roots negotiation; the SDK marks the feature deprecated but keeps it functional during the SEP-2577 window.

package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/resolve"
)

// Progressive-disclosure resource templates. The rhds://data authority keeps
// these templates disjoint from the rhds://elements component templates, which
// the SDK iterates lexicographically rather than in registration order.
//
// Each family has two forms: one without a query and one with a literal "?"
// followed by a reserved-expansion query. The SDK matches templates by
// uritemplate regexp and ignores query variable names; {?version} admits only
// unreserved characters and "=", so "&" would never route. {+query} admits the
// whole query string, and the no-query twin keeps the sets disjoint. The
// parser validates the version key itself.
const (
	elementsResourceTemplate          = "rhds://data/elements{/path*}"
	elementsQueryResourceTemplate     = "rhds://data/elements{/path*}?{+query}"
	tokensResourceTemplate            = "rhds://data/tokens{/path*}"
	tokensQueryResourceTemplate       = "rhds://data/tokens{/path*}?{+query}"
	docsResourceTemplate              = "rhds://data/docs{/path*}"
	docsQueryResourceTemplate         = "rhds://data/docs{/path*}?{+query}"
	releaseNotesResourceTemplate      = "rhds://data/release-notes{/path*}"
	releaseNotesQueryResourceTemplate = "rhds://data/release-notes{/path*}?{+query}"
)

// artifactResourceTarget is one parsed rhds://data read. VersionExplicit
// distinguishes an absent version query from an empty one, which the parser
// rejects.
type artifactResourceTarget struct {
	Package         string
	Kind            string
	MIMEType        string
	Path            []string
	Version         string
	VersionExplicit bool
}

// parseResourceURI parses rhds://data/{package}{/path*} with an optional
// ?version= query. It reads EscapedPath so an encoded slash stays
// distinguishable until after each segment is unescaped and re-split.
// Workspace files are untrusted input, so the parser rejects empty, dot, and
// fragment components and requires exactly one version key with one non-empty
// value.
func parseResourceURI(uri string) (artifactResourceTarget, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return artifactResourceTarget{}, fmt.Errorf("resource %s: %w", uri, err)
	}
	if parsed.Scheme != "rhds" || parsed.Host != "data" {
		return artifactResourceTarget{}, fmt.Errorf("resource %s: want an rhds://data URI", uri)
	}
	// url.Parse cannot distinguish an empty fragment from no fragment, so
	// check the raw URI. The {+query} form admits "#" precisely so the parser,
	// not the router, rejects the URI.
	if strings.Contains(uri, "#") {
		return artifactResourceTarget{}, fmt.Errorf("resource %s: fragments are not supported", uri)
	}

	escaped := parsed.EscapedPath()
	if escaped == "" || !strings.HasPrefix(escaped, "/") {
		return artifactResourceTarget{}, fmt.Errorf("resource %s: missing package segment", uri)
	}
	raw := strings.Split(escaped[1:], "/")
	segments := make([]string, 0, len(raw))
	for _, segment := range raw {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return artifactResourceTarget{}, fmt.Errorf("resource %s: invalid path escape: %w", uri, err)
		}
		for _, part := range strings.Split(decoded, "/") {
			switch part {
			case "":
				return artifactResourceTarget{}, fmt.Errorf("resource %s: empty path segment", uri)
			case ".", "..":
				return artifactResourceTarget{}, fmt.Errorf("resource %s: dot path segments are not supported", uri)
			}
			segments = append(segments, part)
		}
	}
	if len(segments) == 0 {
		return artifactResourceTarget{}, fmt.Errorf("resource %s: missing package segment", uri)
	}

	pkg := segments[0]
	kind, mimeType, ok := artifactKindFor(pkg, segments[1:])
	if !ok {
		return artifactResourceTarget{}, fmt.Errorf(
			"resource %s: unsupported package or path", uri)
	}
	target := artifactResourceTarget{
		Package:  pkg,
		Kind:     kind,
		MIMEType: mimeType,
		Path:     segments[1:],
	}

	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return artifactResourceTarget{}, fmt.Errorf("resource %s: %w", uri, err)
	}
	for key, values := range query {
		if key != "version" {
			return artifactResourceTarget{}, fmt.Errorf(
				"resource %s: unsupported query parameter %q", uri, key)
		}
		if len(values) != 1 {
			return artifactResourceTarget{}, fmt.Errorf(
				"resource %s: the version query parameter is repeated", uri)
		}
		if values[0] == "" {
			return artifactResourceTarget{}, fmt.Errorf(
				"resource %s: the version query parameter is empty", uri)
		}
		target.Version = values[0]
		target.VersionExplicit = true
	}
	return target, nil
}

// artifactKindFor maps a package and its remaining path segments to an
// artifact kind and MIME type. Doc pages keep their leading docs/ or elements/
// segment, matching the generated index.
func artifactKindFor(pkg string, path []string) (kind, mimeType string, ok bool) {
	const (
		jsonMIME     = "application/json"
		markdownMIME = "text/markdown"
	)
	switch pkg {
	case packageElements:
		if len(path) == 0 || (len(path) == 1 && path[0] == kindManifest) {
			return kindManifest, jsonMIME, true
		}
		return "", "", false
	case packageTokens:
		if len(path) == 0 || (len(path) == 1 && path[0] == kindTokens) {
			return kindTokens, jsonMIME, true
		}
		if len(path) == 1 && path[0] == kindThemes {
			return kindThemes, jsonMIME, true
		}
		return "", "", false
	case packageDocs:
		if len(path) == 0 || (len(path) == 1 && path[0] == kindDocs+".json") {
			return kindDocs, jsonMIME, true
		}
		if len(path) == 1 && path[0] == kindPatterns+".json" {
			return kindPatterns, jsonMIME, true
		}
		return "doc:" + strings.Join(path, "/"), markdownMIME, true
	case packageReleaseNotes:
		if len(path) == 0 || (len(path) == 1 && path[0] == kindReleaseNotes) {
			return kindReleaseNotes, markdownMIME, true
		}
		if len(path) == 1 && path[0] == kindChangelog {
			return kindChangelog, markdownMIME, true
		}
		return "", "", false
	default:
		return "", "", false
	}
}

// registerArtifactResourceTemplates adds the four progressive-disclosure
// templates. Reads resolve a version from the query, the workspace cascade, or
// the package defaults before reading the artifact.
func registerArtifactResourceTemplates(server *mcp.Server, service *Service) {
	handler := artifactResourceHandler(service)
	templates := []struct {
		uri         string
		name        string
		mimeType    string
		description string
	}{
		{
			uri:      elementsResourceTemplate,
			name:     "data-elements",
			mimeType: "application/json",
			description: "Bundled elements data. An empty path or manifest reads the custom " +
				"elements manifest. Add ?version= to pin a version.",
		},
		{
			uri:      elementsQueryResourceTemplate,
			name:     "data-elements-query",
			mimeType: "application/json",
			description: "Bundled elements data with an explicit ?version= query. An empty " +
				"path or manifest reads the custom elements manifest.",
		},
		{
			uri:      tokensResourceTemplate,
			name:     "data-tokens",
			mimeType: "application/json",
			description: "Bundled tokens data. An empty path or tokens reads normalized DTCG " +
				"tokens; themes reads the theme map. Add ?version= to pin a version.",
		},
		{
			uri:      tokensQueryResourceTemplate,
			name:     "data-tokens-query",
			mimeType: "application/json",
			description: "Bundled tokens data with an explicit ?version= query. An empty path " +
				"or tokens reads normalized DTCG tokens; themes reads the theme map.",
		},
		{
			uri:  docsResourceTemplate,
			name: "data-docs",
			description: "Bundled docs data. An empty path or docs.json reads the docs index, " +
				"patterns.json reads patterns, and any other path reads that Markdown page. " +
				"Add ?version= to pin a version.",
		},
		{
			uri:      docsQueryResourceTemplate,
			name:     "data-docs-query",
			mimeType: "application/json",
			description: "Bundled docs data with an explicit ?version= query. An empty path or " +
				"docs.json reads the docs index, patterns.json reads patterns, and any other " +
				"path reads that Markdown page.",
		},
		{
			uri:      releaseNotesResourceTemplate,
			name:     "data-release-notes",
			mimeType: "text/markdown",
			description: "Bundled release notes. An empty path or release-notes reads the " +
				"release notes and changelog reads the changelog. Add ?version= to pin a version.",
		},
		{
			uri:      releaseNotesQueryResourceTemplate,
			name:     "data-release-notes-query",
			mimeType: "text/markdown",
			description: "Bundled release notes with an explicit ?version= query. An empty path " +
				"or release-notes reads the release notes and changelog reads the changelog.",
		},
	}
	for _, template := range templates {
		server.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: template.uri,
			Name:        template.name,
			Description: template.description,
			// Docs templates omit MIMEType because a read is JSON for the
			// index and Markdown for a page; the handler sets it per read.
			MIMEType: template.mimeType,
		}, handler)
	}
}

func artifactResourceHandler(service *Service) mcp.ResourceHandler {
	return func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if request == nil || request.Params == nil {
			return nil, fmt.Errorf("artifact resource request has no parameters")
		}
		if service == nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		uri := request.Params.URI
		target, err := parseResourceURI(uri)
		if err != nil {
			// Return the parser's specific error instead of a generic router
			// not-found so unexpected query keys and fragments are diagnosable.
			return nil, err
		}
		version, failure := service.resourceTargetVersion(ctx, request, target)
		if failure != nil {
			switch failure.Code {
			case "ambiguous_version", "workspace_unavailable", "artifact_not_found":
				return nil, fmt.Errorf("resource %s: %s: %s", uri, failure.Message, failure.Hint)
			default:
				return nil, mcp.ResourceNotFoundError(uri)
			}
		}
		payload, err := service.store.Read(target.Package, version, target.Kind)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return textResource(uri, target.MIMEType, payload), nil
	}
}

// resourceTargetVersion picks the version for one read. An explicit
// ?version= uses the same validation as the tools, so tokens/themes rejects a
// version that has no theme map. An omitted version uses the resource
// resolution path, which never elicits.
func (s *Service) resourceTargetVersion(
	ctx context.Context,
	request *mcp.ReadResourceRequest,
	target artifactResourceTarget,
) (string, *ToolError) {
	if target.VersionExplicit {
		return s.resolveVersion(target.Package, target.Kind, target.Version)
	}
	resolved, failure := s.resourceResolution(ctx, request)
	if failure != nil {
		return "", failure
	}
	return s.resourceResolutionVersion(target, resolved)
}

// resourceResolutionVersion maps a workspace resolution to the package
// specific version. Release notes follow the resolved elements version when
// that version has notes, otherwise the latest bundled notes; the changelog is
// always the single latest bundled changelog.
func (s *Service) resourceResolutionVersion(
	target artifactResourceTarget,
	resolved resolution,
) (string, *ToolError) {
	if s.store == nil {
		return "", s.dataFailure()
	}
	var version string
	switch target.Package {
	case packageElements:
		version = resolved.Resolved.Elements
	case packageTokens:
		version = resolved.Resolved.Tokens
	case packageDocs:
		version = resolved.Resolved.Docs
	case packageReleaseNotes:
		if target.Kind == kindChangelog {
			versions := s.store.Index().Versions(packageReleaseNotes, kindChangelog)
			if len(versions) == 0 {
				return "", releaseNotesFailure(kindChangelog)
			}
			version = versions[0]
			break
		}
		version = resolved.Resolved.Elements
		if version == "" || !s.store.Has(packageReleaseNotes, version, target.Kind) {
			versions := s.store.Index().Versions(packageReleaseNotes, kindReleaseNotes)
			if len(versions) == 0 {
				return "", releaseNotesFailure(kindReleaseNotes)
			}
			version = versions[0]
		}
	default:
		return "", &ToolError{
			Code:    "artifact_not_found",
			Message: fmt.Sprintf("unsupported resource package %q", target.Package),
		}
	}
	if version == "" || !s.store.Has(target.Package, version, target.Kind) {
		return "", &ToolError{
			Code: "artifact_not_found",
			Message: fmt.Sprintf(
				"no %s data for %s@%s", target.Kind, target.Package, version),
			Hint: "pass ?version=<bundled-version> or read " + packagesResourceURI +
				" to list bundled versions",
		}
	}
	return version, nil
}

func releaseNotesFailure(kind string) *ToolError {
	return &ToolError{
		Code:    "artifact_not_found",
		Message: fmt.Sprintf("no bundled %s data for %s", kind, packageReleaseNotes),
		Hint:    "build with generated data: make data build",
	}
}

// resourceResolution runs the workspace cascade for a resource read. It
// mirrors the tools without elicitation: ambiguity becomes a resource error
// that names the bundled versions the client can pin with ?version=.
func (s *Service) resourceResolution(
	ctx context.Context,
	request *mcp.ReadResourceRequest,
) (resolution, *ToolError) {
	if failure := s.dataFailure(); failure != nil {
		return resolution{}, failure
	}
	root, warnings, failure := resourceWorkspaceRoot(ctx, request)
	if failure != nil {
		return resolution{}, failure
	}
	return s.resourceResolutionAtRoot(root, warnings)
}

func (s *Service) resolutionFromCascade(
	result resolve.Result,
	rootWarnings []string,
) (resolution, *ToolError) {
	if result.Ambiguity != nil {
		return resolution{
				Candidates: result.Candidates,
				Ambiguity:  result.Ambiguity,
			},
			resourceAmbiguityFailure(result.Ambiguity, s.bundledElements())
	}
	resolved, failure := s.resolutionFrom(result, "", true)
	if failure != nil {
		return resolution{}, failure
	}
	resolved.Resolved.Warnings = append(
		append([]string(nil), rootWarnings...), resolved.Resolved.Warnings...)
	return resolved, nil
}

// resourceResolutionAtRoot resolves at an explicit workspace root. Tests use
// it to exercise the cache without a transport, and resourceResolution uses it
// after acquiring the client root.
func (s *Service) resourceResolutionAtRoot(
	root string,
	rootWarnings []string,
) (resolution, *ToolError) {
	if failure := s.dataFailure(); failure != nil {
		return resolution{}, failure
	}
	result, failure := s.resourceCascadeAtRoot(root)
	if failure != nil {
		return resolution{}, failure
	}
	return s.resolutionFromCascade(result, rootWarnings)
}

func resourceAmbiguityFailure(ambiguity *resolve.Ambiguity, bundled []string) *ToolError {
	sources := make([]string, 0, min(len(ambiguity.Candidates), 5))
	for i, candidate := range ambiguity.Candidates {
		if i >= 5 {
			sources = append(sources,
				fmt.Sprintf("and %d more candidates", len(ambiguity.Candidates)-5))
			break
		}
		sources = append(sources, fmt.Sprintf("%s from %s",
			truncateResponseText(candidate.Version, maxCandidateRunes),
			truncateResponseText(candidate.Source, 300)))
	}
	hint := "pass ?version=<bundled-version>"
	if offered := offeredVersions(ambiguity, bundled); len(offered) > 0 {
		hint = "pass ?version=" + offered[0]
	}
	return &ToolError{
		Code: "ambiguous_version",
		Message: fmt.Sprintf(
			"workspace sources resolve @rhds/elements to different majors %v: %s",
			ambiguity.Majors, strings.Join(sources, "; ")),
		Hint:    hint,
		Details: ambiguityDetails(ambiguity, ""),
	}
}

// resourceCascade acquires a workspace root and runs the version cascade. It
// never elicits and never calls prepareResolution.
func (s *Service) resourceCascade(
	ctx context.Context,
	request *mcp.ReadResourceRequest,
) (resolve.Result, []string, *ToolError) {
	if failure := s.dataFailure(); failure != nil {
		return resolve.Result{}, nil, failure
	}
	root, warnings, failure := resourceWorkspaceRoot(ctx, request)
	if failure != nil {
		return resolve.Result{}, nil, failure
	}
	result, failure := s.resourceCascadeAtRoot(root)
	if failure != nil {
		return resolve.Result{}, nil, failure
	}
	return result, warnings, nil
}

// resourceWorkspaceRoot returns the root for a resource read: cwd unless the
// client advertises roots and negotiated a pre-2026-07-28 protocol. There is no
// multi-round-trip roots flow here because resources cannot elicit.
func resourceWorkspaceRoot(
	ctx context.Context,
	request *mcp.ReadResourceRequest,
) (string, []string, *ToolError) {
	env := newResourceEnv(request)
	if env == nil || !env.rootsCapability || env.session == nil || env.supportsMRTR() {
		root, warning, _, failure := currentWorkingDirectory()
		return root, warningList(warning), failure
	}
	result, err := env.session.ListRoots(ctx, &mcp.ListRootsParams{}) //nolint:staticcheck // roots required by PLAN
	if err != nil {
		root, warning, _, failure := currentWorkingDirectory()
		if failure != nil {
			return "", nil, failure
		}
		return root, warningList(warning + "; the roots request failed: " + err.Error()), nil
	}
	if root, ok := firstFileRoot(result); ok {
		return root, nil, nil
	}
	root, warning, _, failure := currentWorkingDirectory()
	if failure != nil {
		return "", nil, failure
	}
	return root, warningList(warning + "; the client returned no file roots"), nil
}

func newResourceEnv(request *mcp.ReadResourceRequest) *callEnv {
	if request == nil {
		return nil
	}
	env := &callEnv{
		session:         request.Session,
		protocolVersion: request.ProtocolVersion(),
	}
	if request.Params != nil {
		env.inputResponses = request.Params.InputResponses
		env.requestState = request.Params.RequestState
	}
	if capabilities := request.ClientCapabilities(); capabilities != nil {
		env.rootsCapability = capabilities.RootsV2 != nil //nolint:staticcheck // roots required by PLAN
		env.elicitCapability = capabilities.Elicitation != nil
	}
	return env
}

func warningList(warning string) []string {
	if warning == "" {
		return nil
	}
	return []string{warning}
}

// resourceLockfileNames is the cache-key search order. A workspace with none
// of these files is rescanned on every read.
var resourceLockfileNames = []string{
	"package-lock.json",
	"pnpm-lock.yaml",
	"yarn.lock",
	"bun.lock",
}

type resourceResolutionKey struct {
	root      string
	lockfile  string
	modTime   time.Time
	size      int64
	cacheable bool
}

type resourceResolutionEntry struct {
	key    resourceResolutionKey
	result resolve.Result
}

func resourceResolutionKeyFor(root string) resourceResolutionKey {
	for _, name := range resourceLockfileNames {
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		return resourceResolutionKey{
			root:      root,
			lockfile:  path,
			modTime:   info.ModTime(),
			size:      info.Size(),
			cacheable: true,
		}
	}
	return resourceResolutionKey{root: root}
}

// resourceCascadeAtRoot runs the cascade and caches the raw result when a
// lockfile anchors the key. Invalidation is implicit: a changed path, mtime,
// or size misses the single cached entry.
func (s *Service) resourceCascadeAtRoot(root string) (resolve.Result, *ToolError) {
	key := resourceResolutionKeyFor(root)
	if key.cacheable {
		s.resourceCacheMu.Lock()
		if s.resourceCache != nil && s.resourceCache.key == key {
			result := s.resourceCache.result
			s.resourceCacheMu.Unlock()
			return result, nil
		}
		s.resourceCacheMu.Unlock()
	}

	result, err := s.resolve(resolve.Options{
		Root:            root,
		BundledElements: s.bundledElements(),
		Limits:          resolve.DefaultLimits(),
	})
	if err != nil {
		if errors.Is(err, resolve.ErrNoBundledElements) {
			return resolve.Result{}, &ToolError{
				Code:    "artifact_not_found",
				Message: "no bundled elements data",
				Hint:    "build with generated data: make data build",
			}
		}
		return resolve.Result{}, &ToolError{
			Code:    "workspace_unavailable",
			Message: "could not inspect the workspace for an @rhds/elements version",
			Hint:    err.Error(),
		}
	}

	if key.cacheable {
		s.resourceCacheMu.Lock()
		s.resourceCache = &resourceResolutionEntry{key: key, result: result}
		s.resourceCacheMu.Unlock()
	}
	return result, nil
}
