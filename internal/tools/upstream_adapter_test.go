// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 11 requires a roots/elicitation protocol matrix; the SDK marks roots deprecated but keeps it functional.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/mod/semver"
)

// capturedRecord is one slog record captured by recordingHandler.
type capturedRecord struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// recordingHandler captures every slog record for assertions.
type recordingHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := make(map[string]any, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.Any()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, capturedRecord{
		Level:   record.Level,
		Message: record.Message,
		Attrs:   attrs,
	})
	h.mu.Unlock()
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// warnings returns the records logged at WARN or above.
func (h *recordingHandler) warnings() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var warnings []capturedRecord
	for _, record := range h.records {
		if record.Level >= slog.LevelWarn {
			warnings = append(warnings, record)
		}
	}
	return warnings
}

// staticForwarder is an upstream implementation used by the adapter tests. It
// returns fixed results and, when configured, records cancellation.
type staticForwarder struct {
	toolResult     *mcp.CallToolResult
	toolErr        error
	resourceResult *mcp.ReadResourceResult
	resourceErr    error
	onCall         func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

func (f *staticForwarder) callTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if f.onCall != nil {
		return f.onCall(ctx, req)
	}
	if f.toolErr != nil {
		return nil, f.toolErr
	}
	if f.toolResult != nil {
		return f.toolResult, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
}

func (f *staticForwarder) readResource(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if f.resourceErr != nil {
		return nil, f.resourceErr
	}
	if f.resourceResult != nil {
		return f.resourceResult, nil
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{URI: "probe://resource", Text: "ok"}},
	}, nil
}

func testTool(name string) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Title:       "Probe " + name,
		Description: "upstream " + name,
		InputSchema: map[string]any{"type": "object"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}
}

func testResource(uri string) *mcp.Resource {
	return &mcp.Resource{
		URI:         uri,
		Name:        "probe",
		Title:       "Probe resource",
		Description: "upstream resource",
		MIMEType:    "application/json",
	}
}

func testTemplate(uriTemplate string) *mcp.ResourceTemplate {
	return &mcp.ResourceTemplate{
		URITemplate: uriTemplate,
		Name:        "probe-template",
		Title:       "Probe template",
		Description: "upstream template",
		MIMEType:    "application/json",
	}
}

func probeSource(version string) upstreamSource {
	return upstreamSource{
		name:    "probe",
		version: version,
		definitions: upstreamDefinitions{
			tools:     []*mcp.Tool{testTool("probe_tool")},
			resources: []*mcp.Resource{testResource("probe://resource")},
			templates: []*mcp.ResourceTemplate{testTemplate("probe://resource/{id}")},
		},
		forward: &staticForwarder{},
	}
}

// hostWithConflicts registers host entries that collide with probeSource.
// existingVersion is empty when the entries must carry no provenance.
func hostWithConflicts(t *testing.T, existingVersion string) *mcp.Server {
	t.Helper()
	return hostWithConflictsSource(t, existingVersion, "probe")
}

// hostWithConflictsSource is hostWithConflicts with a chosen provenance source.
func hostWithConflictsSource(t *testing.T, existingVersion, existingSource string) *mcp.Server {
	t.Helper()
	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)

	meta := func() mcp.Meta {
		if existingVersion == "" {
			return nil
		}
		return mcp.Meta{provenanceMetaKey: map[string]any{
			"source":  existingSource,
			"version": existingVersion,
		}}
	}

	host.AddTool(&mcp.Tool{
		Name:        "probe_tool",
		Description: "host tool",
		InputSchema: map[string]any{"type": "object"},
		Meta:        meta(),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "host"}}}, nil
	})
	host.AddResource(&mcp.Resource{
		URI:         "probe://resource",
		Name:        "probe",
		Description: "host resource",
		Meta:        meta(),
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: "probe://resource", Text: "host"}},
		}, nil
	})
	host.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "probe://resource/{id}",
		Name:        "probe-template",
		Description: "host template",
		Meta:        meta(),
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: "probe://resource/1", Text: "host"}},
		}, nil
	})
	return host
}

// observedSurface lists the host surface through a client so the assertions
// see exactly what an MCP client sees.
func observedSurface(t *testing.T, host *mcp.Server) (map[string]*mcp.Tool, map[string]*mcp.Resource, map[string]*mcp.ResourceTemplate) {
	t.Helper()
	session := connectServer(t, host)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	templates, err := session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates() error = %v", err)
	}

	toolMap := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		toolMap[tool.Name] = tool
	}
	resourceMap := make(map[string]*mcp.Resource, len(resources.Resources))
	for _, resource := range resources.Resources {
		resourceMap[resource.URI] = resource
	}
	templateMap := make(map[string]*mcp.ResourceTemplate, len(templates.ResourceTemplates))
	for _, template := range templates.ResourceTemplates {
		templateMap[template.URITemplate] = template
	}
	return toolMap, resourceMap, templateMap
}

func TestBundledUpstreamVersionUsesRealBuildInfo(t *testing.T) {
	for _, module := range []string{cemModulePath, asimonimModulePath} {
		got := bundledUpstreamVersion(module)
		if got == unknownUpstreamVersion {
			t.Errorf("bundledUpstreamVersion(%q) = %q, want the build's module version", module, got)
			continue
		}
		if !semver.IsValid(got) {
			t.Errorf("bundledUpstreamVersion(%q) = %q, want a valid semver version", module, got)
		}
	}
}

