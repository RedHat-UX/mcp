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
		payload := buildPackagesResource(ctx, request, service)
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
) PackagesResourcePayload {
	payload := PackagesResourcePayload{Packages: []PackageSummary{}}
	if service == nil {
		payload.Error = toolErrorFrom(data.ErrNoData)
		return payload
	}
	if failure := service.dataFailure(); failure != nil {
		payload.Error = failure
		return payload
	}

	result, rootWarnings, failure := service.resourceCascade(ctx, request)
	if failure != nil {
		payload.Error = failure
		payload.Warnings = append(payload.Warnings, failure.Message)
	} else {
		payload.Candidates = result.Candidates
		payload.Ambiguity = result.Ambiguity
		payload.Warnings = append(payload.Warnings, rootWarnings...)
		payload.Warnings = append(payload.Warnings, result.Warnings...)
		if result.Ambiguity != nil {
			payload.Warnings = append(payload.Warnings, fmt.Sprintf(
				"workspace resolution is ambiguous across majors %v; pass ?version=<bundled-version> to pin a version",
				result.Ambiguity.Majors))
		} else if resolved, resolutionFailure := service.resolutionFrom(result, "", true); resolutionFailure == nil {
			payload.Resolved = resolved.Resolved
			payload.Candidates = resolved.Candidates
			payload.Ambiguity = resolved.Ambiguity
		} else {
			payload.Warnings = append(payload.Warnings, resolutionFailure.Message)
		}
	}

	payload.Packages = service.packageSummaries(payload.Resolved, result)
	payload.Warnings = sanitizeWarnings(payload.Warnings)
	return payload
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
// resource read semantics.
func (s *Service) packageResolution(
	pkg, latest string,
	resolved ResolvedVersion,
	result resolve.Result,
) (version, reason, level, source string) {
	version = latest
	switch pkg {
	case packageElements:
		if resolved.Elements != "" && s.store.Has(packageElements, resolved.Elements, kindManifest) {
			version = resolved.Elements
		}
		level = result.Source
		for _, candidate := range result.Candidates {
			if candidate.Level == result.Source {
				source = candidate.Source
				break
			}
		}
		if level != "" && source != "" {
			reason = fmt.Sprintf("%s evidence from %s", level, source)
		} else if level != "" {
			reason = level + " evidence"
		} else {
			reason = "latest bundled version"
		}
	case packageTokens:
		if resolved.Tokens != "" && s.store.Has(packageTokens, resolved.Tokens, kindTokens) {
			version = resolved.Tokens
		}
		if coupled, ok := s.store.Index().TokensForElements(resolved.Elements); ok {
			reason = fmt.Sprintf("tokens@%s is coupled to elements@%s", coupled, resolved.Elements)
			level = "coupling"
			source = "elements@" + resolved.Elements
		} else {
			reason = "latest bundled tokens version"
			if resolved.Elements != "" {
				reason = fmt.Sprintf(
					"latest bundled tokens version; elements@%s has no recorded coupling",
					resolved.Elements)
			}
			level = "bundled"
		}
	case packageDocs:
		if resolved.Docs != "" && s.store.Has(packageDocs, resolved.Docs, kindDocs) {
			version = resolved.Docs
		}
		if resolved.Elements != "" {
			reason = fmt.Sprintf(
				"newest bundled docs snapshot matching the elements@%s major",
				resolved.Elements)
			level = "major-match"
			source = "elements@" + resolved.Elements
		} else {
			reason = "latest bundled docs snapshot"
			level = "bundled"
		}
	case packageReleaseNotes:
		if resolved.Elements != "" && s.store.Has(packageReleaseNotes, resolved.Elements, kindReleaseNotes) {
			version = resolved.Elements
			reason = fmt.Sprintf(
				"release notes match the resolved elements@%s version", resolved.Elements)
			level = "match"
			source = "elements@" + resolved.Elements
		} else {
			reason = "latest bundled release notes; the resolved elements version has no release notes"
			level = "fallback"
		}
	}
	return version, reason, level, source
}

// trimPackagesResource enforces the summary budget without ever failing the
// read: drop candidates, then collapse version lists, then drop warnings.
func trimPackagesResource(service *Service, payload *PackagesResourcePayload) {
	budget := defaultResponseBudget
	if service != nil {
		budget = service.responseBudget(false)
	}
	payload.Candidates = boundedCandidates(payload.Candidates, maxOutputCandidates)
	if estimatedResponseBytes(*payload) <= budget {
		return
	}
	payload.Candidates = nil
	payload.Truncated = true
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
	payload.Truncated = true
}
