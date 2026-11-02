// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 11 requires a roots/elicitation protocol matrix; the SDK marks roots deprecated but keeps it functional.

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/tools"
)

func protocolFixtureService(t *testing.T) *tools.Service {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate the protocol test source")
	}
	root := filepath.Join(filepath.Dir(sourceFile), "..", "..", "internal", "tools", "testdata")
	store, err := data.Open(os.DirFS(root))
	if err != nil {
		t.Fatalf("opening fixture store: %v", err)
	}
	return tools.New(store, nil)
}

func ambiguousWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	lock := `{"lockfileVersion":3,"packages":{
		"node_modules/@rhds/elements":{"version":"4.0.0"},
		"packages/app/node_modules/@rhds/elements":{"version":"5.0.0"}
	}}`
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatalf("writing lockfile: %v", err)
	}
	return root
}

type clientConfig struct {
	roots       bool
	elicitation bool
	manualMRTR  bool
	rootsList   []*mcp.Root //nolint:staticcheck // roots are required by docs/PLAN.md section 8.1
	choose      string
}

func connectProtocolClient(
	t *testing.T,
	version string,
	config clientConfig,
) *mcp.ClientSession {
	t.Helper()
	server := newServer(slog.New(slog.DiscardHandler), protocolFixtureService(t))

	capabilities := &mcp.ClientCapabilities{}
	if config.roots {
		capabilities.RootsV2 = &mcp.RootCapabilities{} //nolint:staticcheck // roots required by PLAN
	}
	if config.elicitation {
		capabilities.Elicitation = &mcp.ElicitationCapabilities{}
	}
	options := &mcp.ClientOptions{Capabilities: capabilities}
	if config.elicitation {
		choice := config.choose
		if choice == "" {
			choice = "5.0.0"
		}
		options.ElicitationHandler = func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{
				Action:  "accept",
				Content: map[string]any{"version": choice},
			}, nil
		}
	}
	if config.manualMRTR {
		options.MultiRoundTrip = &mcp.MultiRoundTripOptions{Disabled: true}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "protocol-test", Version: "0.0.0"}, options)
	if len(config.rootsList) > 0 {
		client.AddRoots(config.rootsList...) //nolint:staticcheck // roots required by PLAN
	}
	clientSession, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{
		ProtocolVersion: version,
	})
	if err != nil {
		t.Fatalf("connecting client at %s: %v", version, err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func callResolveVersion(
	t *testing.T,
	session *mcp.ClientSession,
	params *mcp.CallToolParams,
) (*mcp.CallToolResult, tools.ResolveVersionOutput) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if params == nil {
		params = &mcp.CallToolParams{Name: "rhds_resolve_version", Arguments: map[string]any{}}
	}
	result, err := session.CallTool(ctx, params)
	if err != nil {
		t.Fatalf("CallTool(rhds_resolve_version) error = %v", err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structured content: %v", err)
	}
	var output tools.ResolveVersionOutput
	if len(encoded) > 0 && string(encoded) != "null" {
		if err := json.Unmarshal(encoded, &output); err != nil {
			t.Fatalf("decoding structured content %s: %v", encoded, err)
		}
	}
	return result, output
}

func fileRoot(t *testing.T, path string) *mcp.Root { //nolint:staticcheck // roots required by PLAN
	t.Helper()
	return &mcp.Root{URI: (&url.URL{Scheme: "file", Path: path}).String()} //nolint:staticcheck // roots required by PLAN
}

// TestProtocolMatrixSessionMethods covers 2025-06-18 and 2025-11-25, where the
// server uses ServerSession.ListRoots and ServerSession.Elicit.
func TestProtocolMatrixSessionMethods(t *testing.T) {
	workspace := ambiguousWorkspace(t)
	for _, version := range []string{"2025-06-18", "2025-11-25"} {
		t.Run(version+"/roots+elicitation", func(t *testing.T) {
			session := connectProtocolClient(t, version, clientConfig{
				roots:       true,
				elicitation: true,
				rootsList:   []*mcp.Root{fileRoot(t, workspace)}, //nolint:staticcheck // roots required by PLAN
			})
			result, out := callResolveVersion(t, session, nil)
			if result.IsError {
				t.Fatalf("IsError = true: %+v", out.Error)
			}
			if out.Resolved.Elements != "5.0.0" {
				t.Errorf("elements = %q, want 5.0.0", out.Resolved.Elements)
			}
			if out.Resolved.Source != "elicitation" {
				t.Errorf("source = %q, want elicitation", out.Resolved.Source)
			}
			if out.Ambiguity == nil || len(out.Ambiguity.Majors) != 2 {
				t.Errorf("ambiguity = %+v", out.Ambiguity)
			}
			if len(out.Candidates) != 2 {
				t.Errorf("candidates = %+v", out.Candidates)
			}
		})

		t.Run(version+"/roots without elicitation", func(t *testing.T) {
			session := connectProtocolClient(t, version, clientConfig{
				roots:     true,
				rootsList: []*mcp.Root{fileRoot(t, workspace)}, //nolint:staticcheck // roots required by PLAN
			})
			result, out := callResolveVersion(t, session, nil)
			if !result.IsError {
				t.Fatal("IsError = false, want a structured ambiguity error")
			}
			if out.Error == nil || out.Error.Code != "ambiguous_version" {
				t.Fatalf("error = %+v", out.Error)
			}
			if out.Ambiguity == nil || len(out.Ambiguity.Candidates) != 2 {
				t.Errorf("ambiguity = %+v", out.Ambiguity)
			}
		})
	}
}

// TestProtocolMatrixMRTR covers 2026-07-28 automatic multi-round-trip.
func TestProtocolMatrixMRTR(t *testing.T) {
	workspace := ambiguousWorkspace(t)
	version := "2026-07-28"

	t.Run("roots+elicitation", func(t *testing.T) {
		session := connectProtocolClient(t, version, clientConfig{
			roots:       true,
			elicitation: true,
			rootsList:   []*mcp.Root{fileRoot(t, workspace)}, //nolint:staticcheck // roots required by PLAN
		})
		result, out := callResolveVersion(t, session, nil)
		if result.IsError {
			t.Fatalf("IsError = true: %+v", out.Error)
		}
		if out.Resolved.Elements != "5.0.0" || out.Resolved.Source != "elicitation" {
			t.Errorf("resolved = %+v", out.Resolved)
		}
	})

	t.Run("roots without elicitation", func(t *testing.T) {
		session := connectProtocolClient(t, version, clientConfig{
			roots:     true,
			rootsList: []*mcp.Root{fileRoot(t, workspace)}, //nolint:staticcheck // roots required by PLAN
		})
		result, out := callResolveVersion(t, session, nil)
		if !result.IsError {
			t.Fatal("IsError = false, want a structured ambiguity error")
		}
		if out.Error == nil || out.Error.Code != "ambiguous_version" {
			t.Fatalf("error = %+v", out.Error)
		}
	})
}

// TestProtocolMatrixMRTRManual drives the retry loop explicitly so the test
// asserts the wire contract: ListRootsParams then ElicitParams, with
// requestState carrying the workspace root between rounds.
func TestProtocolMatrixMRTRManual(t *testing.T) {
	workspace := ambiguousWorkspace(t)
	session := connectProtocolClient(t, "2026-07-28", clientConfig{
		roots:       true,
		elicitation: true,
		manualMRTR:  true,
	})

	result, out := callResolveVersion(t, session, nil)
	if !result.NeedsInput() {
		t.Fatalf("NeedsInput = false, want roots input request; output = %+v", out)
	}
	if _, ok := result.InputRequests["roots"].(*mcp.ListRootsParams); !ok { //nolint:staticcheck // roots required by PLAN
		t.Fatalf("input requests = %T, want ListRootsParams", result.InputRequests["roots"])
	}
	state := result.RequestState
	round, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "rhds_resolve_version",
		Arguments: map[string]any{},
		InputResponses: mcp.InputResponseMap{
			"roots": &mcp.ListRootsResult{Roots: []*mcp.Root{fileRoot(t, workspace)}}, //nolint:staticcheck // roots required by PLAN
		},
		RequestState: state,
	})
	if err != nil {
		t.Fatalf("roots retry error = %v", err)
	}
	if !round.NeedsInput() {
		t.Fatalf("round 2 NeedsInput = false, want elicitation; output = %+v", round.StructuredContent)
	}
	elicit, ok := round.InputRequests["version"].(*mcp.ElicitParams)
	if !ok {
		t.Fatalf("input requests = %T, want ElicitParams", round.InputRequests["version"])
	}
	if elicit.Message == "" {
		t.Error("elicitation message is empty")
	}
	state = round.RequestState
	final, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "rhds_resolve_version",
		Arguments: map[string]any{},
		InputResponses: mcp.InputResponseMap{
			"version": &mcp.ElicitResult{
				Action:  "accept",
				Content: map[string]any{"version": "5.0.0"},
			},
		},
		RequestState: state,
	})
	if err != nil {
		t.Fatalf("elicitation retry error = %v", err)
	}
	if final.NeedsInput() {
		t.Fatalf("final NeedsInput = true: %+v", final.InputRequests)
	}
	encoded, err := json.Marshal(final.StructuredContent)
	if err != nil {
		t.Fatalf("marshal final output: %v", err)
	}
	var resolved tools.ResolveVersionOutput
	if err := json.Unmarshal(encoded, &resolved); err != nil {
		t.Fatalf("unmarshal final output: %v", err)
	}
	if resolved.Error != nil {
		t.Fatalf("final error = %+v", resolved.Error)
	}
	if resolved.Resolved.Elements != "5.0.0" {
		t.Errorf("elements = %q", resolved.Resolved.Elements)
	}
}

