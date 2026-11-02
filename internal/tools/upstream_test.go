// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 11 requires a roots/elicitation protocol matrix; the SDK marks roots deprecated but keeps it functional.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
)

// cemToolNames are the tools cem registers on a host server.
var cemToolNames = []string{
	"validate_html",
	"generate_html",
	"generate_config",
	"validate_config",
}

// asimonimToolNames are the tools asimonim registers on a host server.
var asimonimToolNames = []string{
	"validate_tokens",
	"search_tokens",
	"convert_tokens",
}

// projectWithManifest creates a directory that cem can use as a workspace.
func projectWithManifest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	manifest, err := os.ReadFile(filepath.Join("testdata", "elements", "custom-elements.json"))
	if err != nil {
		t.Fatalf("reading fixture manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "custom-elements.json"), manifest, 0o644); err != nil {
		t.Fatalf("writing fixture manifest: %v", err)
	}
	return root
}

// consumerPackageManifest is a minimal manifest with one custom element. It is
// installed under node_modules to model a design-system consumer.
const consumerPackageManifest = `{
  "schemaVersion": "2.1.1",
  "modules": [
    {
      "kind": "javascript-module",
      "path": "fixture-element.js",
      "declarations": [
        {
          "kind": "class",
          "name": "FixtureElement",
          "tagName": "fixture-element",
          "customElement": true,
          "description": "An element shipped by a dependency."
        }
      ],
      "exports": [
        {
          "kind": "custom-element-definition",
          "name": "fixture-element",
          "declaration": { "name": "FixtureElement", "module": "fixture-element.js" }
        }
      ]
    }
  ]
}`

// consumerProjectWithDependency creates a consumer workspace whose only custom
// elements manifest belongs to a package under node_modules. It deliberately
// has no root custom-elements.json, matching a project that depends on a
// design system rather than authoring one.
func consumerProjectWithDependency(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	rootPackage := `{"name":"consumer","version":"1.0.0","dependencies":{"fixture-elements":"^1.0.0"}}`
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(rootPackage), 0o644); err != nil {
		t.Fatalf("writing consumer package.json: %v", err)
	}

	pkgDir := filepath.Join(root, "node_modules", "fixture-elements")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("creating dependency directory: %v", err)
	}
	dependencyPackage := `{"name":"fixture-elements","version":"1.0.0","customElements":"custom-elements.json"}`
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(dependencyPackage), 0o644); err != nil {
		t.Fatalf("writing dependency package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "custom-elements.json"), []byte(consumerPackageManifest), 0o644); err != nil {
		t.Fatalf("writing dependency manifest: %v", err)
	}
	return root
}

// readResourceText reads uri and concatenates its text contents.
func readResourceText(t *testing.T, session *mcp.ClientSession, uri string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("ReadResource(%q) error = %v", uri, err)
	}
	var builder strings.Builder
	for _, content := range result.Contents {
		builder.WriteString(content.Text)
	}
	return builder.String()
}

// newMountedServer registers the RHDS tools and mounts the upstream surfaces.
func newMountedServer(t *testing.T, opts MountOptions) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	Register(server, New(nil, data.ErrNoData))
	if err := MountUpstreams(server, opts); err != nil {
		t.Fatalf("MountUpstreams() error = %v", err)
	}
	return server
}

func connectServer(t *testing.T, server *mcp.Server, roots ...*mcp.Root) *mcp.ClientSession { //nolint:staticcheck // roots are required by PLAN section 8.1
	t.Helper()
	return connectServerWithProtocol(t, server, "2025-11-25", roots...)
}

// connectServerWithProtocol connects a roots-capable client that negotiates
// protocol explicitly. Revisions from 2026-07-28 on resolve roots through
// multi-round-trip requests rather than a direct roots/list request.
func connectServerWithProtocol(t *testing.T, server *mcp.Server, protocol string, roots ...*mcp.Root) *mcp.ClientSession { //nolint:staticcheck // roots are required by PLAN section 8.1
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	options := &mcp.ClientOptions{}
	if len(roots) > 0 {
		options.Capabilities = &mcp.ClientCapabilities{ //nolint:staticcheck // roots are required by PLAN section 8.1
			RootsV2: &mcp.RootCapabilities{}, //nolint:staticcheck // roots are required by PLAN section 8.1
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "upstream-test", Version: "0.0.0"}, options)
	if len(roots) > 0 {
		client.AddRoots(roots...) //nolint:staticcheck // roots are required by PLAN section 8.1
	}
	clientSession, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{
		ProtocolVersion: protocol,
	})
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func listToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestMountUpstreamsToolUnion(t *testing.T) {
	t.Chdir(projectWithManifest(t))
	session := connectServer(t, newMountedServer(t, MountOptions{}))

	names := listToolNames(t, session)
	want := []string{
		"rhds_resolve_version",
		"rhds_list_components",
		"rhds_get_component_api",
		"rhds_search",
		"rhds_query_tokens",
		"rhds_validate_markup",
	}
	want = append(want, cemToolNames...)
	want = append(want, asimonimToolNames...)
	for _, name := range want {
		if !slices.Contains(names, name) {
			t.Errorf("tools/list is missing %q: %v", name, names)
		}
	}

	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			t.Errorf("tool %q is registered more than once", name)
		}
		seen[name] = true
	}
}

