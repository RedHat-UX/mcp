// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/resolve"
)

func TestParseResourceURI(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		want artifactResourceTarget
	}{
		{
			name: "elements empty path",
			uri:  "rhds://data/elements",
			want: artifactResourceTarget{Package: packageElements, Kind: kindManifest, MIMEType: "application/json"},
		},
		{
			name: "elements manifest alias",
			uri:  "rhds://data/elements/manifest",
			want: artifactResourceTarget{
				Package: packageElements, Kind: kindManifest, MIMEType: "application/json", Path: []string{"manifest"},
			},
		},
		{
			name: "tokens empty path",
			uri:  "rhds://data/tokens",
			want: artifactResourceTarget{Package: packageTokens, Kind: kindTokens, MIMEType: "application/json"},
		},
		{
			name: "tokens tokens alias",
			uri:  "rhds://data/tokens/tokens",
			want: artifactResourceTarget{
				Package: packageTokens, Kind: kindTokens, MIMEType: "application/json", Path: []string{"tokens"},
			},
		},
		{
			name: "tokens themes",
			uri:  "rhds://data/tokens/themes",
			want: artifactResourceTarget{
				Package: packageTokens, Kind: kindThemes, MIMEType: "application/json", Path: []string{"themes"},
			},
		},
		{
			name: "docs empty path",
			uri:  "rhds://data/docs",
			want: artifactResourceTarget{Package: packageDocs, Kind: kindDocs, MIMEType: "application/json"},
		},
		{
			name: "docs index alias",
			uri:  "rhds://data/docs/docs.json",
			want: artifactResourceTarget{
				Package: packageDocs, Kind: kindDocs, MIMEType: "application/json", Path: []string{"docs.json"},
			},
		},
		{
			name: "docs patterns",
			uri:  "rhds://data/docs/patterns.json",
			want: artifactResourceTarget{
				Package: packageDocs, Kind: kindPatterns, MIMEType: "application/json", Path: []string{"patterns.json"},
			},
		},
		{
			name: "docs multi segment page",
			uri:  "rhds://data/docs/elements/rh-button/docs/00-overview.md",
			want: artifactResourceTarget{
				Package:  packageDocs,
				Kind:     "doc:elements/rh-button/docs/00-overview.md",
				MIMEType: "text/markdown",
				Path:     []string{"elements", "rh-button", "docs", "00-overview.md"},
			},
		},
		{
			name: "docs escaped slash re-splits",
			uri:  "rhds://data/docs/get-started%2Fintroduction.md",
			want: artifactResourceTarget{
				Package:  packageDocs,
				Kind:     "doc:get-started/introduction.md",
				MIMEType: "text/markdown",
				Path:     []string{"get-started", "introduction.md"},
			},
		},
		{
			name: "release notes empty path",
			uri:  "rhds://data/release-notes",
			want: artifactResourceTarget{
				Package: packageReleaseNotes, Kind: kindReleaseNotes, MIMEType: "text/markdown",
			},
		},
		{
			name: "release notes alias",
			uri:  "rhds://data/release-notes/release-notes",
			want: artifactResourceTarget{
				Package: packageReleaseNotes, Kind: kindReleaseNotes, MIMEType: "text/markdown",
				Path: []string{"release-notes"},
			},
		},
		{
			name: "changelog",
			uri:  "rhds://data/release-notes/changelog",
			want: artifactResourceTarget{
				Package: packageReleaseNotes, Kind: kindChangelog, MIMEType: "text/markdown",
				Path: []string{"changelog"},
			},
		},
		{
			name: "explicit bare version",
			uri:  "rhds://data/elements?version=5.0.0",
			want: artifactResourceTarget{
				Package: packageElements, Kind: kindManifest, MIMEType: "application/json",
				Version: "5.0.0", VersionExplicit: true,
			},
		},
		{
			name: "explicit v prefixed version",
			uri:  "rhds://data/docs?version=v5.0.0",
			want: artifactResourceTarget{
				Package: packageDocs, Kind: kindDocs, MIMEType: "application/json",
				Version: "v5.0.0", VersionExplicit: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseResourceURI(tc.uri)
			if err != nil {
				t.Fatalf("parseResourceURI(%q) error = %v", tc.uri, err)
			}
			if got.Package != tc.want.Package || got.Kind != tc.want.Kind ||
				got.MIMEType != tc.want.MIMEType || got.Version != tc.want.Version ||
				got.VersionExplicit != tc.want.VersionExplicit || !slices.Equal(got.Path, tc.want.Path) {
				t.Errorf("parseResourceURI(%q) = %+v, want %+v", tc.uri, got, tc.want)
			}
		})
	}
}