func TestUpstreamVersionFromBuildInfoFallbacks(t *testing.T) {
	info := &debug.BuildInfo{Deps: []*debug.Module{
		{Path: "example.com/devel", Version: "(devel)"},
		{Path: "example.com/empty", Version: ""},
		{Path: "example.com/valid", Version: "v1.2.3"},
		{Path: "example.com/replaced-local", Version: "v0.1.0", Replace: &debug.Module{Path: "../local"}},
		{Path: "example.com/replaced-versioned", Version: "v0.1.0", Replace: &debug.Module{Path: "example.com/fork", Version: "v0.2.0"}},
		{Path: "example.com/replaced-devel", Version: "v0.1.0", Replace: &debug.Module{Path: "example.com/fork", Version: "(devel)"}},
	}}

	tests := []struct {
		name   string
		info   *debug.BuildInfo
		module string
		want   string
	}{
		{name: "devel falls back", info: info, module: "example.com/devel", want: unknownUpstreamVersion},
		{name: "empty falls back", info: info, module: "example.com/empty", want: unknownUpstreamVersion},
		{name: "missing falls back", info: info, module: "example.com/missing", want: unknownUpstreamVersion},
		{name: "nil build info falls back", info: nil, module: "example.com/valid", want: unknownUpstreamVersion},
		{name: "real version is reported", info: info, module: "example.com/valid", want: "v1.2.3"},
		{name: "local replacement reports unknown", info: info, module: "example.com/replaced-local", want: unknownUpstreamVersion},
		{name: "versioned replacement reports the replacement version", info: info, module: "example.com/replaced-versioned", want: "v0.2.0"},
		{name: "devel replacement reports unknown", info: info, module: "example.com/replaced-devel", want: unknownUpstreamVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := upstreamVersionFrom(test.info, test.module); got != test.want {
				t.Errorf("upstreamVersionFrom(%q) = %q, want %q", test.module, got, test.want)
			}
		})
	}
}

func TestMountSourcesConflictMatrix(t *testing.T) {
	const bundled = "v0.11.6"

	tests := []struct {
		name                string
		bundledVersion      string
		existingVersion     string
		existingSource      string
		policy              ConflictPolicy
		wantReplaced        bool
		wantWarnings        int
		wantReason          string
		wantExistingVersion string
	}{
		{name: "newest replaces older", existingVersion: "v0.11.5", policy: ConflictNewest, wantReplaced: true, wantWarnings: 3, wantReason: "older"},
		{name: "newest keeps newer", existingVersion: "v0.11.7", policy: ConflictNewest, wantReplaced: false, wantWarnings: 3, wantReason: "shadowed"},
		{name: "newest keeps equal", existingVersion: bundled, policy: ConflictNewest, wantReplaced: false, wantWarnings: 0},
		{name: "newest keeps versioned entry when bundled version is unknown", bundledVersion: unknownUpstreamVersion, existingVersion: "v0.11.5", policy: ConflictNewest, wantReplaced: false, wantWarnings: 3, wantReason: "bundled version is unknown"},
		{name: "newest keeps versioned entry when bundled version is devel", bundledVersion: "(devel)", existingVersion: "v0.11.5", policy: ConflictNewest, wantReplaced: false, wantWarnings: 3, wantReason: "bundled version is unknown"},
		{name: "newest keeps versioned entry when bundled version is malformed", bundledVersion: "banana", existingVersion: "v0.11.5", policy: ConflictNewest, wantReplaced: false, wantWarnings: 3, wantReason: "bundled version is unknown"},
		{name: "newest replaces unknown provenance", existingVersion: "", policy: ConflictNewest, wantReplaced: true, wantWarnings: 3, wantReason: "unknown"},
		{name: "newest replaces unknown provenance when bundled version is unknown", bundledVersion: unknownUpstreamVersion, existingVersion: "", policy: ConflictNewest, wantReplaced: true, wantWarnings: 3, wantReason: "unknown"},
		{name: "newest replaces different upstream when bundled version is unknown", bundledVersion: unknownUpstreamVersion, existingVersion: "v0.11.5", existingSource: "other", policy: ConflictNewest, wantReplaced: true, wantWarnings: 3, wantReason: "different upstream"},
		{name: "newest replaces malformed provenance", existingVersion: "banana", policy: ConflictNewest, wantReplaced: true, wantWarnings: 3, wantReason: "unknown", wantExistingVersion: unknownUpstreamVersion},
		{name: "newest replaces devel provenance", existingVersion: "(devel)", policy: ConflictNewest, wantReplaced: true, wantWarnings: 3, wantReason: "unknown", wantExistingVersion: unknownUpstreamVersion},
		{name: "replace replaces older", existingVersion: "v0.11.5", policy: ConflictReplace, wantReplaced: true, wantWarnings: 3, wantReason: "policy"},
		{name: "replace replaces newer", existingVersion: "v0.11.7", policy: ConflictReplace, wantReplaced: true, wantWarnings: 3, wantReason: "policy"},
		{name: "replace replaces unknown provenance", existingVersion: "", policy: ConflictReplace, wantReplaced: true, wantWarnings: 3, wantReason: "unknown"},
		{name: "skip keeps older", existingVersion: "v0.11.5", policy: ConflictSkip, wantReplaced: false, wantWarnings: 3, wantReason: "policy"},
		{name: "skip keeps newer", existingVersion: "v0.11.7", policy: ConflictSkip, wantReplaced: false, wantWarnings: 3, wantReason: "policy"},
		{name: "skip keeps unknown provenance", existingVersion: "", policy: ConflictSkip, wantReplaced: false, wantWarnings: 3, wantReason: "unknown"},
		{name: "skip keeps malformed provenance", existingVersion: "banana", policy: ConflictSkip, wantReplaced: false, wantWarnings: 3, wantReason: "unknown", wantExistingVersion: unknownUpstreamVersion},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundledVersion := test.bundledVersion
			if bundledVersion == "" {
				bundledVersion = bundled
			}
			host := hostWithConflicts(t, test.existingVersion)
			if test.existingSource != "" {
				host = hostWithConflictsSource(t, test.existingVersion, test.existingSource)
			}
			logger, records := newRecordingLogger()

			err := mountSources(host, []upstreamSource{probeSource(bundledVersion)}, test.policy, logger)
			if err != nil {
				t.Fatalf("mountSources() error = %v", err)
			}

			tools, resources, templates := observedSurface(t, host)
			tool := tools["probe_tool"]
			resource := resources["probe://resource"]
			template := templates["probe://resource/{id}"]
			if tool == nil || resource == nil || template == nil {
				t.Fatalf("mounted surface is incomplete: tool=%v resource=%v template=%v", tool, resource, template)
			}

			if got := tool.Description; got != wantDescription(test.wantReplaced, "upstream probe_tool", "host tool") {
				t.Errorf("tool description = %q, want replaced=%v", got, test.wantReplaced)
			}
			if got := resource.Description; got != wantDescription(test.wantReplaced, "upstream resource", "host resource") {
				t.Errorf("resource description = %q, want replaced=%v", got, test.wantReplaced)
			}
			if got := template.Description; got != wantDescription(test.wantReplaced, "upstream template", "host template") {
				t.Errorf("template description = %q, want replaced=%v", got, test.wantReplaced)
			}

			warnings := records.warnings()
			if len(warnings) != test.wantWarnings {
				t.Fatalf("warnings = %d, want %d: %#v", len(warnings), test.wantWarnings, warnings)
			}
			for _, warning := range warnings {
				if got := warning.Attrs["source"]; got != "probe" {
					t.Errorf("warning source = %v, want probe", got)
				}
				if got := warning.Attrs["bundledVersion"]; got != bundledVersion {
					t.Errorf("warning bundledVersion = %v, want %q", got, bundledVersion)
				}
				if _, ok := warning.Attrs["name"]; !ok {
					t.Errorf("warning has no name: %#v", warning.Attrs)
				}
				if test.wantReason != "" && !strings.Contains(warning.Message, test.wantReason) {
					t.Errorf("warning message %q does not mention %q", warning.Message, test.wantReason)
				}
			}
			if test.wantWarnings > 0 {
				wantExisting := test.existingVersion
				if test.wantExistingVersion != "" {
					wantExisting = test.wantExistingVersion
				}
				if wantExisting == "" {
					wantExisting = unknownUpstreamVersion
				}
				for _, warning := range warnings {
					if got := warning.Attrs["existingVersion"]; got != wantExisting {
						t.Errorf("warning existingVersion = %v, want %q", got, wantExisting)
					}
				}
			}

			if test.wantReplaced {
				raw, ok := tool.GetMeta()[provenanceMetaKey]
				if !ok {
					t.Fatalf("replaced tool is missing %q provenance: %#v", provenanceMetaKey, tool.GetMeta())
				}
				meta, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("provenance = %T, want a JSON object", raw)
				}
				if got := meta["source"]; got != "probe" {
					t.Errorf("provenance source = %v, want probe", got)
				}
				if got := meta["version"]; got != bundledVersion {
					t.Errorf("provenance version = %v, want %q", got, bundledVersion)
				}
			}
		})
	}
}

