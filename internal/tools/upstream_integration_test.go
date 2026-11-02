// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 11 requires a roots/elicitation protocol matrix; the SDK marks roots deprecated but keeps it functional.

package tools

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	asimfs "bennypowers.dev/asimonim/fs"
	asimmcp "bennypowers.dev/asimonim/mcp"
	cemmcp "bennypowers.dev/cem/mcp"
	cemworkspace "bennypowers.dev/cem/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
)

// newMountedServerWithMount registers the RHDS tools and returns the upstream
// mount handle so tests can inspect and close its internal sessions.
func newMountedServerWithMount(t *testing.T, opts MountOptions) (*mcp.Server, *Mount) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	Register(server, New(nil, data.ErrNoData))
	mount, err := MountUpstreams(server, opts)
	if err != nil {
		t.Fatalf("MountUpstreams() error = %v", err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	return server, mount
}

// directCEMServer registers cem on a server without the proxy adapter.
func directCEMServer(t *testing.T, cwd string) *mcp.Server {
	t.Helper()
	workspace, err := cemworkspace.NewFileSystemWorkspaceContext(cwd)
	if err != nil {
		t.Fatalf("creating cem workspace: %v", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "direct-cem", Version: "0.0.0"}, nil)
	if _, err := cemmcp.Register(server, workspace, cemmcp.ServerConfig{}); err != nil {
		t.Fatalf("direct cem registration: %v", err)
	}
	return server
}

// directAsimonimServer registers asimonim on a server without the proxy adapter.
func directAsimonimServer(t *testing.T, cwd string) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "direct-asimonim", Version: "0.0.0"}, nil)
	if _, err := asimmcp.Register(server, asimfs.NewOSFileSystem(), nil, cwd); err != nil {
		t.Fatalf("direct asimonim registration: %v", err)
	}
	return server
}

func TestMountUpstreamsAddsProvenance(t *testing.T) {
	t.Chdir(projectWithManifest(t))
	server, _ := newMountedServerWithMount(t, MountOptions{})

	tools, resources, templates := observedSurface(t, server)
	for name, module := range map[string]string{
		"validate_html": cemModulePath,
		"search_tokens": asimonimModulePath,
	} {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tools/list is missing %q", name)
		}
		if got := provenanceVersion(t, tool.GetMeta()); got != bundledUpstreamVersion(module) {
			t.Errorf("%s provenance version = %q, want %q", name, got, bundledUpstreamVersion(module))
		}
	}

	for uri, source := range map[string]string{
		"cem://elements":    "cem",
		"asimonim://tokens": "asimonim",
	} {
		resource, ok := resources[uri]
		if !ok {
			t.Fatalf("resources/list is missing %q", uri)
		}
		module := cemModulePath
		if source == "asimonim" {
			module = asimonimModulePath
		}
		meta, ok := resource.GetMeta()[provenanceMetaKey].(map[string]any)
		if !ok {
			t.Fatalf("%s is missing provenance: %#v", uri, resource.GetMeta())
		}
		if got := meta["source"]; got != source {
			t.Errorf("%s provenance source = %v, want %s", uri, got, source)
		}
		if got := meta["version"]; got != bundledUpstreamVersion(module) {
			t.Errorf("%s provenance version = %v, want %s", uri, got, bundledUpstreamVersion(module))
		}
	}

	for templateURI := range map[string]struct{}{
		"cem://element/{tagName}/slots": {},
		"asimonim://tokens/{+source}":   {},
	} {
		template, ok := templates[templateURI]
		if !ok {
			t.Fatalf("resources/templates/list is missing %q", templateURI)
		}
		if _, ok := template.GetMeta()[provenanceMetaKey]; !ok {
			t.Errorf("%s is missing provenance: %#v", templateURI, template.GetMeta())
		}
	}
}

func provenanceVersion(t *testing.T, meta map[string]any) string {
	t.Helper()
	raw, ok := meta[provenanceMetaKey]
	if !ok {
		t.Fatalf("entry is missing %q: %#v", provenanceMetaKey, meta)
	}
	provenance, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("provenance = %T, want a JSON object", raw)
	}
	version, _ := provenance["version"].(string)
	return version
}

// callTool calls a tool and fails when the call returns a protocol error.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	return result
}

// callToolError calls a tool and returns its protocol error, if any.
func callToolError(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	return err
}