func TestMountUpstreamsDisablesTools(t *testing.T) {
	t.Chdir(projectWithManifest(t))
	session := connectServer(t, newMountedServer(t, MountOptions{
		// The empty entry is a no-op, not an invalid selection.
		DisableTools: []string{"", "validate_html", "search_tokens"},
	}))

	names := listToolNames(t, session)
	for _, name := range []string{"validate_html", "search_tokens"} {
		if slices.Contains(names, name) {
			t.Errorf("tools/list still contains disabled tool %q", name)
		}
	}
	if !slices.Contains(names, "generate_html") {
		t.Errorf("tools/list lost an enabled cem tool: %v", names)
	}
	if !slices.Contains(names, "validate_tokens") {
		t.Errorf("tools/list lost an enabled asimonim tool: %v", names)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "validate_html",
		Arguments: map[string]any{"html": "<rh-button></rh-button>"},
	})
	if err == nil {
		t.Fatal("CallTool(validate_html) error = nil, want unknown-tool error")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("CallTool(validate_html) error = %v, want an unknown-tool error", err)
	}
}

func TestMountUpstreamsDisablesResourcesAndTemplates(t *testing.T) {
	t.Chdir(projectWithManifest(t))
	session := connectServer(t, newMountedServer(t, MountOptions{
		DisableResources: []string{"cem://elements"},
		DisableTemplates: []string{"cem://element/{tagName}/slots"},
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	for _, resource := range resources.Resources {
		if resource.URI == "cem://elements" {
			t.Error("resources/list still contains the disabled URI cem://elements")
		}
	}

	templates, err := session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates() error = %v", err)
	}
	for _, template := range templates.ResourceTemplates {
		if template.URITemplate == "cem://element/{tagName}/slots" {
			t.Error("resources/templates/list still contains the disabled template")
		}
	}

	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "cem://elements"}); err == nil {
		t.Error("ReadResource(cem://elements) error = nil, want an unknown-resource error")
	}
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "cem://element/rh-button/slots",
	}); err == nil {
		t.Error("ReadResource(cem://element/rh-button/slots) error = nil, want an unknown-resource error")
	}
}

// TestMountUpstreamsDisableListDoesNotRemoveHostTools guards the contract that
// MountOptions filters only the upstream surfaces. A host tool that happens to
// share a name with an upstream tool, or that merely appears in a stale deny
// list, must survive mounting.
func TestMountUpstreamsDisableListDoesNotRemoveHostTools(t *testing.T) {
	// An empty workspace: cem mounts, but its validate_html is disabled below,
	// so its surface cannot explain why a host tool with the same name exists.
	t.Chdir(t.TempDir())

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "validate_html",
		Description: "host-owned tool",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "host-owned"}},
		}, nil
	})

	err := MountUpstreams(server, MountOptions{DisableTools: []string{"validate_html"}})
	if err != nil {
		t.Fatalf("MountUpstreams() error = %v, want nil", err)
	}

	session := connectServer(t, server)
	names := listToolNames(t, session)
	if !slices.Contains(names, "validate_html") {
		t.Fatalf("tools/list lost the host tool named in the disable list: %v", names)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "validate_html",
		Arguments: map[string]any{"html": "<x-y></x-y>"},
	})
	if err != nil {
		t.Fatalf("CallTool(validate_html) error = %v", err)
	}
	if got := contentText(result); got != "host-owned" {
		t.Errorf("CallTool(validate_html) = %q, want the host handler's response", got)
	}
}

