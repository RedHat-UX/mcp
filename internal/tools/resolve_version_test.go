// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/resolve"
)

func writeWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating directory for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return root
}

func TestResolveVersionReportsCandidatesAndResolvedBlock(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"package-lock.json": `{"lockfileVersion":3,"packages":{"node_modules/@rhds/elements":{"version":"5.0.0"}}}`,
		"package.json":      `{"dependencies":{"@rhds/elements":"^4.0.0"}}`,
	})
	out := fixtureService(t).ResolveVersionInWorkspace(root, ResolveVersionInput{})
	if out.Error != nil {
		t.Fatalf("ResolveVersion() error = %+v", out.Error)
	}
	if out.Resolved.Elements != "5.0.0" {
		t.Errorf("elements = %q, want 5.0.0", out.Resolved.Elements)
	}
	if out.Resolved.Tokens != "3.2.0" {
		t.Errorf("tokens = %q, want 3.2.0", out.Resolved.Tokens)
	}
	if out.Resolved.Docs != "v5.0.0" {
		t.Errorf("docs = %q, want v5.0.0", out.Resolved.Docs)
	}
	if out.Resolved.Source != "package-lock.json" {
		t.Errorf("source = %q", out.Resolved.Source)
	}
	if len(out.Candidates) < 2 {
		t.Fatalf("candidates = %+v, want lockfile and package.json evidence", out.Candidates)
	}
	var sawLockfile, sawPackageJSON bool
	for _, candidate := range out.Candidates {
		switch candidate.Level {
		case resolve.LevelLockfile:
			sawLockfile = candidate.Source == "package-lock.json" && candidate.Version == "5.0.0"
		case resolve.LevelPackageJSON:
			sawPackageJSON = candidate.Source == "package.json" && candidate.Version == "4.0.0"
		}
	}
	if !sawLockfile || !sawPackageJSON {
		t.Errorf("candidates = %+v", out.Candidates)
	}
	// The lower-precedence major disagreement is reported, not silently lost.
	joined := strings.Join(out.Resolved.Warnings, " ")
	if !strings.Contains(joined, "ignored 4.0.0") {
		t.Errorf("warnings = %v", out.Resolved.Warnings)
	}
}

func TestResolveVersionAmbiguousReturnsStructuredError(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"package-lock.json": `{"lockfileVersion":3,"packages":{
			"node_modules/@rhds/elements":{"version":"4.0.0"},
			"packages/app/node_modules/@rhds/elements":{"version":"5.0.0"}
		}}`,
	})
	out := fixtureService(t).ResolveVersionInWorkspace(root, ResolveVersionInput{})
	if out.Error == nil {
		t.Fatal("error = nil, want ambiguous_version")
	}
	if out.Error.Code != "ambiguous_version" {
		t.Errorf("error code = %q", out.Error.Code)
	}
	if out.Ambiguity == nil || len(out.Ambiguity.Candidates) != 2 {
		t.Errorf("ambiguity = %+v", out.Ambiguity)
	}
	if len(out.Candidates) != 2 {
		t.Errorf("candidates = %+v", out.Candidates)
	}
	details, ok := out.Error.Details.(map[string]any)
	if !ok {
		t.Fatalf("error details = %T, want a map", out.Error.Details)
	}
	if _, ok := details["candidates"]; !ok {
		t.Errorf("error details = %+v, want candidates", details)
	}
	if _, ok := details["configOptions"]; !ok {
		t.Errorf("error details = %+v, want config options", details)
	}
}

func TestResolveVersionExplicitSkipsWorkspace(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"package-lock.json": `{"lockfileVersion":3,"packages":{"node_modules/@rhds/elements":{"version":"5.0.0"}}}`,
	})
	out := fixtureService(t).ResolveVersionInWorkspace(root, ResolveVersionInput{Version: "4.0.0"})
	if out.Error != nil {
		t.Fatalf("ResolveVersion() error = %+v", out.Error)
	}
	if out.Resolved.Elements != "4.0.0" {
		t.Errorf("elements = %q", out.Resolved.Elements)
	}
	if out.Resolved.Docs != "v4.2.2" {
		t.Errorf("docs = %q, want v4.2.2", out.Resolved.Docs)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].Level != resolve.LevelExplicit {
		t.Errorf("candidates = %+v, want only the explicit argument", out.Candidates)
	}
}

func TestResolveVersionNoEvidenceUsesBundled(t *testing.T) {
	root := t.TempDir()
	out := fixtureService(t).ResolveVersionInWorkspace(root, ResolveVersionInput{})
	if out.Error != nil {
		t.Fatalf("ResolveVersion() error = %+v", out.Error)
	}
	if out.Resolved.Elements != "5.0.0" || out.Resolved.Source != "bundled" {
		t.Errorf("resolved = %+v", out.Resolved)
	}
	if !strings.Contains(strings.Join(out.Resolved.Warnings, " "), "latest bundled") {
		t.Errorf("warnings = %v", out.Resolved.Warnings)
	}
}

