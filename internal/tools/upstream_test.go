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
		ProtocolVersion: "2025-11-25",
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
		DisableTools: []string{"validate_html", "search_tokens"},
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

func TestMountUpstreamsSkipsCEMWithoutManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	if err := MountUpstreams(server, MountOptions{}); err != nil {
		t.Fatalf("MountUpstreams() error = %v, want nil when cem is skipped", err)
	}

	names := listToolNames(t, connectServer(t, server))
	for _, name := range cemToolNames {
		if slices.Contains(names, name) {
			t.Errorf("tools/list contains cem tool %q without a workspace manifest", name)
		}
	}
	for _, name := range asimonimToolNames {
		if !slices.Contains(names, name) {
			t.Errorf("tools/list is missing asimonim tool %q after the cem skip: %v", name, names)
		}
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
	return contentText(result)
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