func TestParseResourceURIRejectsInvalidURIs(t *testing.T) {
	invalid := []string{
		"https://data/elements",
		"rhds://elements/5.0.0/manifest",
		"rhds://data",
		"rhds://data/nope",
		"rhds://data/elements/",
		"rhds://data/elements//manifest",
		"rhds://data/docs/./page.md",
		"rhds://data/docs/../page.md",
		"rhds://data/docs/%2E%2E/page.md",
		"rhds://data/elements/bogus",
		"rhds://data/tokens/bogus",
		"rhds://data/tokens/themes/extra",
		"rhds://data/release-notes/5.0.0",
		"rhds://data/elements?foo=5.0.0",
		"rhds://data/elements?version=5.0.0&version=4.0.0",
		"rhds://data/elements?version=",
		"rhds://data/elements?",
		"rhds://data/elements?version",
		"rhds://data/elements#manifest",
	}
	for _, uri := range invalid {
		t.Run(uri, func(t *testing.T) {
			if got, err := parseResourceURI(uri); err == nil {
				t.Fatalf("parseResourceURI(%q) = %+v, want an error", uri, got)
			}
		})
	}
}

func TestArtifactKindFor(t *testing.T) {
	cases := []struct {
		pkg  string
		path []string
		kind string
		mime string
		ok   bool
	}{
		{packageElements, nil, kindManifest, "application/json", true},
		{packageElements, []string{"manifest"}, kindManifest, "application/json", true},
		{packageElements, []string{"modules"}, "", "", false},
		{packageTokens, nil, kindTokens, "application/json", true},
		{packageTokens, []string{"tokens"}, kindTokens, "application/json", true},
		{packageTokens, []string{"themes"}, kindThemes, "application/json", true},
		{packageTokens, []string{"nope"}, "", "", false},
		{packageDocs, nil, kindDocs, "application/json", true},
		{packageDocs, []string{"docs.json"}, kindDocs, "application/json", true},
		{packageDocs, []string{"patterns.json"}, kindPatterns, "application/json", true},
		{
			packageDocs, []string{"get-started", "introduction.md"},
			"doc:get-started/introduction.md", "text/markdown", true,
		},
		{packageReleaseNotes, nil, kindReleaseNotes, "text/markdown", true},
		{packageReleaseNotes, []string{"release-notes"}, kindReleaseNotes, "text/markdown", true},
		{packageReleaseNotes, []string{"changelog"}, kindChangelog, "text/markdown", true},
		{packageReleaseNotes, []string{"5.0.0"}, "", "", false},
		{"nope", nil, "", "", false},
	}
	for _, tc := range cases {
		kind, mime, ok := artifactKindFor(tc.pkg, tc.path)
		if kind != tc.kind || mime != tc.mime || ok != tc.ok {
			t.Errorf("artifactKindFor(%q, %v) = (%q, %q, %v), want (%q, %q, %v)",
				tc.pkg, tc.path, kind, mime, ok, tc.kind, tc.mime, tc.ok)
		}
	}
}

func newResourceOnlyServer(t *testing.T, svc *Service) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	RegisterResources(server, svc)
	return server
}