func TestResolveVersionUnbundledExplicitIsReported(t *testing.T) {
	out := fixtureService(t).ResolveVersion(context.Background(), ResolveVersionInput{Version: "9.9.9"})
	if out.Error != nil {
		t.Fatalf("ResolveVersion() error = %+v", out.Error)
	}
	if out.Resolved.Elements != "9.9.9" {
		t.Errorf("elements = %q", out.Resolved.Elements)
	}
	if !strings.Contains(strings.Join(out.Resolved.Warnings, " "), "not bundled") {
		t.Errorf("warnings = %v", out.Resolved.Warnings)
	}
}

func TestResolveVersionUnknownTokensVersion(t *testing.T) {
	out := fixtureService(t).ResolveVersion(context.Background(), ResolveVersionInput{
		Version:       "5.0.0",
		TokensVersion: "9.9.9",
	})
	if out.Error == nil || out.Error.Code != "artifact_not_found" {
		t.Fatalf("error = %+v, want artifact_not_found", out.Error)
	}
}

func TestResolveVersionJSONIncludesCandidates(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"package.json": `{"dependencies":{"@rhds/elements":"^5.0.0"}}`,
	})
	out := fixtureService(t).ResolveVersionInWorkspace(root, ResolveVersionInput{})
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Candidates []struct {
			Source     string `json:"source"`
			Precedence int    `json:"precedence"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Candidates) != 1 {
		t.Fatalf("candidates = %+v", decoded.Candidates)
	}
	if decoded.Candidates[0].Source != "package.json" || decoded.Candidates[0].Precedence == 0 {
		t.Errorf("candidate = %+v", decoded.Candidates[0])
	}
}

func TestPrepareResolutionMapsNoBundledElements(t *testing.T) {
	index := `{"schemaVersion":1,"artifacts":[{"package":"tokens","version":"3.2.0","kind":"tokens","path":"tokens.json"}]}`
	store, err := data.Open(fstest.MapFS{
		"index.json": {Data: []byte(index)},
	})
	if err != nil {
		t.Fatalf("data.Open() error = %v", err)
	}
	service := New(store, nil)
	_, required, failure := service.prepareResolution(context.Background(), &callEnv{}, "", "", true)
	if required != nil {
		t.Fatalf("required = %+v, want a failure", required)
	}
	if failure == nil || failure.Code != "artifact_not_found" {
		t.Fatalf("failure = %+v, want artifact_not_found", failure)
	}
}

func TestResolveVersionBoundsLargeAmbiguity(t *testing.T) {
	var packages strings.Builder
	for i := 0; i < 60; i++ {
		if i > 0 {
			packages.WriteByte(',')
		}
		fmt.Fprintf(&packages,
			`"node_modules/a%d/node_modules/@rhds/elements":{"version":"4.0.%d"},`+
				`"node_modules/b%d/node_modules/@rhds/elements":{"version":"5.0.%d"}`,
			i, i, i, i)
	}
	root := writeWorkspace(t, map[string]string{
		"package-lock.json": `{"lockfileVersion":3,"packages":{` + packages.String() + `}}`,
	})
	out := fixtureService(t).ResolveVersionInWorkspace(root, ResolveVersionInput{})
	if out.Error == nil || out.Error.Code != "ambiguous_version" {
		t.Fatalf("error = %+v, want ambiguous_version", out.Error)
	}
	if size := estimatedResponseBytes(out); size > defaultResponseBudget {
		t.Fatalf("estimated response = %d bytes, want <= %d", size, defaultResponseBudget)
	}
	if out.Ambiguity == nil {
		t.Fatal("ambiguity = nil")
	}
	if len(out.Ambiguity.Candidates) > 5 {
		t.Errorf("ambiguity candidates = %d, want at most 5", len(out.Ambiguity.Candidates))
	}
	details, ok := out.Error.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %T, want a map", out.Error.Details)
	}
	if candidates, ok := details["candidates"].([]string); !ok || len(candidates) > 5 {
		t.Errorf("details candidates = %#v, want at most 5 short labels", details["candidates"])
	}
}

func TestErrorPathsAreBounded(t *testing.T) {
	service := fixtureService(t)
	huge := strings.Repeat("x", 200_000)

	component := service.GetComponentAPI(context.Background(), GetComponentAPIInput{TagName: huge})
	if component.Error == nil {
		t.Fatal("expected an error for the huge tag name")
	}
	if size := estimatedResponseBytes(component); size > defaultResponseBudget {
		t.Errorf("component error response = %d bytes, want <= %d", size, defaultResponseBudget)
	}

	section := service.GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName:  "rh-button",
		Sections: []string{huge},
	})
	if section.Error == nil || section.Error.Code != "invalid_argument" {
		t.Fatalf("section error = %+v", section.Error)
	}
	if size := estimatedResponseBytes(section); size > defaultResponseBudget {
		t.Errorf("section error response = %d bytes, want <= %d", size, defaultResponseBudget)
	}

	tokens := service.QueryTokens(context.Background(), QueryTokensInput{
		Token:   huge,
		Version: "3.2.0",
	})
	if tokens.Error == nil {
		t.Fatal("expected an error for the huge token name")
	}
	if size := estimatedResponseBytes(tokens); size > defaultResponseBudget {
		t.Errorf("token error response = %d bytes, want <= %d", size, defaultResponseBudget)
	}

	resolved := service.ResolveVersion(context.Background(), ResolveVersionInput{
		Version:       "5.0.0",
		TokensVersion: huge,
	})
	if resolved.Error == nil {
		t.Fatal("expected an error for the huge tokens version")
	}
	if size := estimatedResponseBytes(resolved); size > defaultResponseBudget {
		t.Errorf("resolve error response = %d bytes, want <= %d", size, defaultResponseBudget)
	}

	if _, componentEnv := service.getComponentAPIWithEnv(context.Background(), nil, GetComponentAPIInput{
		TagName: huge,
	}); estimatedResponseBytes(componentEnv) > defaultResponseBudget {
		t.Errorf("component env error response = %d bytes, want <= %d",
			estimatedResponseBytes(componentEnv), defaultResponseBudget)
	}
	if _, listEnv := service.listComponentsWithEnv(context.Background(), nil, ListComponentsInput{
		Version: huge,
	}); estimatedResponseBytes(listEnv) > defaultResponseBudget {
		t.Errorf("list env error response = %d bytes, want <= %d",
			estimatedResponseBytes(listEnv), defaultResponseBudget)
	}
	if _, searchEnv := service.searchWithEnv(context.Background(), nil, SearchInput{
		Query:   "button",
		Version: huge,
	}); estimatedResponseBytes(searchEnv) > defaultResponseBudget {
		t.Errorf("search env error response = %d bytes, want <= %d",
			estimatedResponseBytes(searchEnv), defaultResponseBudget)
	}
}

func TestResolveVersionBoundsHugeLockfileVersion(t *testing.T) {
	huge := strings.Repeat("9", 1_000_000)
	root := writeWorkspace(t, map[string]string{
		"package-lock.json": `{"lockfileVersion":3,"packages":{"node_modules/@rhds/elements":{"version":"` + huge + `"}}}`,
	})
	out := fixtureService(t).ResolveVersionInWorkspace(root, ResolveVersionInput{})
	if out.Error != nil {
		t.Fatalf("ResolveVersion() error = %+v", out.Error)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if size := estimatedResponseBytes(out); size > defaultResponseBudget {
		t.Fatalf("estimated response = %d bytes, want <= %d (raw %d)", size, defaultResponseBudget, len(encoded))
	}
	for _, candidate := range out.Candidates {
		if len(candidate.Version) > maxCandidateRunes+3 {
			t.Errorf("candidate version was not truncated: %d bytes", len(candidate.Version))
		}
		if len(candidate.Spec) > maxCandidateRunes+3 {
			t.Errorf("candidate spec was not truncated: %d bytes", len(candidate.Spec))
		}
	}
}

func TestResolveVersionCapsWarningCount(t *testing.T) {
	files := map[string]string{
		"package.json": `{"private":true,"workspaces":["packages/*"]}`,
	}
	for i := 0; i < 40; i++ {
		files[fmt.Sprintf("packages/pkg%02d/package.json", i)] =
			fmt.Sprintf(`{"dependencies":{"@rhds/elements":"file:../missing-%d"}}`, i)
	}
	root := writeWorkspace(t, files)
	out := fixtureService(t).ResolveVersionInWorkspace(root, ResolveVersionInput{})
	if out.Error != nil {
		t.Fatalf("ResolveVersion() error = %+v", out.Error)
	}
	if len(out.Resolved.Warnings) > maxResponseWarnings+1 {
		t.Errorf("warnings = %d, want at most %d", len(out.Resolved.Warnings), maxResponseWarnings+1)
	}
	if estimatedResponseBytes(out) > defaultResponseBudget {
		t.Errorf("estimated response = %d, want <= %d",
			estimatedResponseBytes(out), defaultResponseBudget)
	}
}