// TestProtocolMatrixCWDFallback covers clients with no roots capability. The
// cwd is the workspace, so the cascade still sees the ambiguity.
func TestProtocolMatrixCWDFallback(t *testing.T) {
	workspace := ambiguousWorkspace(t)
	t.Chdir(workspace)

	t.Run("2025-06-18 with elicitation", func(t *testing.T) {
		session := connectProtocolClient(t, "2025-06-18", clientConfig{
			elicitation: true,
		})
		result, out := callResolveVersion(t, session, nil)
		if result.IsError {
			t.Fatalf("IsError = true: %+v", out.Error)
		}
		if out.Resolved.Elements != "5.0.0" || out.Resolved.Source != "elicitation" {
			t.Errorf("resolved = %+v", out.Resolved)
		}
	})

	t.Run("2025-11-25 with elicitation", func(t *testing.T) {
		session := connectProtocolClient(t, "2025-11-25", clientConfig{
			elicitation: true,
		})
		result, out := callResolveVersion(t, session, nil)
		if result.IsError {
			t.Fatalf("IsError = true: %+v", out.Error)
		}
		if out.Resolved.Elements != "5.0.0" || out.Resolved.Source != "elicitation" {
			t.Errorf("resolved = %+v", out.Resolved)
		}
		if len(out.Resolved.Warnings) == 0 {
			t.Error("expected a cwd fallback warning")
		}
	})

	t.Run("2026-07-28 with elicitation", func(t *testing.T) {
		session := connectProtocolClient(t, "2026-07-28", clientConfig{
			elicitation: true,
		})
		result, out := callResolveVersion(t, session, nil)
		if result.IsError {
			t.Fatalf("IsError = true: %+v", out.Error)
		}
		if out.Resolved.Elements != "5.0.0" || out.Resolved.Source != "elicitation" {
			t.Errorf("resolved = %+v", out.Resolved)
		}
	})

	t.Run("2025-06-18 without capabilities", func(t *testing.T) {
		session := connectProtocolClient(t, "2025-06-18", clientConfig{})
		result, out := callResolveVersion(t, session, nil)
		if !result.IsError {
			t.Fatal("IsError = false, want ambiguous_version")
		}
		if out.Error == nil || out.Error.Code != "ambiguous_version" {
			t.Fatalf("error = %+v", out.Error)
		}
	})

	t.Run("2026-07-28 without elicitation", func(t *testing.T) {
		session := connectProtocolClient(t, "2026-07-28", clientConfig{})
		result, out := callResolveVersion(t, session, nil)
		if !result.IsError {
			t.Fatal("IsError = false, want ambiguous_version")
		}
		if out.Error == nil || out.Error.Code != "ambiguous_version" {
			t.Fatalf("error = %+v", out.Error)
		}
	})
}