// TestMountUpstreamsDisableListRegistersNoTransientSurface asserts that an
// excluded name is never registered at all: the final surface omits it, calling
// it fails as unknown, and a client that connects after mounting observes no
// list-changed churn.
func TestMountUpstreamsDisableListRegistersNoTransientSurface(t *testing.T) {
	t.Chdir(projectWithManifest(t))
	server := newMountedServer(t, MountOptions{
		DisableTools:     []string{"validate_html"},
		DisableResources: []string{"cem://elements"},
		DisableTemplates: []string{"cem://element/{tagName}/slots"},
	})

	var toolChanges, resourceChanges atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "upstream-test", Version: "0.0.0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			toolChanges.Add(1)
		},
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) {
			resourceChanges.Add(1)
		},
	})
	clientSession, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{
		ProtocolVersion: "2025-11-25",
	})
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	if names := listToolNames(t, clientSession); slices.Contains(names, "validate_html") {
		t.Errorf("tools/list contains the excluded tool: %v", names)
	}
	if _, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "validate_html",
		Arguments: map[string]any{"html": "<rh-button></rh-button>"},
	}); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("CallTool(validate_html) error = %v, want an unknown-tool error", err)
	}

	// Give any delayed notification a chance to arrive; mounting happens
	// before the session exists, so there must be no churn to replay.
	time.Sleep(100 * time.Millisecond)
	if got := toolChanges.Load(); got != 0 {
		t.Errorf("client received %d tool list-changed notifications, want 0", got)
	}
	if got := resourceChanges.Load(); got != 0 {
		t.Errorf("client received %d resource list-changed notifications, want 0", got)
	}
}

// TestMountUpstreamsReplacesConflictingToolsByDefault pins the documented
// default conflict policy: a pre-existing tool with the same name is replaced
// by the upstream tool.
func TestMountUpstreamsReplacesConflictingToolsByDefault(t *testing.T) {
	t.Chdir(projectWithManifest(t))

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "validate_html",
		Description: "pre-existing tool",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "pre-existing"}},
		}, nil
	})

	if err := MountUpstreams(server, MountOptions{}); err != nil {
		t.Fatalf("MountUpstreams() error = %v", err)
	}

	session := connectServer(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "validate_html" && tool.Description == "pre-existing tool" {
			t.Error("tools/list still describes the pre-existing validate_html tool; the upstream tool did not replace it")
		}
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "validate_html",
		Arguments: map[string]any{"html": "<rh-button></rh-button>"},
	})
	if err != nil {
		t.Fatalf("CallTool(validate_html) error = %v", err)
	}
	if got := contentText(result); strings.Contains(got, "pre-existing") {
		t.Errorf("CallTool(validate_html) = %q, want the upstream handler's response", got)
	}
}

// TestMountUpstreamsMountsCEMForConsumerDependencies covers the design-system
// consumer case: the manifest belongs to a dependency under node_modules, and
// the consumer root has no custom-elements.json. cem discovers dependency
// manifests itself, so the host must mount it anyway.
func TestMountUpstreamsMountsCEMForConsumerDependencies(t *testing.T) {
	t.Chdir(consumerProjectWithDependency(t))
	session := connectServer(t, newMountedServer(t, MountOptions{}))

	names := listToolNames(t, session)
	for _, name := range cemToolNames {
		if !slices.Contains(names, name) {
			t.Errorf("tools/list is missing cem tool %q for a consumer dependency: %v", name, names)
		}
	}

	if got := readResourceText(t, session, "cem://element/fixture-element"); !strings.Contains(got, "fixture-element") {
		t.Errorf("ReadResource(cem://element/fixture-element) = %q, want the dependency element", got)
	}
}

// TestMountUpstreamsMountsCEMWithRootManifest pins the original behavior: a
// workspace that owns a root custom-elements.json still mounts cem.
func TestMountUpstreamsMountsCEMWithRootManifest(t *testing.T) {
	t.Chdir(projectWithManifest(t))
	session := connectServer(t, newMountedServer(t, MountOptions{}))

	names := listToolNames(t, session)
	for _, name := range cemToolNames {
		if !slices.Contains(names, name) {
			t.Errorf("tools/list is missing cem tool %q with a root manifest: %v", name, names)
		}
	}

	if got := readResourceText(t, session, "cem://element/rh-accordion"); !strings.Contains(got, "rh-accordion") {
		t.Errorf("ReadResource(cem://element/rh-accordion) = %q, want the root manifest element", got)
	}
}

