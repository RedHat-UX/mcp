// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/resolve"
)

func readPackagesResource(t *testing.T, session *mcp.ClientSession) PackagesResourcePayload {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: packagesResourceURI})
	if err != nil {
		t.Fatalf("ReadResource(%q) error = %v", packagesResourceURI, err)
	}
	if len(result.Contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(result.Contents))
	}
	if result.Contents[0].MIMEType != "application/json" {
		t.Errorf("MIMEType = %q, want application/json", result.Contents[0].MIMEType)
	}
	var payload PackagesResourcePayload
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &payload); err != nil {
		t.Fatalf("packages payload is not JSON: %v\n%s", err, result.Contents[0].Text)
	}
	return payload
}

func packageSummaries(payload PackagesResourcePayload) map[string]PackageSummary {
	summaries := make(map[string]PackageSummary, len(payload.Packages))
	for _, summary := range payload.Packages {
		summaries[summary.Package] = summary
	}
	return summaries
}

func TestPackagesResourceSummarizesAllPackages(t *testing.T) {
	svc := fixtureService(t)
	svc.resolveWorkspace = func(resolve.Options) (resolve.Result, error) {
		return resolve.Result{
			Elements: "4.0.0",
			Source:   "package-lock.json",
			Candidates: []resolve.Candidate{{
				Version: "4.0.0", Source: "package-lock.json",
				Precedence: resolve.PrecedenceLockfile, Level: resolve.LevelLockfile,
			}},
			Warnings: []string{"workspace warning"},
		}, nil
	}
	session := connectServer(t, newResourceOnlyServer(t, svc))

	payload := readPackagesResource(t, session)
	if len(payload.Packages) != 4 {
		t.Fatalf("packages = %d, want 4: %+v", len(payload.Packages), payload.Packages)
	}
	if payload.Resolved.Elements != "4.0.0" {
		t.Errorf("resolved elements = %q, want 4.0.0", payload.Resolved.Elements)
	}
	if len(payload.Resolved.Warnings) != 0 {
		t.Errorf("resolved warnings = %v, want warnings only at the top level",
			payload.Resolved.Warnings)
	}
	if count := strings.Count(strings.Join(payload.Warnings, "\n"), "workspace warning"); count != 1 {
		t.Errorf("workspace warning appears %d times, want 1: %v", count, payload.Warnings)
	}

	summaries := packageSummaries(payload)
	elements, ok := summaries[packageElements]
	if !ok {
		t.Fatalf("packages is missing elements: %+v", payload.Packages)
	}
	if elements.Latest != "5.0.0" || elements.Count != 3 || elements.Resolved != "4.0.0" {
		t.Errorf("elements summary = %+v", elements)
	}
	if !slices.Contains(elements.Versions, "5.0.0") || !slices.Contains(elements.Versions, "1.0.0") {
		t.Errorf("elements versions = %v", elements.Versions)
	}
	if !slices.Contains(elements.Kinds, kindManifest) {
		t.Errorf("elements kinds = %v", elements.Kinds)
	}
	if elements.Level != resolve.LevelLockfile || elements.Source != "package-lock.json" {
		t.Errorf("elements level/source = %q/%q, want lockfile/package-lock.json",
			elements.Level, elements.Source)
	}
	if !strings.Contains(elements.Reason, "lockfile") ||
		!strings.Contains(elements.Reason, "package-lock.json") {
		t.Errorf("elements reason = %q", elements.Reason)
	}

	tokens, ok := summaries[packageTokens]
	if !ok {
		t.Fatalf("packages is missing tokens: %+v", payload.Packages)
	}
	if tokens.Latest != "3.2.0" || tokens.Count != 2 || tokens.Resolved != "3.2.0" {
		t.Errorf("tokens summary = %+v", tokens)
	}
	for _, want := range []string{kindTokens, kindThemes} {
		if !slices.Contains(tokens.Kinds, want) {
			t.Errorf("tokens kinds = %v, want %q", tokens.Kinds, want)
		}
	}

	docs, ok := summaries[packageDocs]
	if !ok {
		t.Fatalf("packages is missing docs: %+v", payload.Packages)
	}
	if docs.Latest != "v5.0.0" || docs.Count != 2 || docs.Resolved != "v4.2.2" {
		t.Errorf("docs summary = %+v", docs)
	}
	for _, want := range []string{kindDocs, kindPatterns, "doc"} {
		if !slices.Contains(docs.Kinds, want) {
			t.Errorf("docs kinds = %v, want %q", docs.Kinds, want)
		}
	}

	notes, ok := summaries[packageReleaseNotes]
	if !ok {
		t.Fatalf("packages is missing release-notes: %+v", payload.Packages)
	}
	if notes.Latest != "5.0.0" || notes.Count != 1 || notes.Resolved != "5.0.0" {
		t.Errorf("release-notes summary = %+v", notes)
	}
	if !slices.Contains(notes.Kinds, kindReleaseNotes) {
		t.Errorf("release-notes kinds = %v", notes.Kinds)
	}
	if notes.Reason == "" {
		t.Error("release-notes summary has no reason")
	}
}