func callListComponents(
	t *testing.T,
	session *mcp.ClientSession,
	arguments map[string]any,
) (*mcp.CallToolResult, tools.ListComponentsOutput) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "rhds_list_components",
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("CallTool(rhds_list_components) error = %v", err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structured content: %v", err)
	}
	var output tools.ListComponentsOutput
	if len(encoded) > 0 && string(encoded) != "null" {
		if err := json.Unmarshal(encoded, &output); err != nil {
			t.Fatalf("decoding structured content %s: %v", encoded, err)
		}
	}
	return result, output
}

// TestPaginationUsesCursorVersionBeforeResolution proves that page two uses
// the cursor-bound version instead of re-running the ambiguous workspace
// resolution.
func TestPaginationUsesCursorVersionBeforeResolution(t *testing.T) {
	workspace := ambiguousWorkspace(t)
	t.Chdir(workspace)
	session := connectProtocolClient(t, "2026-07-28", clientConfig{})

	_, first := callListComponents(t, session, map[string]any{
		"version":  "5.0.0",
		"pageSize": 2,
	})
	if first.Error != nil {
		t.Fatalf("page 1 error = %+v", first.Error)
	}
	if first.NextCursor == "" {
		t.Fatal("page 1 has no next cursor")
	}

	_, second := callListComponents(t, session, map[string]any{
		"cursor":   first.NextCursor,
		"pageSize": 2,
	})
	if second.Error != nil {
		t.Fatalf("page 2 error = %+v; resolution should not run for a cursor", second.Error)
	}
	if second.Resolved.Elements != "5.0.0" {
		t.Errorf("page 2 elements = %q, want the cursor-bound 5.0.0", second.Resolved.Elements)
	}
	if second.Resolved.Source != "cursor" {
		t.Errorf("page 2 source = %q, want cursor", second.Resolved.Source)
	}

	_, changed := callListComponents(t, session, map[string]any{
		"cursor":            first.NextCursor,
		"includeDeprecated": true,
	})
	if changed.Error == nil || changed.Error.Code != "cursor_options_mismatch" {
		t.Fatalf("changed includeDeprecated error = %+v, want cursor_options_mismatch", changed.Error)
	}
}

func TestProtocolExplicitVersionSkipsRoots(t *testing.T) {
	for _, version := range []string{"2025-06-18", "2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			session := connectProtocolClient(t, version, clientConfig{
				roots:       true,
				elicitation: true,
			})
			result, out := callResolveVersion(t, session, &mcp.CallToolParams{
				Name:      "rhds_resolve_version",
				Arguments: map[string]any{"version": "4.0.0"},
			})
			if result.NeedsInput() {
				t.Fatalf("explicit version returned an input request: %+v", result.InputRequests)
			}
			if result.IsError {
				t.Fatalf("IsError = true: %+v", out.Error)
			}
			if out.Resolved.Elements != "4.0.0" || out.Resolved.Source != "version argument" {
				t.Errorf("resolved = %+v", out.Resolved)
			}
			if len(out.Candidates) != 1 || out.Candidates[0].Level != "explicit" {
				t.Errorf("candidates = %+v", out.Candidates)
			}
		})
	}
}