func wantDescription(replaced bool, upstream, host string) string {
	if replaced {
		return upstream
	}
	return host
}

func TestMountSourcesErrorPolicy(t *testing.T) {
	host := hostWithConflicts(t, "v0.11.5")
	logger, records := newRecordingLogger()

	err := mountSources(host, []upstreamSource{probeSource("v0.11.6")}, ConflictError, logger)
	var conflict *UpstreamConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("mountSources() error = %v, want *UpstreamConflictError", err)
	}
	if got := len(conflict.Conflicts); got != 3 {
		t.Errorf("conflict count = %d, want 3: %#v", got, conflict.Conflicts)
	}
	for _, kind := range []string{"tool", "resource", "template"} {
		if !slices.ContainsFunc(conflict.Conflicts, func(c UpstreamConflict) bool { return c.Kind == kind }) {
			t.Errorf("conflict error has no %s conflict: %#v", kind, conflict.Conflicts)
		}
	}
	if warnings := records.warnings(); len(warnings) != 0 {
		t.Errorf("error policy logged %d warnings, want 0: %#v", len(warnings), warnings)
	}

	// The failed mount must leave the host surface untouched.
	tools, resources, templates := observedSurface(t, host)
	if got := tools["probe_tool"].Description; got != "host tool" {
		t.Errorf("tool description = %q, want the host entry untouched", got)
	}
	if got := resources["probe://resource"].Description; got != "host resource" {
		t.Errorf("resource description = %q, want the host entry untouched", got)
	}
	if got := templates["probe://resource/{id}"].Description; got != "host template" {
		t.Errorf("template description = %q, want the host entry untouched", got)
	}
}

func TestMountSourcesRejectsUnknownPolicy(t *testing.T) {
	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, _ := newRecordingLogger()
	if err := mountSources(host, []upstreamSource{probeSource("v0.11.6")}, ConflictPolicy("bogus"), logger); err == nil {
		t.Fatal("mountSources() error = nil, want an unknown-policy error")
	}
}