func TestProxiedCEMToolMatchesDirectRegistration(t *testing.T) {
	cwd := projectWithManifest(t)
	t.Chdir(cwd)

	direct := connectServer(t, directCEMServer(t, cwd))
	proxied := connectServer(t, newMountedServer(t, MountOptions{}))

	args := map[string]any{"html": `<rh-button>Save</rh-button>`}
	wantResult := callTool(t, direct, "validate_html", args)
	gotResult := callTool(t, proxied, "validate_html", args)
	if diff := jsonDiff(t, wantResult, gotResult); diff != "" {
		t.Errorf("proxied validate_html result differs from direct registration:\n%s", diff)
	}
}

func TestProxiedCEMResourceMatchesDirectRegistration(t *testing.T) {
	cwd := projectWithManifest(t)
	t.Chdir(cwd)

	direct := connectServer(t, directCEMServer(t, cwd))
	proxied := connectServer(t, newMountedServer(t, MountOptions{}))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	want, err := direct.ReadResource(ctx, &mcp.ReadResourceParams{URI: "cem://elements"})
	if err != nil {
		t.Fatalf("direct ReadResource(cem://elements) error = %v", err)
	}
	got, err := proxied.ReadResource(ctx, &mcp.ReadResourceParams{URI: "cem://elements"})
	if err != nil {
		t.Fatalf("proxied ReadResource(cem://elements) error = %v", err)
	}
	if diff := jsonDiff(t, want, got); diff != "" {
		t.Errorf("proxied cem resource differs from direct registration:\n%s", diff)
	}
}

func TestProxiedAsimonimToolMatchesDirectRegistration(t *testing.T) {
	root := tokenProject(t, "alpha", "#ff0000")
	t.Chdir(t.TempDir())

	direct := connectServer(t, directAsimonimServer(t, "."), &mcp.Root{URI: fileURI(root)})         //nolint:staticcheck // roots are required by PLAN section 8.1
	proxied := connectServer(t, newMountedServer(t, MountOptions{}), &mcp.Root{URI: fileURI(root)}) //nolint:staticcheck // roots are required by PLAN section 8.1

	want := searchTokensResult(t, direct, "alpha")
	got := searchTokensResult(t, proxied, "alpha")
	if diff := jsonDiff(t, want, got); diff != "" {
		t.Errorf("proxied search_tokens result differs from direct registration:\n%s", diff)
	}
}

func TestProxiedAsimonimResourceMatchesDirectRegistration(t *testing.T) {
	root := tokenProject(t, "alpha", "#ff0000")
	t.Chdir(t.TempDir())

	direct := connectServer(t, directAsimonimServer(t, "."), &mcp.Root{URI: fileURI(root)})         //nolint:staticcheck // roots are required by PLAN section 8.1
	proxied := connectServer(t, newMountedServer(t, MountOptions{}), &mcp.Root{URI: fileURI(root)}) //nolint:staticcheck // roots are required by PLAN section 8.1

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	want, err := direct.ReadResource(ctx, &mcp.ReadResourceParams{URI: "asimonim://tokens"})
	if err != nil {
		t.Fatalf("direct ReadResource(asimonim://tokens) error = %v", err)
	}
	got, err := proxied.ReadResource(ctx, &mcp.ReadResourceParams{URI: "asimonim://tokens"})
	if err != nil {
		t.Fatalf("proxied ReadResource(asimonim://tokens) error = %v", err)
	}
	if diff := jsonDiff(t, want, got); diff != "" {
		t.Errorf("proxied asimonim resource differs from direct registration:\n%s", diff)
	}
}

func TestProxiedCEMErrorMatchesDirectRegistration(t *testing.T) {
	cwd := projectWithManifest(t)
	t.Chdir(cwd)

	direct := connectServer(t, directCEMServer(t, cwd))
	proxied := connectServer(t, newMountedServer(t, MountOptions{}))

	args := map[string]any{"focus": "nonexistent"}
	wantErr := callToolError(t, direct, "generate_config", args)
	gotErr := callToolError(t, proxied, "generate_config", args)
	if wantErr == nil || gotErr == nil {
		t.Fatalf("generate_config error mismatch: direct=%v proxied=%v", wantErr, gotErr)
	}
	if wantErr.Error() != gotErr.Error() {
		t.Errorf("error text mismatch:\ndirect: %s\nproxied: %s", wantErr, gotErr)
	}
}