func TestStaticResourceListIsSmall(t *testing.T) {
	client := connectToolClient(t, fixtureService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	list, err := client.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(list.Resources) != 2 {
		t.Fatalf("resources = %d, want exactly 2 static resources: %+v",
			len(list.Resources), list.Resources)
	}
	uris := make([]string, 0, len(list.Resources))
	for _, resource := range list.Resources {
		uris = append(uris, resource.URI)
	}
	for _, want := range []string{migrationsResourceURI, packagesResourceURI} {
		if !slices.Contains(uris, want) {
			t.Errorf("resources are missing %q: %v", want, uris)
		}
	}
	if pattern := regexp.MustCompile(`\d+\.\d+\.\d+`); pattern.MatchString(strings.Join(uris, " ")) {
		t.Errorf("static resource URIs contain a version literal: %v", uris)
	}
	encoded, err := json.Marshal(list.Resources)
	if err != nil {
		t.Fatalf("marshaling resources: %v", err)
	}
	if len(encoded) >= 4*1024 {
		t.Errorf("resources/list is %d bytes, want under 4 KiB", len(encoded))
	}
}

func TestResourceTemplatesAreRegistered(t *testing.T) {
	session := connectServer(t, newResourceOnlyServer(t, fixtureService(t)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates() error = %v", err)
	}
	var templates []string
	for _, template := range result.ResourceTemplates {
		templates = append(templates, template.URITemplate)
	}
	want := []string{
		"rhds://data/elements{/path*}",
		"rhds://data/elements{/path*}?{+query}",
		"rhds://data/tokens{/path*}",
		"rhds://data/tokens{/path*}?{+query}",
		"rhds://data/docs{/path*}",
		"rhds://data/docs{/path*}?{+query}",
		"rhds://data/release-notes{/path*}",
		"rhds://data/release-notes{/path*}?{+query}",
	}
	if len(templates) != len(want) {
		t.Fatalf("templates = %v, want exactly %v", templates, want)
	}
	for _, uri := range want {
		if !slices.Contains(templates, uri) {
			t.Errorf("templates are missing %q: %v", uri, templates)
		}
	}
	for _, template := range templates {
		if strings.HasPrefix(template, "rhds://elements") {
			t.Errorf("generic template %q collides with the component authority", template)
		}
	}
}

func TestComponentSectionURIStillRoutesToComponentTemplate(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	svc := fixtureService(t)
	RegisterResources(server, svc)
	RegisterComponentResources(server, svc)
	session := connectServer(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhds://elements/5.0.0/components/rh-button/attributes",
	})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	var payload []json.RawMessage
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &payload); err != nil {
		t.Fatalf("component section payload is not a JSON array: %v\n%s", err, result.Contents[0].Text)
	}
	if len(payload) != 3 {
		t.Errorf("attributes = %d, want 3 from the component resource", len(payload))
	}
	if bytes.Contains([]byte(result.Contents[0].Text), []byte("schemaVersion")) {
		t.Error("component URI returned the elements manifest instead of the component section")
	}
}

func TestArtifactResourceParserErrorsReachTheClient(t *testing.T) {
	session := connectServer(t, newResourceOnlyServer(t, fixtureService(t)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cases := []struct {
		uri      string
		contains string
	}{
		// {+query} admits "&", so the query-bearing template routes and the
		// parser, not the router, produces the error.
		{"rhds://data/elements?version=5.0.0&foo=1", "foo"},
		{"rhds://data/elements?version=5.0.0&version=4.0.0", "repeated"},
		{"rhds://data/elements?version=", "empty"},
		{"rhds://data/elements?", "query is empty"},
		{"rhds://data/elements?version=5.0.0#manifest", "fragment"},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			_, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: tc.uri})
			if err == nil {
				t.Fatalf("ReadResource(%q) error = nil, want a parser error", tc.uri)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("error = %v, want it to mention %q", err, tc.contains)
			}
		})
	}
}

func TestArtifactResourceReadsWithExplicitVersion(t *testing.T) {
	session := connectServer(t, newResourceOnlyServer(t, fixtureService(t)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cases := []struct {
		uri      string
		mimeType string
		contains string
	}{
		{"rhds://data/elements?version=5.0.0", "application/json", `"schemaVersion"`},
		{"rhds://data/elements/manifest?version=5.0.0", "application/json", `"rh-button"`},
		{"rhds://data/tokens?version=3.2.0", "application/json", `"$description"`},
		{"rhds://data/tokens/tokens?version=3.2.0", "application/json", `"$description"`},
		{"rhds://data/tokens/themes?version=3.2.0", "application/json", `"light"`},
		{"rhds://data/docs?version=5.0.0", "application/json", "v5.0.0"},
		{"rhds://data/docs/docs.json?version=v5.0.0", "application/json", "v5.0.0"},
		{"rhds://data/docs/patterns.json?version=5.0.0", "application/json", "patterns/button"},
		{"rhds://data/docs/get-started/introduction.md?version=5.0.0", "text/markdown", "# Get started"},
		{
			"rhds://data/docs/elements%2Frh-button%2Fdocs%2F00-overview.md?version=v5.0.0",
			"text/markdown", "# Button",
		},
		{"rhds://data/release-notes?version=5.0.0", "text/markdown", "## 5.0.0"},
		{"rhds://data/release-notes/release-notes?version=5.0.0", "text/markdown", "## 5.0.0"},
		{"rhds://data/release-notes/changelog?version=latest", "text/markdown", "Major Changes"},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: tc.uri})
			if err != nil {
				t.Fatalf("ReadResource(%q) error = %v", tc.uri, err)
			}
			if len(result.Contents) != 1 {
				t.Fatalf("contents = %d, want 1", len(result.Contents))
			}
			contents := result.Contents[0]
			if contents.MIMEType != tc.mimeType {
				t.Errorf("MIMEType = %q, want %q", contents.MIMEType, tc.mimeType)
			}
			if !strings.Contains(contents.Text, tc.contains) {
				t.Errorf("payload does not contain %q:\n%s", tc.contains, contents.Text)
			}
		})
	}
}