// TestMountSourcesPagination follows result cursors for every surface kind.
func TestMountSourcesPagination(t *testing.T) {
	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	// A page size of 2 forces the SDK server to set NextCursor; capturing all
	// five entries proves captureSurface follows it.
	upstream := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.0.0"}, &mcp.ServerOptions{PageSize: 2})
	for i := 0; i < 5; i++ {
		name := "paged_" + string(rune('a'+i))
		upstream.AddTool(testToolWithName(name), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name}}}, nil
		})
		uri := "paged://resource/" + string(rune('a'+i))
		upstream.AddResource(&mcp.Resource{URI: uri, Name: uri, Description: "paged"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, Text: uri}}}, nil
		})
		template := "paged://template/" + string(rune('a'+i)) + "/{id}"
		upstream.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: template, Name: template, Description: "paged"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "paged://template/x/1", Text: "paged"}}}, nil
		})
	}

	client, closeUpstream := connectInMemory(t, upstream, "2025-11-25")
	defer closeUpstream()

	definitions, err := captureSurface(context.Background(), client)
	if err != nil {
		t.Fatalf("captureSurface() error = %v", err)
	}
	if got := len(definitions.tools); got != 5 {
		t.Errorf("captured %d tools, want 5", got)
	}
	if got := len(definitions.resources); got != 5 {
		t.Errorf("captured %d resources, want 5", got)
	}
	if got := len(definitions.templates); got != 5 {
		t.Errorf("captured %d templates, want 5", got)
	}

	logger, _ := newRecordingLogger()
	if err := mountSources(host, []upstreamSource{{
		name:        "paged",
		version:     "v1.0.0",
		definitions: definitions,
		forward:     &staticForwarder{},
	}}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	tools, resources, templates := observedSurface(t, host)
	if got := len(tools); got != 5 {
		t.Errorf("host has %d tools, want 5", got)
	}
	if got := len(resources); got != 5 {
		t.Errorf("host has %d resources, want 5", got)
	}
	if got := len(templates); got != 5 {
		t.Errorf("host has %d templates, want 5", got)
	}
}

func testToolWithName(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: "paged", InputSchema: map[string]any{"type": "object"}}
}

// connectInMemory connects a client to server and returns a cleanup that
// closes both sessions.
func connectInMemory(t *testing.T, server *mcp.Server, protocol string) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "adapter-test", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	return clientSession, func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	}
}

func TestProxyPreservesToolResultFidelity(t *testing.T) {
	want := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: "hello"}, &mcp.TextContent{Text: "world"}},
		StructuredContent: map[string]any{"answer": float64(42), "nested": []any{"a", "b"}},
		Meta:              mcp.Meta{"upstream/result": "kept"},
		IsError:           true,
	}
	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, _ := newRecordingLogger()
	if err := mountSources(host, []upstreamSource{{
		name:        "probe",
		version:     "v1.0.0",
		definitions: upstreamDefinitions{tools: []*mcp.Tool{testTool("probe_tool")}},
		forward:     &staticForwarder{toolResult: want},
	}}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	session := connectServer(t, host)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "probe_tool",
		Arguments: map[string]any{"x": 1},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if diff := jsonDiff(t, want, got); diff != "" {
		t.Errorf("proxied result differs from the upstream result:\n%s", diff)
	}
}

func TestProxyPreservesResourceResultFidelity(t *testing.T) {
	want := &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{URI: "probe://resource", MIMEType: "application/json", Text: `{"a":1}`},
		},
		Meta: mcp.Meta{"upstream/result": "kept"},
	}
	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, _ := newRecordingLogger()
	if err := mountSources(host, []upstreamSource{{
		name:        "probe",
		version:     "v1.0.0",
		definitions: upstreamDefinitions{resources: []*mcp.Resource{testResource("probe://resource")}},
		forward:     &staticForwarder{resourceResult: want},
	}}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	session := connectServer(t, host)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "probe://resource"})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	if diff := jsonDiff(t, want, got); diff != "" {
		t.Errorf("proxied result differs from the upstream result:\n%s", diff)
	}
}

func TestProxyPreservesErrors(t *testing.T) {
	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, _ := newRecordingLogger()
	const upstreamError = "upstream exploded"
	if err := mountSources(host, []upstreamSource{{
		name:        "probe",
		version:     "v1.0.0",
		definitions: upstreamDefinitions{tools: []*mcp.Tool{testTool("probe_tool")}},
		forward:     &staticForwarder{toolErr: errors.New(upstreamError)},
	}}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	session := connectServer(t, host)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "probe_tool"})
	if err == nil {
		t.Fatal("CallTool() error = nil, want the upstream error")
	}
	if !strings.Contains(err.Error(), upstreamError) {
		t.Errorf("CallTool() error = %v, want it to contain %q", err, upstreamError)
	}
}

func TestProxyPreservesCancellation(t *testing.T) {
	canceled := make(chan struct{})
	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, _ := newRecordingLogger()
	if err := mountSources(host, []upstreamSource{{
		name:        "probe",
		version:     "v1.0.0",
		definitions: upstreamDefinitions{tools: []*mcp.Tool{testTool("probe_tool")}},
		forward: &staticForwarder{onCall: func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			select {
			case <-ctx.Done():
				close(canceled)
				return nil, ctx.Err()
			case <-time.After(30 * time.Second):
				return nil, errors.New("upstream did not observe cancellation")
			}
		}},
	}}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	session := connectServer(t, host)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "probe_tool"}); err == nil {
		t.Fatal("CallTool() error = nil, want a cancellation error")
	}

	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream forwarder never observed the cancellation")
	}
}

func jsonDiff(t *testing.T, want, got any) string {
	t.Helper()
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshaling want: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshaling got: %v", err)
	}
	if string(wantJSON) == string(gotJSON) {
		return ""
	}
	return "want: " + string(wantJSON) + "\n got: " + string(gotJSON)
}

func newRecordingLogger() (*slog.Logger, *recordingHandler) {
	handler := &recordingHandler{}
	return slog.New(handler), handler
}