// TestMountUpstreamsMountsCEMWithoutManifest pins the chosen behavior for a
// workspace with no package.json and no manifest. Constructing the workspace
// and registering cem both succeed, so cem mounts with an empty registry: the
// tools are listed, validate_html flags custom elements as unknown, and element
// lookups (both the generate_html tool and the element resources) report the
// element as missing, rather than the surface disappearing. This is
// intentional; the mount condition is workspace constructibility, not the
// presence of a manifest.
func TestMountUpstreamsMountsCEMWithoutManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	if err := MountUpstreams(server, MountOptions{}); err != nil {
		t.Fatalf("MountUpstreams() error = %v, want nil for a constructible empty workspace", err)
	}

	session := connectServer(t, server)
	names := listToolNames(t, session)
	for _, name := range cemToolNames {
		if !slices.Contains(names, name) {
			t.Errorf("tools/list is missing cem tool %q for an empty workspace: %v", name, names)
		}
	}
	for _, name := range asimonimToolNames {
		if !slices.Contains(names, name) {
			t.Errorf("tools/list is missing asimonim tool %q: %v", name, names)
		}
	}

	// The static resource reads from the empty registry; a specific element
	// is absent, so its templated read fails. Asserting the error rather than
	// its wording keeps the test from depending on cem's message text.
	readResourceText(t, session, "cem://elements")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	validated, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "validate_html",
		Arguments: map[string]any{"html": "<fixture-element></fixture-element>"},
	})
	if err != nil {
		t.Fatalf("CallTool(validate_html) error = %v", err)
	}
	if got := contentText(validated); !strings.Contains(got, "Unknown Element") {
		t.Errorf("CallTool(validate_html) = %q, want the element flagged as unknown", got)
	}

	generated, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "generate_html",
		Arguments: map[string]any{"tagName": "fixture-element"},
	})
	if err != nil {
		t.Fatalf("CallTool(generate_html) error = %v", err)
	}
	if got := contentText(generated); !strings.Contains(got, "not found") {
		t.Errorf("CallTool(generate_html) = %q, want the element reported missing", got)
	}

	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "cem://element/fixture-element"}); err == nil {
		t.Fatal("ReadResource(cem://element/fixture-element) error = nil, want an error on an empty registry")
	}
}

func TestMountUpstreamsWarnsWhenCEMWorkspaceIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("removing temp cwd: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	err := MountUpstreams(server, MountOptions{})
	var warning *MountWarning
	if !errors.As(err, &warning) {
		t.Fatalf("MountUpstreams() error = %v, want a *MountWarning", err)
	}
	if warning.Package != "cem" {
		t.Errorf("warning package = %q, want cem", warning.Package)
	}

	names := listToolNames(t, connectServer(t, server))
	for _, name := range cemToolNames {
		if slices.Contains(names, name) {
			t.Errorf("tools/list contains cem tool %q when the workspace is unavailable", name)
		}
	}
	for _, name := range asimonimToolNames {
		if !slices.Contains(names, name) {
			t.Errorf("tools/list is missing asimonim tool %q after the cem warning: %v", name, names)
		}
	}
}

// TestMountUpstreamsWarnsWhenCEMWorkspaceIsInvalid exercises mountCEM's
// workspace-construction failure directly, rather than the getwd failure in
// TestMountUpstreamsWarnsWhenCEMWorkspaceIsUnavailable. An unreadable
// package.json makes NewFileSystemWorkspaceContext fail, so cem is skipped with
// the advisory warning and asimonim still mounts.
func TestMountUpstreamsWarnsWhenCEMWorkspaceIsInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("writing invalid package.json: %v", err)
	}
	t.Chdir(dir)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	err := MountUpstreams(server, MountOptions{})
	var warning *MountWarning
	if !errors.As(err, &warning) {
		t.Fatalf("MountUpstreams() error = %v, want a *MountWarning", err)
	}
	if warning.Package != "cem" {
		t.Errorf("warning package = %q, want cem", warning.Package)
	}

	names := listToolNames(t, connectServer(t, server))
	for _, name := range cemToolNames {
		if slices.Contains(names, name) {
			t.Errorf("tools/list contains cem tool %q for an invalid workspace", name)
		}
	}
	for _, name := range asimonimToolNames {
		if !slices.Contains(names, name) {
			t.Errorf("tools/list is missing asimonim tool %q after the cem warning: %v", name, names)
		}
	}
}

func TestMountUpstreamsAsimonimResolvesConfigFromSessionRoots(t *testing.T) {
	rootA := tokenProject(t, "alpha", "#ff0000")
	rootB := tokenProject(t, "beta", "#0000ff")

	t.Chdir(t.TempDir())
	server := newMountedServer(t, MountOptions{})

	sessionA := connectServer(t, server, &mcp.Root{URI: fileURI(rootA)}) //nolint:staticcheck // roots are required by PLAN section 8.1
	sessionB := connectServer(t, server, &mcp.Root{URI: fileURI(rootB)}) //nolint:staticcheck // roots are required by PLAN section 8.1

	if got := searchTokens(t, sessionA, "alpha"); !strings.Contains(got, "alpha") {
		t.Errorf("client A search for alpha = %s, want its root's token", got)
	}
	if got := searchTokens(t, sessionA, "beta"); strings.Contains(got, "beta") {
		t.Errorf("client A search for beta = %s, want no token from client B's root", got)
	}
	if got := searchTokens(t, sessionB, "beta"); !strings.Contains(got, "beta") {
		t.Errorf("client B search for beta = %s, want its root's token", got)
	}
	if got := searchTokens(t, sessionB, "alpha"); strings.Contains(got, "alpha") {
		t.Errorf("client B search for alpha = %s, want no token from client A's root", got)
	}
}