func TestArtifactResourceReadsUseWorkspaceCascade(t *testing.T) {
	svc := fixtureService(t)
	var roots []string
	svc.resolveWorkspace = func(options resolve.Options) (resolve.Result, error) {
		roots = append(roots, options.Root)
		return resolve.Result{
			Elements: "1.0.0",
			Source:   resolve.LevelLockfile,
			Candidates: []resolve.Candidate{{
				Version: "1.0.0", Source: "package-lock.json",
				Precedence: resolve.PrecedenceLockfile, Level: resolve.LevelLockfile,
			}},
		}, nil
	}
	session := connectServer(t, newResourceOnlyServer(t, svc))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	read := func(uri, contains string) {
		t.Helper()
		result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("ReadResource(%q) error = %v", uri, err)
		}
		if !strings.Contains(result.Contents[0].Text, contains) {
			t.Errorf("%s payload does not contain %q:\n%s", uri, contains, result.Contents[0].Text)
		}
	}

	elementsText := readResourceText(t, session, "rhds://data/elements")
	if !strings.Contains(elementsText, `"rh-card"`) {
		t.Errorf("elements payload does not contain rh-card:\n%s", elementsText)
	}
	if strings.Contains(elementsText, `"rh-button"`) {
		t.Error("elements read without a version used the latest bundled manifest instead of the cascade")
	}
	tokensText := readResourceText(t, session, "rhds://data/tokens")
	if !strings.Contains(tokensText, `"$description"`) {
		t.Errorf("tokens payload does not contain a description:\n%s", tokensText)
	}
	if strings.Contains(tokensText, `"legacy"`) {
		t.Error("tokens read without a version used the latest bundled tokens instead of the cascade")
	}
	read("rhds://data/docs", "v5.0.0")
	read("rhds://data/release-notes", "## 5.0.0")
	read("rhds://data/release-notes/changelog", "Major Changes")
	if len(roots) == 0 {
		t.Fatal("resolveWorkspace was never called")
	}
	for _, root := range roots {
		if root == "" {
			t.Error("resolveWorkspace was called with an empty root")
		}
	}
}

func TestArtifactResourceAmbiguitySuggestsVersionQuery(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "rhds://data/elements"})
	if err == nil {
		t.Fatal("ReadResource() error = nil, want an ambiguity error")
	}
	if !strings.Contains(err.Error(), "?version=") {
		t.Errorf("ambiguity error does not suggest ?version=: %v", err)
	}
}

func TestArtifactResourceExplicitVersionBypassesAmbiguousWorkspace(t *testing.T) {
	svc := fixtureService(t)
	calls := 0
	svc.resolveWorkspace = func(resolve.Options) (resolve.Result, error) {
		calls++
		return resolve.Result{
			Ambiguity: &resolve.Ambiguity{
				Majors: []int{4, 5},
				Candidates: []resolve.Candidate{
					{Version: "4.0.0", Source: "package-lock.json", Level: resolve.LevelLockfile},
					{Version: "5.0.0", Source: "package.json", Level: resolve.LevelPackageJSON},
				},
			},
		}, nil
	}
	session := connectServer(t, newResourceOnlyServer(t, svc))

	text := readResourceText(t, session, "rhds://data/elements?version=5.0.0")
	if !strings.Contains(text, `"rh-button"`) {
		t.Errorf("explicit version payload does not contain the 5.0.0 manifest:\n%s", text)
	}
	if calls != 0 {
		t.Errorf("resolveWorkspace calls = %d, want 0 for an explicit version", calls)
	}
}