func TestProvenanceFromMeta(t *testing.T) {
	tests := []struct {
		name   string
		meta   map[string]any
		want   upstreamProvenance
		wantOK bool
	}{
		{name: "nil meta", meta: nil},
		{name: "missing key", meta: map[string]any{"other": "value"}},
		{name: "wrong value type", meta: map[string]any{provenanceMetaKey: "not-an-object"}},
		{name: "object without fields", meta: map[string]any{provenanceMetaKey: map[string]any{}}, wantOK: true},
		{name: "object with fields", meta: map[string]any{provenanceMetaKey: map[string]any{
			"source": "cem", "version": "v0.11.6",
		}}, want: upstreamProvenance{source: "cem", version: "v0.11.6"}, wantOK: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := provenanceFromMeta(test.meta)
			if ok != test.wantOK {
				t.Fatalf("provenanceFromMeta() ok = %v, want %v", ok, test.wantOK)
			}
			if ok && got != test.want {
				t.Errorf("provenanceFromMeta() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRootsFromResponses(t *testing.T) {
	tests := []struct {
		name      string
		responses mcp.InputResponseMap
		wantOK    bool
	}{
		{name: "nil map", responses: nil},
		{name: "missing key", responses: mcp.InputResponseMap{}},
		{name: "other input response", responses: mcp.InputResponseMap{
			rootsInputRequestKey: &mcp.ElicitResult{},
		}},
		{name: "typed nil roots", responses: mcp.InputResponseMap{
			rootsInputRequestKey: (*mcp.ListRootsResult)(nil), //nolint:staticcheck // roots required by PLAN section 8.1
		}},
		{name: "roots", responses: mcp.InputResponseMap{
			rootsInputRequestKey: &mcp.ListRootsResult{Roots: []*mcp.Root{{URI: "file:///tmp/project"}}}, //nolint:staticcheck // roots required by PLAN section 8.1
		}, wantOK: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			roots, ok := rootsFromResponses(test.responses)
			if ok != test.wantOK {
				t.Fatalf("rootsFromResponses() ok = %v, want %v", ok, test.wantOK)
			}
			if ok && len(roots) != 1 {
				t.Errorf("rootsFromResponses() = %d roots, want 1", len(roots))
			}
		})
	}
}

// TestProxyCancellationReachesInternalClient exercises cancellation through
// the real clientForwarder and in-memory transports, not the static test
// forwarder.
func TestProxyCancellationReachesInternalClient(t *testing.T) {
	canceled := make(chan struct{})
	internal := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "0.0.0"}, nil)
	internal.AddTool(&mcp.Tool{
		Name:        "slow_tool",
		Description: "blocks until canceled",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-ctx.Done():
			close(canceled)
			return nil, ctx.Err()
		case <-time.After(30 * time.Second):
			return nil, errors.New("upstream did not observe cancellation")
		}
	})
	internalSession, closeInternal := connectInMemory(t, internal, "2025-11-25")
	defer closeInternal()

	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, _ := newRecordingLogger()
	if err := mountSources(host, []upstreamSource{{
		name:    "slow",
		version: "v1.0.0",
		definitions: upstreamDefinitions{tools: []*mcp.Tool{{
			Name:        "slow_tool",
			Description: "blocks until canceled",
			InputSchema: map[string]any{"type": "object"},
		}}},
		forward: &clientForwarder{client: internalSession},
	}}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	session := connectServer(t, host)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "slow_tool"}); err == nil {
		t.Fatal("CallTool() error = nil, want a cancellation error")
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("internal upstream handler never observed the cancellation")
	}
}

