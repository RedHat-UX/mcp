// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/mod/semver"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/resolve"
)

// packagesResourceURI is the small discovery index. It is registered even in a
// build without bundled data so a client can still see why data is missing.
const packagesResourceURI = "rhds://packages/"

// PackageSummary is one package row in the discovery index.
type PackageSummary struct {
	Package  string   `json:"package"`
	Latest   string   `json:"latest,omitempty"`
	Versions []string `json:"versions,omitempty"`
	Count    int      `json:"count"`
	Kinds    []string `json:"kinds,omitempty"`
	Resolved string   `json:"resolved,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Level    string   `json:"level,omitempty"`
	Source   string   `json:"source,omitempty"`
}

// PackagesResourcePayload is the rhds://packages/ body. Candidates are dropped
// first when the response exceeds the budget, then version lists collapse to
// latest plus count. The resource never returns an error for size.
type PackagesResourcePayload struct {
	Resolved   ResolvedVersion     `json:"resolved"`
	Candidates []resolve.Candidate `json:"candidates,omitempty"`
	Ambiguity  *resolve.Ambiguity  `json:"ambiguity,omitempty"`
	Packages   []PackageSummary    `json:"packages"`
	Warnings   []string            `json:"warnings,omitempty"`
	Truncated  bool                `json:"truncated,omitempty"`
	Error      *ToolError          `json:"error,omitempty"`
}

func registerPackagesResource(server *mcp.Server, service *Service) {
	server.AddResource(&mcp.Resource{
		URI:         packagesResourceURI,
		Name:        "packages",
		Title:       "RHDS package index",
		Description: "Bundled @rhds/elements, @rhds/tokens, docs, and release-note versions with the resolved workspace version, candidates, and warnings.",
		MIMEType:    "application/json",
	}, packagesResourceHandler(service))
}

func packagesResourceHandler(service *Service) mcp.ResourceHandler {
	return func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		payload, required := buildPackagesResource(ctx, request, service)
		if required != nil {
			return required, nil
		}
		trimPackagesResource(service, &payload)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("resource %s: %w", packagesResourceURI, err)
		}
		return textResource(packagesResourceURI, "application/json", encoded), nil
	}
}

func buildPackagesResource(
	ctx context.Context,
	request *mcp.ReadResourceRequest,
	service *Service,
) (PackagesResourcePayload, *mcp.ReadResourceResult) {
	payload := PackagesResourcePayload{Packages: []PackageSummary{}}
	if service == nil {
		payload.Error = toolErrorFrom(data.ErrNoData)
		return payload, nil
	}
	if failure := service.dataFailure(); failure != nil {
		payload.Error = failure
		return payload, nil
	}

	result, rootWarnings, required, failure := service.resourceCascade(ctx, request)
	if required != nil {
		return payload, required
	}
	if failure != nil {
		payload.Error = boundedToolError(failure)
		payload.Warnings = append(payload.Warnings, failure.Message)
	} else {
		payload.Candidates = boundedCandidates(result.Candidates, maxOutputCandidates)
		payload.Ambiguity = boundedAmbiguity(result.Ambiguity)
		payload.Warnings = append(payload.Warnings, rootWarnings...)
		payload.Warnings = append(payload.Warnings, result.Warnings...)
		if result.Ambiguity != nil {
			payload.Warnings = append(payload.Warnings, fmt.Sprintf(
				"workspace resolution is ambiguous across majors %v; pass ?version=<bundled-version> to pin a version",
				result.Ambiguity.Majors))
		} else if resolved, resolutionFailure := service.resolutionFrom(result, "", true); resolutionFailure == nil {
			payload.Resolved = resolved.Resolved
			// Warnings are carried once, at the top level of the payload.
			payload.Resolved.Warnings = nil
			payload.Candidates = boundedCandidates(resolved.Candidates, maxOutputCandidates)
			payload.Ambiguity = boundedAmbiguity(resolved.Ambiguity)
		} else {
			payload.Warnings = append(payload.Warnings, resolutionFailure.Message)
		}
	}

	payload.Packages = service.packageSummaries(payload.Resolved, result)
	payload.Warnings = sanitizeWarnings(payload.Warnings)
	return payload, nil
}

// boundedAmbiguity caps and shortens the candidate evidence so a hostile
// workspace cannot inflate the package index.
func boundedAmbiguity(ambiguity *resolve.Ambiguity) *resolve.Ambiguity {
	if ambiguity == nil {
		return nil
	}
	bounded := *ambiguity
	bounded.Candidates = boundedCandidates(ambiguity.Candidates, maxOutputCandidates)
	return &bounded
}

// boundedToolError shortens client-visible error text without dropping the
// code or the option to report it.
func boundedToolError(failure *ToolError) *ToolError {
	if failure == nil {
		return nil
	}
	bounded := *failure
	bounded.Message = truncateResponseText(failure.Message, maxWarningRunes)
	bounded.Hint = truncateResponseText(failure.Hint, maxWarningRunes)
	return &bounded
}

func (s *Service) packageSummaries(
	resolved ResolvedVersion,
	result resolve.Result,
) []PackageSummary {
	if s.store == nil {
		return []PackageSummary{}
	}
	summaries := make([]PackageSummary, 0, 4)
	for _, pkg := range []string{
		packageElements, packageTokens, packageDocs, packageReleaseNotes,
	} {
		versions := s.packageVersions(pkg)
		summary := PackageSummary{
			Package:  pkg,
			Versions: versions,
			Count:    len(versions),
			Kinds:    s.packageKinds(pkg),
		}
		if len(versions) > 0 {
			summary.Latest = versions[0]
		}
		summary.Resolved, summary.Reason, summary.Level, summary.Source =
			s.packageResolution(pkg, summary.Latest, resolved, result)
		summaries = append(summaries, summary)
	}
	return summaries
}

var packagePrimaryKinds = map[string]string{
	packageElements:     kindManifest,
	packageTokens:       kindTokens,
	packageDocs:         kindDocs,
	packageReleaseNotes: kindReleaseNotes,
}

// packageVersions returns the newest-first versions for the package's primary
// artifact kind. The changelog is deliberately excluded because its version is
// the literal "latest".
func (s *Service) packageVersions(pkg string) []string {
	kind, ok := packagePrimaryKinds[pkg]
	if !ok {
		return nil
	}
	return s.store.Index().Versions(pkg, kind)
}

// packageKinds collapses per-page doc kinds into one "doc" family so a docs
// snapshot with thousands of pages does not inflate the index.
func (s *Service) packageKinds(pkg string) []string {
	seen := make(map[string]bool)
	var kinds []string
	for _, artifact := range s.store.Index().Artifacts {
		if artifact.Package != pkg {
			continue
		}
		kind := artifact.Kind
		if strings.HasPrefix(kind, "doc:") {
			kind = "doc"
		}
		if seen[kind] {
			continue
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// packageResolution picks the package version and records why, mirroring the
// resource read semantics. Workspace evidence is attributed to a package only
// when the version served actually matches that evidence; otherwise the reason
// names the fallback and any unbundled workspace version.
func (s *Service) packageResolution(
	pkg, latest string,
	resolved ResolvedVersion,
	result resolve.Result,
) (version, reason, level, source string) {
	version = latest
	workspaceElements := resolvedWorkspaceElements(resolved, result)
	switch pkg {
	case packageElements:
		if workspaceElements != "" && s.store.Has(packageElements, workspaceElements, kindManifest) {
			version = workspaceElements
			// On a successful cascade result.Source is the evidence source
			// (for example "package-lock.json"); on an ambiguous one it is the
			// winning level. Match on source first, then the ambiguity level.
			for _, candidate := range result.Candidates {
				if candidate.Source == result.Source {
					level = candidate.Level
					source = candidate.Source
					break
				}
			}
			if level == "" && result.Ambiguity != nil {
				level = result.Ambiguity.Level
			}
			if source == "" && result.Ambiguity == nil {
				source = result.Source
			}
			if level != "" && source != "" {
				reason = fmt.Sprintf("%s evidence from %s", level, source)
			} else if level != "" {
				reason = level + " evidence"
			} else {
				reason = "latest bundled version"
			}
			break
		}
		level = "bundled"
		if workspaceElements != "" {
			reason = fmt.Sprintf(
				"workspace resolves elements@%s, which is not bundled; using latest bundled elements@%s",
				workspaceElements, latest)
		} else {
			reason = "latest bundled version"
		}
	case packageTokens:
		if resolved.Tokens != "" && s.store.Has(packageTokens, resolved.Tokens, kindTokens) {
			version = resolved.Tokens
		}
		if coupled, ok := s.store.Index().TokensForElements(workspaceElements); ok {
			if coupled == version {
				reason = fmt.Sprintf("tokens@%s is coupled to elements@%s", coupled, workspaceElements)
			} else {
				reason = fmt.Sprintf(
					"elements@%s couples tokens@%s, which is not bundled; using tokens@%s",
					workspaceElements, coupled, version)
			}
			level = "coupling"
			source = "elements@" + workspaceElements
		} else {
			reason = "latest bundled tokens version"
			if workspaceElements != "" {
				reason = fmt.Sprintf(
					"latest bundled tokens version; elements@%s has no recorded coupling",
					workspaceElements)
			}
			level = "bundled"
		}
	case packageDocs:
		if resolved.Docs != "" && s.store.Has(packageDocs, resolved.Docs, kindDocs) {
			version = resolved.Docs
		}
		if workspaceElements != "" && sameMajor(version, workspaceElements) {
			reason = fmt.Sprintf(
				"newest bundled docs snapshot matching the elements@%s major",
				workspaceElements)
			level = "major-match"
			source = "elements@" + workspaceElements
			break
		}
		level = "fallback"
		reason = "latest bundled docs snapshot"
		if workspaceElements != "" {
			reason = fmt.Sprintf(
				"no bundled docs snapshot for the elements@%s major; using newest bundled docs@%s",
				workspaceElements, version)
			source = "elements@" + workspaceElements
		}
	case packageReleaseNotes:
		if workspaceElements != "" && s.store.Has(packageReleaseNotes, workspaceElements, kindReleaseNotes) {
			version = workspaceElements
			reason = fmt.Sprintf(
				"release notes match the resolved elements@%s version", workspaceElements)
			level = "match"
			source = "elements@" + workspaceElements
		} else {
			reason = "latest bundled release notes; the resolved elements version has no release notes"
			if workspaceElements != "" {
				reason = fmt.Sprintf(
					"elements@%s has no bundled release notes; using latest bundled release notes@%s",
					workspaceElements, version)
			}
			level = "fallback"
		}
	}
	return version, reason, level, source
}

// resolvedWorkspaceElements returns the workspace elements version even when
// resolutionFrom rejected it because the binary does not bundle that version.
// An ambiguous result has no workspace version.
func resolvedWorkspaceElements(resolved ResolvedVersion, result resolve.Result) string {
	if resolved.Elements != "" {
		return resolved.Elements
	}
	if result.Ambiguity == nil {
		return result.Elements
	}
	return ""
}

// sameMajor compares the major of two version strings, accepting an optional
// v prefix on either side.
func sameMajor(left, right string) bool {
	leftCanonical := semver.Canonical("v" + strings.TrimPrefix(left, "v"))
	rightCanonical := semver.Canonical("v" + strings.TrimPrefix(right, "v"))
	return leftCanonical != "" && rightCanonical != "" &&
		semver.Major(leftCanonical) == semver.Major(rightCanonical)
}

// trimPackagesResource enforces the summary budget without ever failing the
// read: drop candidates, then ambiguity candidates, then collapse version
// lists, then drop warnings and ambiguity.
func trimPackagesResource(service *Service, payload *PackagesResourcePayload) {
	budget := defaultResponseBudget
	if service != nil {
		budget = service.responseBudget(false)
	}
	// Warnings are canonical at the top level; never measure or emit the
	// nested copy.
	payload.Resolved.Warnings = nil
	payload.Candidates = boundedCandidates(payload.Candidates, maxOutputCandidates)
	payload.Ambiguity = boundedAmbiguity(payload.Ambiguity)
	if estimatedResponseBytes(*payload) <= budget {
		return
	}
	payload.Candidates = nil
	payload.Truncated = true
	if estimatedResponseBytes(*payload) <= budget {
		return
	}
	if payload.Ambiguity != nil {
		payload.Ambiguity.Candidates = nil
		payload.Truncated = true
	}
	if estimatedResponseBytes(*payload) <= budget {
		return
	}
	for index := range payload.Packages {
		if len(payload.Packages[index].Versions) > 0 {
			payload.Packages[index].Versions = nil
			payload.Truncated = true
		}
	}
	if estimatedResponseBytes(*payload) <= budget {
		return
	}
	payload.Warnings = nil
	if estimatedResponseBytes(*payload) <= budget {
		return
	}
	payload.Ambiguity = nil
	payload.Truncated = true
	if estimatedResponseBytes(*payload) <= budget {
		return
	}
	// Even the minimal four-summary shape can exceed a very small test budget.
	// Return a bounded error instead of an oversized payload.
	*payload = PackagesResourcePayload{
		Packages:  []PackageSummary{},
		Truncated: true,
		Error: &ToolError{
			Code:    "response_too_large",
			Message: "the package index exceeds the response budget",
		},
	}
}