func TestArtifactResourceDocsCascadeUsesMajorMatch(t *testing.T) {
	svc := fixtureService(t)
	svc.resolveWorkspace = func(resolve.Options) (resolve.Result, error) {
		return resolve.Result{
			Elements: "4.0.0",
			Source:   resolve.LevelLockfile,
			Candidates: []resolve.Candidate{{
				Version: "4.0.0", Source: "package-lock.json",
				Precedence: resolve.PrecedenceLockfile, Level: resolve.LevelLockfile,
			}},
		}, nil
	}
	session := connectServer(t, newResourceOnlyServer(t, svc))

	text := readResourceText(t, session, "rhds://data/docs")
	if !strings.Contains(text, "Get started with RHDS 4") {
		t.Errorf("docs read did not use the v4.2.2 snapshot for elements@4.0.0:\n%s", text)
	}
}

func TestArtifactResourceInvalidVersionOrPathIsNotFound(t *testing.T) {
	session := connectServer(t, newResourceOnlyServer(t, fixtureService(t)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cases := []struct {
		uri      string
		contains string
	}{
		{"rhds://data/elements?version=9.9.9", ""},
		{"rhds://data/tokens?version=9.9.9", ""},
		{"rhds://data/docs?version=9.9.9", ""},
		{"rhds://data/tokens/themes?version=1.1.2", ""},
		{"rhds://data/elements/bogus", "unsupported package or path"},
		{"rhds://data/tokens/bogus", "unsupported package or path"},
		{"rhds://data/release-notes/bogus", "unsupported package or path"},
		{"rhds://data/docs/does/not/exist.md?version=5.0.0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			_, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: tc.uri})
			if err == nil {
				t.Fatalf("ReadResource(%q) error = nil, want not found", tc.uri)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("error = %v, want it to mention %q", err, tc.contains)
			}
		})
	}
}

func TestResourceResolutionCache(t *testing.T) {
	t.Run("hit", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		svc := fixtureService(t)
		calls := 0
		svc.resolveWorkspace = func(resolve.Options) (resolve.Result, error) {
			calls++
			return resolve.Result{Elements: "4.0.0", Source: resolve.LevelLockfile}, nil
		}
		for range 2 {
			resolved, failure := svc.resourceResolutionAtRoot(root, nil)
			if failure != nil {
				t.Fatalf("resourceResolutionAtRoot() failure = %+v", failure)
			}
			if resolved.Resolved.Elements != "4.0.0" {
				t.Errorf("elements = %q, want 4.0.0", resolved.Resolved.Elements)
			}
		}
		if calls != 1 {
			t.Errorf("resolveWorkspace calls = %d, want 1 for a cache hit", calls)
		}
	})

	t.Run("lockfile change invalidates", func(t *testing.T) {
		root := t.TempDir()
		lockfile := filepath.Join(root, "package-lock.json")
		if err := os.WriteFile(lockfile, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		svc := fixtureService(t)
		calls := 0
		svc.resolveWorkspace = func(resolve.Options) (resolve.Result, error) {
			calls++
			return resolve.Result{Elements: "4.0.0", Source: resolve.LevelLockfile}, nil
		}
		if _, failure := svc.resourceResolutionAtRoot(root, nil); failure != nil {
			t.Fatalf("first resolution failure = %+v", failure)
		}
		if err := os.WriteFile(lockfile, []byte(`{"changed":true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, failure := svc.resourceResolutionAtRoot(root, nil); failure != nil {
			t.Fatalf("second resolution failure = %+v", failure)
		}
		if calls != 2 {
			t.Errorf("resolveWorkspace calls = %d, want 2 after a lockfile change", calls)
		}
	})

	t.Run("no lockfile bypasses the cache", func(t *testing.T) {
		root := t.TempDir()
		svc := fixtureService(t)
		calls := 0
		svc.resolveWorkspace = func(resolve.Options) (resolve.Result, error) {
			calls++
			return resolve.Result{Elements: "4.0.0", Source: resolve.LevelLockfile}, nil
		}
		for range 2 {
			if _, failure := svc.resourceResolutionAtRoot(root, nil); failure != nil {
				t.Fatalf("resourceResolutionAtRoot() failure = %+v", failure)
			}
		}
		if calls != 2 {
			t.Errorf("resolveWorkspace calls = %d, want 2 with no lockfile", calls)
		}
	})
}

func TestResourceResolutionWithoutData(t *testing.T) {
	svc := New(nil, data.ErrNoData)
	_, failure := svc.resourceResolutionAtRoot(t.TempDir(), nil)
	if failure == nil || failure.Code != "no_bundled_data" {
		t.Fatalf("failure = %+v, want no_bundled_data", failure)
	}
}