// TestMountUpstreamsAsimonimResolvesRootsThroughMRTR covers protocol revision
// 2026-07-28, where the server may not issue a direct roots/list request. Each
// session's workspace must still come from its own roots through multi-round-trip
// requests, never from the cwd fallback.
func TestMountUpstreamsAsimonimResolvesRootsThroughMRTR(t *testing.T) {
	rootA := tokenProject(t, "alpha", "#ff0000")
	rootB := tokenProject(t, "beta", "#0000ff")

	// The cwd has no token config, so only the roots can resolve a workspace.
	t.Chdir(t.TempDir())
	server := newMountedServer(t, MountOptions{})

	sessionA := connectServerWithProtocol(t, server, "2026-07-28", &mcp.Root{URI: fileURI(rootA)}) //nolint:staticcheck // roots are required by PLAN section 8.1
	sessionB := connectServerWithProtocol(t, server, "2026-07-28", &mcp.Root{URI: fileURI(rootB)}) //nolint:staticcheck // roots are required by PLAN section 8.1

	result := searchTokensResult(t, sessionA, "alpha")
	if got := contentText(result); !strings.Contains(got, "alpha") {
		t.Errorf("client A search for alpha = %s, want its root's token", got)
	}
	if got := searchTokens(t, sessionA, "beta"); strings.Contains(got, "beta") {
		t.Errorf("client A search for beta = %s, want no token from client B's root", got)
	}
	if got := searchTokens(t, sessionB, "beta"); !strings.Contains(got, "beta") {
		t.Errorf("client B search for beta = %s, want its root's token", got)
	}
	if got := searchTokens(t, sessionB, "alpha"); strings.Contains(got, "alpha") {
		t.Errorf("client B search for alpha = %s, want no token from client A's root", got)
	}

	// The result must record where its workspace came from: the client root,
	// not the cwd fallback.
	raw, ok := result.GetMeta()[workspaceMetaKey]
	if !ok {
		t.Fatalf("search_tokens result is missing %q in _meta: %#v", workspaceMetaKey, result.GetMeta())
	}
	meta, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%q in _meta = %T, want a JSON object", workspaceMetaKey, raw)
	}
	if got := meta["source"]; got != "roots" {
		t.Errorf("workspace meta source = %v, want roots", got)
	}
	if got := meta["root"]; got != rootA {
		t.Errorf("workspace meta root = %v, want %q", got, rootA)
	}
	if warning, ok := meta["warning"]; ok {
		t.Errorf("workspace meta has warning %v, want none for resolved roots", warning)
	}
}

// workspaceMetaKey is the _meta key asimonim uses for workspace provenance.
const workspaceMetaKey = "asimonim/workspace"

// tokenProject writes an asimonim config and token file under root.
func tokenProject(t *testing.T, name, value string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".config"), 0o755); err != nil {
		t.Fatalf("creating config directory: %v", err)
	}
	tokens := map[string]any{
		name: map[string]any{"$type": "color", "$value": value},
	}
	tokensJSON, err := json.Marshal(tokens)
	if err != nil {
		t.Fatalf("marshaling tokens: %v", err)
	}
	tokensPath := filepath.Join(root, "tokens.json")
	if err := os.WriteFile(tokensPath, tokensJSON, 0o644); err != nil {
		t.Fatalf("writing tokens: %v", err)
	}
	config := "files:\n  - " + tokensPath + "\n"
	if err := os.WriteFile(filepath.Join(root, ".config", "design-tokens.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return root
}

func fileURI(path string) string {
	return "file://" + filepath.ToSlash(path)
}

func searchTokens(t *testing.T, session *mcp.ClientSession, query string) string {
	t.Helper()
	return contentText(searchTokensResult(t, session, query))
}

func searchTokensResult(t *testing.T, session *mcp.ClientSession, query string) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search_tokens",
		Arguments: map[string]any{"query": query},
	})
	if err != nil {
		t.Fatalf("CallTool(search_tokens, %q) error = %v", query, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(search_tokens, %q) IsError = true: %s", query, contentText(result))
	}
	return result
}

func contentText(result *mcp.CallToolResult) string {
	var builder strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}