func TestProxiedAsimonimToolErrorMatchesDirectRegistration(t *testing.T) {
	root := tokenProject(t, "alpha", "#ff0000")
	t.Chdir(t.TempDir())

	direct := connectServer(t, directAsimonimServer(t, "."), &mcp.Root{URI: fileURI(root)})         //nolint:staticcheck // roots are required by PLAN section 8.1
	proxied := connectServer(t, newMountedServer(t, MountOptions{}), &mcp.Root{URI: fileURI(root)}) //nolint:staticcheck // roots are required by PLAN section 8.1

	args := map[string]any{"query": "[", "regex": true}
	want := callTool(t, direct, "search_tokens", args)
	got := callTool(t, proxied, "search_tokens", args)
	if !want.IsError || !got.IsError {
		t.Fatalf("search_tokens IsError: direct=%v proxied=%v, want true", want.IsError, got.IsError)
	}
	if diff := jsonDiff(t, want, got); diff != "" {
		t.Errorf("proxied asimonim error result differs from direct registration:\n%s", diff)
	}
}

func TestMountUpstreamsPerSessionRootsThroughProxy(t *testing.T) {
	rootA := tokenProject(t, "alpha", "#ff0000")
	rootB := tokenProject(t, "beta", "#0000ff")

	t.Chdir(t.TempDir())
	server, mount := newMountedServerWithMount(t, MountOptions{})
	baseline := runtime.NumGoroutine()

	sessionA := connectServerWithProtocol(t, server, "2026-07-28", &mcp.Root{URI: fileURI(rootA)}) //nolint:staticcheck // roots are required by PLAN section 8.1
	sessionB := connectServerWithProtocol(t, server, "2026-07-28", &mcp.Root{URI: fileURI(rootB)}) //nolint:staticcheck // roots are required by PLAN section 8.1

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

	result := searchTokensResult(t, sessionA, "alpha")
	raw, ok := result.GetMeta()[workspaceMetaKey]
	if !ok {
		t.Fatalf("proxied search_tokens result is missing %q in _meta: %#v", workspaceMetaKey, result.GetMeta())
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

	// Resource reads use the same per-session client and must resolve the
	// same workspace as the tool calls.
	resourcesA, err := sessionA.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "asimonim://tokens"})
	if err != nil {
		t.Fatalf("client A ReadResource(asimonim://tokens) error = %v", err)
	}
	resourcesB, err := sessionB.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "asimonim://tokens"})
	if err != nil {
		t.Fatalf("client B ReadResource(asimonim://tokens) error = %v", err)
	}
	if got := resourceText(resourcesA); !strings.Contains(got, rootA) || strings.Contains(got, rootB) {
		t.Errorf("client A tokens resource = %s, want only root A (%s)", got, rootA)
	}
	if got := resourceText(resourcesB); !strings.Contains(got, rootB) || strings.Contains(got, rootA) {
		t.Errorf("client B tokens resource = %s, want only root B (%s)", got, rootB)
	}

	// Closing the host sessions must close the internal per-session clients
	// and drop them from the mount.
	if err := sessionA.Close(); err != nil {
		t.Fatalf("closing session A: %v", err)
	}
	if err := sessionB.Close(); err != nil {
		t.Fatalf("closing session B: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return mount.activeSessions() == 0 },
		"internal per-session clients were not cleaned up")
	waitFor(t, 5*time.Second, func() bool { return runtime.NumGoroutine() <= baseline+2 },
		"goroutines leaked after closing the host sessions")
}

// resourceText concatenates the text contents of a resource read.
func resourceText(result *mcp.ReadResourceResult) string {
	var builder strings.Builder
	for _, content := range result.Contents {
		if content != nil {
			builder.WriteString(content.Text)
		}
	}
	return builder.String()
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func TestMountUpstreamsConflictErrorPolicy(t *testing.T) {
	t.Chdir(projectWithManifest(t))

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "validate_html",
		Description: "host tool",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "host"}}}, nil
	})

	mount, err := MountUpstreams(server, MountOptions{ConflictPolicy: ConflictError})
	var conflict *UpstreamConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("MountUpstreams() error = %v, want *UpstreamConflictError", err)
	}
	if mount != nil {
		t.Errorf("MountUpstreams() mount = %v, want nil after a conflict error", mount)
	}

	names := listToolNames(t, connectServer(t, server))
	if !slicesContains(names, "validate_html") {
		t.Fatalf("tools/list lost the host tool: %v", names)
	}
	for _, name := range cemToolNames {
		if name == "validate_html" {
			continue
		}
		if slicesContains(names, name) {
			t.Errorf("tools/list contains cem tool %q after an error-policy conflict; nothing should be mounted", name)
		}
	}
	for _, name := range asimonimToolNames {
		if slicesContains(names, name) {
			t.Errorf("tools/list contains %q after an error-policy conflict; nothing should be mounted", name)
		}
	}
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestMountUpstreamsCapturesSlogWarnings(t *testing.T) {
	t.Chdir(projectWithManifest(t))

	logger, records := newRecordingLogger()
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "validate_html",
		Description: "host tool",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, nil
	})

	mount, err := MountUpstreams(server, MountOptions{})
	if err != nil {
		t.Fatalf("MountUpstreams() error = %v", err)
	}
	t.Cleanup(func() { _ = mount.Close() })

	warnings := records.warnings()
	if len(warnings) == 0 {
		t.Fatal("MountUpstreams() logged no warning for a conflicting entry without provenance")
	}
	found := false
	for _, warning := range warnings {
		if warning.Attrs["name"] != "validate_html" {
			continue
		}
		found = true
		if warning.Attrs["source"] != "cem" {
			t.Errorf("warning source = %v, want cem", warning.Attrs["source"])
		}
		if warning.Attrs["existingVersion"] != unknownUpstreamVersion {
			t.Errorf("warning existingVersion = %v, want %q", warning.Attrs["existingVersion"], unknownUpstreamVersion)
		}
		if warning.Attrs["bundledVersion"] != bundledUpstreamVersion(cemModulePath) {
			t.Errorf("warning bundledVersion = %v, want %q", warning.Attrs["bundledVersion"], bundledUpstreamVersion(cemModulePath))
		}
	}
	if !found {
		t.Errorf("no warning names validate_html: %#v", warnings)
	}
}