func TestMountCloseIsIdempotent(t *testing.T) {
	t.Chdir(projectWithManifest(t))
	_, mount := newMountedServerWithMount(t, MountOptions{})
	if err := mount.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := mount.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestMountUpstreamsRejectsUnknownPolicy(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	mount, err := MountUpstreams(server, MountOptions{ConflictPolicy: ConflictPolicy("bogus")})
	if err == nil {
		t.Fatal("MountUpstreams() error = nil, want an unknown-policy error")
	}
	if mount != nil {
		t.Errorf("MountUpstreams() mount = %v, want nil", mount)
	}
}

// TestMountSourcesErrorPolicyWithoutConflictsMounts guards the strict policy's
// happy path: error means "error on a collision", not "mount nothing".
func TestMountSourcesErrorPolicyWithoutConflictsMounts(t *testing.T) {
	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, records := newRecordingLogger()

	if err := mountSources(host, []upstreamSource{probeSource("v1.0.0")}, ConflictError, logger); err != nil {
		t.Fatalf("mountSources() error = %v, want nil without conflicts", err)
	}
	if warnings := records.warnings(); len(warnings) != 0 {
		t.Errorf("warnings = %d, want 0: %#v", len(warnings), warnings)
	}

	tools, resources, templates := observedSurface(t, host)
	if got := tools["probe_tool"].Description; got != "upstream probe_tool" {
		t.Errorf("tool description = %q, want the upstream entry", got)
	}
	if got := resources["probe://resource"].Description; got != "upstream resource" {
		t.Errorf("resource description = %q, want the upstream entry", got)
	}
	if got := templates["probe://resource/{id}"].Description; got != "upstream template" {
		t.Errorf("template description = %q, want the upstream entry", got)
	}
}

// TestMountSourcesCrossSourceVersionsAreNotComparable proves versions from
// different upstreams are not compared as semver.
func TestMountSourcesCrossSourceVersionsAreNotComparable(t *testing.T) {
	host := hostWithConflictsSource(t, "v0.11.5", "other")
	logger, records := newRecordingLogger()

	if err := mountSources(host, []upstreamSource{probeSource("v0.11.6")}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	tools, _, _ := observedSurface(t, host)
	if got := tools["probe_tool"].Description; got != "upstream probe_tool" {
		t.Errorf("tool description = %q, want a replacement under the unknown-version rule", got)
	}

	warnings := records.warnings()
	if len(warnings) != 3 {
		t.Fatalf("warnings = %d, want 3: %#v", len(warnings), warnings)
	}
	for _, warning := range warnings {
		if !strings.Contains(warning.Message, "different upstream") {
			t.Errorf("warning message %q does not mention a different upstream", warning.Message)
		}
		if got := warning.Attrs["existingVersion"]; got != "v0.11.5" {
			t.Errorf("warning existingVersion = %v, want v0.11.5", got)
		}
		if got := warning.Attrs["existingSource"]; got != "other" {
			t.Errorf("warning existingSource = %v, want other", got)
		}
	}
}

// TestMountCloseWithActiveSession closes the mount while a host session is
// still connected: the internal client must close immediately and further
// proxied calls must fail instead of hanging.
func TestMountCloseWithActiveSession(t *testing.T) {
	root := tokenProject(t, "alpha", "#ff0000")
	t.Chdir(t.TempDir())
	server, mount := newMountedServerWithMount(t, MountOptions{})
	session := connectServerWithProtocol(t, server, "2026-07-28", &mcp.Root{URI: fileURI(root)}) //nolint:staticcheck // roots are required by PLAN section 8.1

	if got := searchTokens(t, session, "alpha"); !strings.Contains(got, "alpha") {
		t.Fatalf("search_tokens = %s, want the root's token", got)
	}
	if got := mount.activeSessions(); got != 1 {
		t.Fatalf("active internal sessions = %d, want 1", got)
	}

	if err := mount.Close(); err != nil {
		t.Fatalf("Mount.Close() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search_tokens",
		Arguments: map[string]any{"query": "alpha"},
	}); err == nil {
		t.Error("CallTool() error = nil after Mount.Close, want a closed-mount error")
	}

	if err := session.Close(); err != nil {
		t.Fatalf("closing host session: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return mount.activeSessions() == 0 },
		"internal session was not cleaned up after close")
}

func TestForwardedToolParamsPreserveCallerFields(t *testing.T) {
	request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Meta:      mcp.Meta{"progressToken": "token-1", "caller/trace": "trace-1"},
		Name:      "probe_tool",
		Arguments: json.RawMessage(`{"x":1}`),
		InputResponses: mcp.InputResponseMap{
			"elicit-1": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"ok": true}},
		},
		RequestState: "state-1",
	}}

	got := forwardedToolParams(request)
	if got.Name != "probe_tool" {
		t.Errorf("Name = %q, want probe_tool", got.Name)
	}
	if diff := jsonDiff(t, request.Params.GetMeta(), got.GetMeta()); diff != "" {
		t.Errorf("forwarded Meta differs:\n%s", diff)
	}
	if diff := jsonDiff(t, request.Params.InputResponses, got.InputResponses); diff != "" {
		t.Errorf("forwarded InputResponses differ:\n%s", diff)
	}
	if got.RequestState != "state-1" {
		t.Errorf("RequestState = %q, want state-1", got.RequestState)
	}
	if diff := jsonDiff(t, request.Params.Arguments, got.Arguments); diff != "" {
		t.Errorf("forwarded Arguments differ:\n%s", diff)
	}
	if token := got.GetProgressToken(); token != "token-1" {
		t.Errorf("progress token = %v, want token-1", token)
	}
	got.Meta["probe"] = true
	if _, ok := request.Params.GetMeta()["probe"]; ok {
		t.Error("forwarded params share the caller's _meta map; mutating it changed the request")
	}
}

func TestForwardedInputState(t *testing.T) {
	rootsAndElicit := mcp.InputResponseMap{
		rootsInputRequestKey: &mcp.ListRootsResult{}, //nolint:staticcheck // roots required by PLAN section 8.1
		"elicit-1":           &mcp.ElicitResult{Action: "accept"},
	}
	rootsOnly := mcp.InputResponseMap{
		rootsInputRequestKey: &mcp.ListRootsResult{}, //nolint:staticcheck // roots required by PLAN section 8.1
	}
	elicitOnly := mcp.InputResponseMap{
		"elicit-1": &mcp.ElicitResult{Action: "accept"},
	}

	tests := []struct {
		name      string
		fresh     bool
		responses mcp.InputResponseMap
		state     string
		want      mcp.InputResponseMap
		wantState string
	}{
		{
			name:      "fresh call drops all caller input state",
			fresh:     true,
			responses: rootsAndElicit,
			state:     rootsRequestState,
		},
		{
			name:      "established call strips the proxy roots response",
			responses: rootsAndElicit,
			state:     rootsRequestState,
			want:      elicitOnly,
		},
		{
			name:      "established call strips the roots response and keeps unrelated state",
			responses: rootsOnly,
			state:     "upstream-state",
			wantState: "upstream-state",
		},
		{
			name:      "established call preserves unrelated input state",
			responses: elicitOnly,
			state:     "upstream-state",
			want:      elicitOnly,
			wantState: "upstream-state",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, gotState := forwardedInputState(test.fresh, test.responses, test.state)
			if diff := jsonDiff(t, test.want, got); diff != "" {
				t.Errorf("forwardedInputState() responses differ:\n%s", diff)
			}
			if gotState != test.wantState {
				t.Errorf("forwardedInputState() state = %q, want %q", gotState, test.wantState)
			}
		})
	}
}