func TestPackagesResourceWithoutBundledData(t *testing.T) {
	session := connectServer(t, newResourceOnlyServer(t, New(nil, data.ErrNoData)))
	payload := readPackagesResource(t, session)
	if payload.Error == nil || payload.Error.Code != "no_bundled_data" {
		t.Fatalf("error = %+v, want no_bundled_data", payload.Error)
	}
	if len(payload.Packages) != 0 {
		t.Errorf("packages = %+v, want none without bundled data", payload.Packages)
	}
}

func TestPackagesResourceReportsAmbiguity(t *testing.T) {
	svc := fixtureService(t)
	svc.resolveWorkspace = func(resolve.Options) (resolve.Result, error) {
		return resolve.Result{
			Candidates: []resolve.Candidate{
				{Version: "4.0.0", Source: "package-lock.json", Level: resolve.LevelLockfile},
				{Version: "5.0.0", Source: "package.json", Level: resolve.LevelPackageJSON},
			},
			Ambiguity: &resolve.Ambiguity{
				Level: resolve.LevelLockfile,
				Candidates: []resolve.Candidate{
					{Version: "4.0.0", Source: "package-lock.json", Level: resolve.LevelLockfile},
					{Version: "5.0.0", Source: "package.json", Level: resolve.LevelPackageJSON},
				},
				Majors: []int{4, 5},
			},
		}, nil
	}
	session := connectServer(t, newResourceOnlyServer(t, svc))

	payload := readPackagesResource(t, session)
	if payload.Ambiguity == nil {
		t.Fatal("ambiguity is not reported")
	}
	if !slices.Equal(payload.Ambiguity.Majors, []int{4, 5}) {
		t.Errorf("ambiguity majors = %v, want [4 5]", payload.Ambiguity.Majors)
	}
	if payload.Resolved.Elements != "" {
		t.Errorf("resolved elements = %q, want empty while ambiguous", payload.Resolved.Elements)
	}
	elements := packageSummaries(payload)[packageElements]
	if elements.Resolved != "5.0.0" {
		t.Errorf("elements summary resolved = %q, want the latest bundled version", elements.Resolved)
	}
	if !slices.ContainsFunc(payload.Warnings, func(w string) bool {
		return strings.Contains(w, "ambiguous")
	}) {
		t.Errorf("warnings do not mention ambiguity: %v", payload.Warnings)
	}
}

func TestPackagesResourceStaysWithinBudget(t *testing.T) {
	svc := fixtureService(t)
	svc.maxResponseBytes = 400
	session := connectServer(t, newResourceOnlyServer(t, svc))

	payload := readPackagesResource(t, session)
	if !payload.Truncated {
		t.Errorf("truncated = false, want true for a 400 byte budget: %+v", payload)
	}
	if len(payload.Packages) != 4 {
		t.Errorf("packages = %d, want all 4 summaries after budget trimming", len(payload.Packages))
	}
	for _, summary := range payload.Packages {
		if summary.Package == "" || summary.Count == 0 {
			t.Errorf("summary lost required fields: %+v", summary)
		}
	}
}