func TestMountUpstreamsDisableListSkipsProvenance(t *testing.T) {
	t.Chdir(projectWithManifest(t))
	server, _ := newMountedServerWithMount(t, MountOptions{
		DisableTools:     []string{"validate_html"},
		DisableResources: []string{"cem://elements"},
		DisableTemplates: []string{"cem://element/{tagName}/slots"},
	})

	tools, resources, templates := observedSurface(t, server)
	if _, ok := tools["validate_html"]; ok {
		t.Error("disabled tool was mounted")
	}
	if _, ok := resources["cem://elements"]; ok {
		t.Error("disabled resource was mounted")
	}
	if _, ok := templates["cem://element/{tagName}/slots"]; ok {
		t.Error("disabled template was mounted")
	}
}

// TestMountUpstreamsConcurrentFirstCallsShareSession proves that concurrent
// first calls on one host session resolve roots once and share one internal
// client instead of dialing twice and leaking a session.
func TestMountUpstreamsConcurrentFirstCallsShareSession(t *testing.T) {
	root := tokenProject(t, "alpha", "#ff0000")
	t.Chdir(t.TempDir())
	server, mount := newMountedServerWithMount(t, MountOptions{})
	session := connectServerWithProtocol(t, server, "2026-07-28", &mcp.Root{URI: fileURI(root)}) //nolint:staticcheck // roots are required by PLAN section 8.1

	const callers = 8
	var wait sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      "search_tokens",
				Arguments: map[string]any{"query": "alpha"},
			})
			switch {
			case err != nil:
				errs <- err
			case result.IsError:
				errs <- errors.New(contentText(result))
			case !strings.Contains(contentText(result), "alpha"):
				errs <- errors.New("result does not contain the root's alpha token")
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent call error = %v", err)
	}

	if got := mount.activeSessions(); got != 1 {
		t.Errorf("active internal sessions = %d, want 1 per host session", got)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("closing host session: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return mount.activeSessions() == 0 },
		"internal session was not cleaned up after the host session closed")
}

// TestProxiedAsimonimCwdMatchesDirectRegistrationWithoutRoots proves a host
// client without the roots capability gets the same cwd workspace and _meta
// as direct asimonim registration, with no spurious roots warning.
func TestProxiedAsimonimCwdMatchesDirectRegistrationWithoutRoots(t *testing.T) {
	t.Chdir(tokenProject(t, "alpha", "#ff0000"))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	direct := connectServer(t, directAsimonimServer(t, cwd))
	proxied := connectServer(t, newMountedServer(t, MountOptions{}))

	want := searchTokensResult(t, direct, "alpha")
	got := searchTokensResult(t, proxied, "alpha")
	if diff := jsonDiff(t, want, got); diff != "" {
		t.Errorf("proxied no-roots result differs from direct registration:\n%s", diff)
	}

	for name, result := range map[string]*mcp.CallToolResult{"direct": want, "proxied": got} {
		meta, ok := result.GetMeta()[workspaceMetaKey].(map[string]any)
		if !ok {
			t.Fatalf("%s result is missing %q: %#v", name, workspaceMetaKey, result.GetMeta())
		}
		if meta["source"] != "cwd" {
			t.Errorf("%s workspace source = %v, want cwd", name, meta["source"])
		}
		if warning, ok := meta["warning"]; ok {
			t.Errorf("%s workspace has warning %v, want none", name, warning)
		}
	}
}