func TestProgressRelayScopesTokensBySession(t *testing.T) {
	relay := newProgressRelay()
	sessionA := new(mcp.ServerSession)
	sessionB := new(mcp.ServerSession)

	relay.register("shared", sessionA)
	if got := relay.target("shared"); got != sessionA {
		t.Fatalf("target = %v, want session A", got)
	}

	relay.register("shared", sessionB)
	if got := relay.target("shared"); got != nil {
		t.Errorf("target = %v for a token shared by two sessions, want nil", got)
	}

	// Releasing one call must not remove the other session's registration.
	relay.release(context.Background(), "shared", sessionA)
	if got := relay.target("shared"); got != sessionB {
		t.Errorf("target = %v after session A released, want session B", got)
	}
	relay.release(context.Background(), "shared", sessionB)
	if got := relay.target("shared"); got != nil {
		t.Errorf("target = %v after both sessions released, want nil", got)
	}
	if got := relay.activeTokens(); got != 0 {
		t.Errorf("activeTokens() = %d, want 0", got)
	}
}

func TestProgressRelayDuplicateRegistrationIsRefcounted(t *testing.T) {
	relay := newProgressRelay()
	session := new(mcp.ServerSession)

	relay.register("dup", session)
	relay.register("dup", session)
	relay.release(context.Background(), "dup", session)
	if got := relay.target("dup"); got != session {
		t.Fatalf("target = %v after one of two registrations released, want the session", got)
	}
	relay.release(context.Background(), "dup", session)
	if got := relay.target("dup"); got != nil {
		t.Errorf("target = %v after both registrations released, want nil", got)
	}
	if got := relay.activeTokens(); got != 0 {
		t.Errorf("activeTokens() = %d, want 0", got)
	}
}

func TestProgressRelayBarrierIsMembershipBased(t *testing.T) {
	relay := newProgressRelay()
	for _, token := range []any{
		progressBarrierTokenBase + "caller-chosen",
		"ordinary-token",
	} {
		if _, ok := relay.takeBarrier(token); ok {
			t.Errorf("takeBarrier(%v) = true for a token that was never registered", token)
		}
	}
}

func TestBuildCEMSourceWiresProgressRelay(t *testing.T) {
	t.Chdir(projectWithManifest(t))
	mount := &Mount{state: newMountState()}
	t.Cleanup(func() { _ = mount.Close() })

	source, err := buildCEMSource(".", MountOptions{}, mount)
	if err != nil {
		t.Fatalf("buildCEMSource() error = %v", err)
	}
	if source == nil {
		t.Fatal("buildCEMSource() = nil, want a cem source")
	}
	forwarder, ok := source.forward.(*clientForwarder)
	if !ok {
		t.Fatalf("forwarder = %T, want *clientForwarder", source.forward)
	}
	if forwarder.relay == nil {
		t.Fatal("cem forwarder has no progress relay; cem progress notifications would be dropped")
	}
	if forwarder.relay.server == nil {
		t.Error("cem progress relay has no internal server session, so its drain cannot run")
	}
}

func TestForwardedResourceParamsPreserveCallerFields(t *testing.T) {
	request := &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{
		Meta: mcp.Meta{"progressToken": "token-2", "caller/trace": "trace-2"},
		URI:  "probe://resource",
		InputResponses: mcp.InputResponseMap{
			"elicit-2": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"ok": true}},
		},
		RequestState: "state-2",
	}}

	got := forwardedResourceParams(request)
	if got.URI != "probe://resource" {
		t.Errorf("URI = %q, want probe://resource", got.URI)
	}
	if diff := jsonDiff(t, request.Params.GetMeta(), got.GetMeta()); diff != "" {
		t.Errorf("forwarded Meta differs:\n%s", diff)
	}
	if diff := jsonDiff(t, request.Params.InputResponses, got.InputResponses); diff != "" {
		t.Errorf("forwarded InputResponses differ:\n%s", diff)
	}
	if got.RequestState != "state-2" {
		t.Errorf("RequestState = %q, want state-2", got.RequestState)
	}
	if token := got.GetProgressToken(); token != "token-2" {
		t.Errorf("progress token = %v, want token-2", token)
	}
}

// TestProxyForwardsToolRequestFields proves the internal upstream handler
// receives the same _meta, input responses, request state, and arguments a
// directly registered handler would receive.
func TestProxyForwardsToolRequestFields(t *testing.T) {
	captured := make(chan *mcp.CallToolParamsRaw, 1)
	definition := &mcp.Tool{Name: "echo_params", Description: "echo", InputSchema: map[string]any{"type": "object"}}
	internal := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.0.0"}, nil)
	internal.AddTool(definition, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		captured <- request.Params
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})

	internalSession, closeInternal := connectInMemory(t, internal, "2025-11-25")
	defer closeInternal()

	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, _ := newRecordingLogger()
	if err := mountSources(host, []upstreamSource{{
		name:        "probe",
		version:     "v1.0.0",
		definitions: upstreamDefinitions{tools: []*mcp.Tool{definition}},
		forward:     &clientForwarder{client: internalSession},
	}}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	session := connectServer(t, host)
	meta := mcp.Meta{"progressToken": "token-1", "caller/trace": "trace-1"}
	responses := mcp.InputResponseMap{
		"elicit-1": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"ok": true}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:           "echo_params",
		Arguments:      map[string]any{"x": 1},
		Meta:           meta,
		InputResponses: responses,
		RequestState:   "state-1",
	}); err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}

	select {
	case got := <-captured:
		if diff := jsonDiff(t, meta, got.GetMeta()); diff != "" {
			t.Errorf("upstream handler Meta differs:\n%s", diff)
		}
		if diff := jsonDiff(t, responses, got.InputResponses); diff != "" {
			t.Errorf("upstream handler InputResponses differ:\n%s", diff)
		}
		if got.RequestState != "state-1" {
			t.Errorf("upstream handler RequestState = %q, want state-1", got.RequestState)
		}
		if diff := jsonDiff(t, json.RawMessage(`{"x":1}`), got.Arguments); diff != "" {
			t.Errorf("upstream handler Arguments differ:\n%s", diff)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream handler was never called")
	}
}

// TestProxyForwardsResourceRequestFields is the resources/read counterpart of
// TestProxyForwardsToolRequestFields.
func TestProxyForwardsResourceRequestFields(t *testing.T) {
	captured := make(chan *mcp.ReadResourceParams, 1)
	internal := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.0.0"}, nil)
	definition := &mcp.Resource{URI: "probe://resource", Name: "probe", Description: "echo"}
	internal.AddResource(definition, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		captured <- request.Params
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: request.Params.URI, Text: "ok"}},
		}, nil
	})

	internalSession, closeInternal := connectInMemory(t, internal, "2025-11-25")
	defer closeInternal()

	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, _ := newRecordingLogger()
	if err := mountSources(host, []upstreamSource{{
		name:        "probe",
		version:     "v1.0.0",
		definitions: upstreamDefinitions{resources: []*mcp.Resource{definition}},
		forward:     &clientForwarder{client: internalSession},
	}}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	session := connectServer(t, host)
	meta := mcp.Meta{"progressToken": "token-2", "caller/trace": "trace-2"}
	responses := mcp.InputResponseMap{
		"elicit-2": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"ok": true}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI:            "probe://resource",
		Meta:           meta,
		InputResponses: responses,
		RequestState:   "state-2",
	}); err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}

	select {
	case got := <-captured:
		if diff := jsonDiff(t, meta, got.GetMeta()); diff != "" {
			t.Errorf("upstream handler Meta differs:\n%s", diff)
		}
		if diff := jsonDiff(t, responses, got.InputResponses); diff != "" {
			t.Errorf("upstream handler InputResponses differ:\n%s", diff)
		}
		if got.RequestState != "state-2" {
			t.Errorf("upstream handler RequestState = %q, want state-2", got.RequestState)
		}
		if got.URI != "probe://resource" {
			t.Errorf("upstream handler URI = %q, want probe://resource", got.URI)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream resource handler was never called")
	}
}

// TestProxyRelaysProgressNotifications proves a progress notification emitted
// by the internal upstream reaches the host client, and that the token map is
// empty once the forwarded call returns.
func TestProxyRelaysProgressNotifications(t *testing.T) {
	const token = "progress-token-1"
	internal := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.0.0"}, nil)
	definition := &mcp.Tool{Name: "progress_tool", Description: "reports progress", InputSchema: map[string]any{"type": "object"}}
	internal.AddTool(definition, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: request.Params.GetProgressToken(),
			Message:       "halfway",
			Progress:      1,
			Total:         2,
		}); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
	})

	relay := newProgressRelay()
	internalSession, serverSession, err := connectInternalClient(
		context.Background(), internal, "rhds-mcp-progress-test", "2025-11-25", nil, false, relay,
	)
	if err != nil {
		t.Fatalf("connectInternalClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = internalSession.Close()
		_ = serverSession.Close()
	})

	host := mcp.NewServer(&mcp.Implementation{Name: "host", Version: "0.0.0"}, nil)
	logger, _ := newRecordingLogger()
	if err := mountSources(host, []upstreamSource{{
		name:        "probe",
		version:     "v1.0.0",
		definitions: upstreamDefinitions{tools: []*mcp.Tool{definition}},
		forward:     &clientForwarder{client: internalSession, relay: relay},
	}}, ConflictNewest, logger); err != nil {
		t.Fatalf("mountSources() error = %v", err)
	}

	progress := make(chan *mcp.ProgressNotificationParams, 1)
	session := connectServerWithClientOptions(t, host, "2025-11-25", &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
		ProgressNotificationHandler: func(_ context.Context, request *mcp.ProgressNotificationClientRequest) {
			if request != nil && request.Params != nil {
				progress <- request.Params
			}
		},
	})

	params := new(mcp.CallToolParams)
	params.Name = "progress_tool"
	params.SetProgressToken(token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := session.CallTool(ctx, params); err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}

	select {
	case got := <-progress:
		if got.ProgressToken != token {
			t.Errorf("progress token = %v, want %q", got.ProgressToken, token)
		}
		if got.Message != "halfway" || got.Progress != 1 || got.Total != 2 {
			t.Errorf("progress = %#v, want the upstream notification unchanged", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host client did not receive the relayed progress notification")
	}

	if got := relay.activeTokens(); got != 0 {
		t.Errorf("progress relay retained %d token(s) after the call returned, want 0", got)
	}
}

// connectServerWithClientOptions connects a host client with caller-supplied
// options, so tests can observe notifications such as progress.
func connectServerWithClientOptions(
	t *testing.T,
	server *mcp.Server,
	protocol string,
	options *mcp.ClientOptions,
) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "upstream-test", Version: "0.0.0"}, options)
	clientSession, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func TestNonNilRoots(t *testing.T) {
	roots := []*mcp.Root{ //nolint:staticcheck // roots required by PLAN section 8.1
		nil,
		{URI: "file:///a"},
		nil,
		{URI: "file:///b"},
	}
	got := nonNilRoots(roots)
	if len(got) != 2 {
		t.Fatalf("nonNilRoots() returned %d roots, want 2", len(got))
	}
	if got[0].URI != "file:///a" || got[1].URI != "file:///b" {
		t.Errorf("nonNilRoots() = %#v, want the non-nil roots in order", got)
	}
	if got := nonNilRoots(nil); len(got) != 0 {
		t.Errorf("nonNilRoots(nil) = %d roots, want 0", len(got))
	}
}
