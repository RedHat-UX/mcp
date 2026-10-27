// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/tools"
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func checkJSONRPCLine(t *testing.T, line string) rpcMessage {
	t.Helper()
	var message rpcMessage
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		t.Fatalf("stdout line is not JSON: %q: %v", line, err)
	}
	if message.JSONRPC != "2.0" {
		t.Fatalf("stdout line is not JSON-RPC 2.0: %q", line)
	}
	return message
}

func initializeRequest(protocolVersion string) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"clientInfo":{"name":"rhds-mcp-test","version":"0.0.0"},"capabilities":{}}}`,
		protocolVersion,
	)
}

func buildServerBinary(t *testing.T) string {
	t.Helper()
	binaryName := "rhds-mcp"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(t.TempDir(), binaryName)
	build := exec.Command("go", "build", "-trimpath", "-o", binary, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building binary: %v", err)
	}
	return binary
}

func TestAdvertisedProtocolVersionsAreNewestFirst(t *testing.T) {
	want := []string{"2026-07-28", "2025-11-25", "2025-06-18"}
	if got := supportedProtocolVersions; !slices.Equal(got, want) {
		t.Fatalf("supportedProtocolVersions = %v, want %v", got, want)
	}
}

func TestInstructionsListEverySupportedVersion(t *testing.T) {
	instructions := serverInstructions()
	for _, version := range supportedProtocolVersions {
		if !strings.Contains(instructions, version) {
			t.Errorf("instructions do not list supported version %q", version)
		}
	}
}

func TestInitializeAtEachAdvertisedVersion(t *testing.T) {
	for _, version := range supportedProtocolVersions {
		t.Run(version, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			server := newServer(slog.New(slog.DiscardHandler), tools.New(nil, data.ErrNoData))
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			session, err := server.Connect(ctx, serverTransport, nil)
			if err != nil {
				t.Fatalf("connecting server: %v", err)
			}
			defer func() { _ = session.Close() }()

			client := mcp.NewClient(&mcp.Implementation{
				Name:    "rhds-mcp-test",
				Version: "0.0.0",
			}, nil)
			clientSession, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{
				ProtocolVersion: version,
			})
			if err != nil {
				t.Fatalf("initialize at %s: %v", version, err)
			}
			defer func() { _ = clientSession.Close() }()

			result := clientSession.InitializeResult()
			if result == nil {
				t.Fatal("initialize returned no result")
			}
			if result.ProtocolVersion != version {
				t.Errorf("negotiated protocol version = %q, want %q", result.ProtocolVersion, version)
			}
			if result.ServerInfo == nil {
				t.Fatal("initialize result has no server info")
			}
			if result.ServerInfo.Name != serverName {
				t.Errorf("server name = %q, want %q", result.ServerInfo.Name, serverName)
			}
			if result.ServerInfo.Version != serverVersion {
				t.Errorf("server version = %q, want %q", result.ServerInfo.Version, serverVersion)
			}
			if result.Instructions == "" {
				t.Error("initialize result has no instructions")
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil", err: nil, want: 0},
		{name: "canceled", err: context.Canceled, want: 0},
		{name: "wrapped canceled", err: fmt.Errorf("run: %w", context.Canceled), want: 0},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: 1},
		{name: "other error", err: errors.New("boom"), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err); got != tt.want {
				t.Errorf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestStdoutCarriesOnlyJSONRPC builds the real binary and drives it over stdio.
// Every line written to stdout must be a JSON-RPC message; logs belong on
// stderr. The fake method call provokes an error log so the assertion covers a
// failing request as well as successful ones.
func TestStdoutCarriesOnlyJSONRPC(t *testing.T) {
	binary := buildServerBinary(t)
	protocolVersion := supportedProtocolVersions[1]

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("opening stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("opening stdout: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting server: %v", err)
	}

	requests := []string{
		initializeRequest(protocolVersion),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"rhds/does-not-exist","params":{}}`,
	}
	if _, err := fmt.Fprintln(stdin, strings.Join(requests, "\n")); err != nil {
		t.Fatalf("writing requests: %v", err)
	}

	// Read the responses before closing stdin: closing stdin tears down the
	// session, and the SDK may drop a response that is still in flight.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	seen := make(map[string]bool)
	var initializeVersion string
	for len(seen) < 3 {
		if !scanner.Scan() {
			t.Fatalf("stdout closed before every response arrived: %v", scanner.Err())
		}
		message := checkJSONRPCLine(t, scanner.Text())
		switch string(message.ID) {
		case "1":
			var result struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if err := json.Unmarshal(message.Result, &result); err != nil {
				t.Fatalf("decoding initialize result: %v", err)
			}
			initializeVersion = result.ProtocolVersion
			seen["1"] = true
		case "2", "3":
			seen[string(message.ID)] = true
		}
	}
	if initializeVersion != protocolVersion {
		t.Errorf("initialize response protocol version = %q, want %q",
			initializeVersion, protocolVersion)
	}

	if err := stdin.Close(); err != nil {
		t.Fatalf("closing stdin: %v", err)
	}
	// Drain anything the server wrote before shutting down.
	for scanner.Scan() {
		checkJSONRPCLine(t, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading stdout: %v", err)
	}

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("server did not exit: %v", ctx.Err())
	}
	// A nonzero exit after stdin closes is acceptable; the assertion here is
	// about the stdout contract, not the shutdown code.
	if waitErr != nil {
		t.Logf("server exited with: %v", waitErr)
	}

	if stderr.Len() == 0 {
		t.Error("expected diagnostic logs on stderr")
	}
}

// TestSIGTERMExitsCleanly asserts that an interrupt-style shutdown maps to exit
// code 0 rather than logging an error and failing.
func TestSIGTERMExitsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM delivery differs on Windows")
	}

	binary := buildServerBinary(t)
	protocolVersion := supportedProtocolVersions[1]

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("opening stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("opening stdout: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting server: %v", err)
	}
	if _, err := fmt.Fprintln(stdin, initializeRequest(protocolVersion)); err != nil {
		t.Fatalf("writing initialize: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	if !scanner.Scan() {
		// Wait for the exec stderr copy goroutine before reading the buffer.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("no initialize response before signal: %v (stderr: %s)",
			scanner.Err(), stderr.String())
	}
	checkJSONRPCLine(t, scanner.Text())

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}

	// The process closes stdout when it exits; drain it before calling Wait.
	for scanner.Scan() {
		checkJSONRPCLine(t, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading stdout: %v", err)
	}

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("server did not exit after SIGTERM: %v", ctx.Err())
	}
	if waitErr != nil {
		t.Fatalf("server exit after SIGTERM: %v (stderr: %s)", waitErr, stderr.String())
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit code after SIGTERM = %d, want 0 (stderr: %s)", code, stderr.String())
	}
}

func connectTestClient(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "rhds-mcp-test", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func TestPhaseBToolsAreRegistered(t *testing.T) {
	clientSession := connectTestClient(t,
		newServer(slog.New(slog.DiscardHandler), tools.New(nil, data.ErrNoData)))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		if tool.OutputSchema == nil {
			t.Errorf("tool %q has no output schema", tool.Name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not marked read-only", tool.Name)
		}
	}
	want := []string{
		"rhds_resolve_version",
		"rhds_list_components",
		"rhds_get_component_api",
		"rhds_search",
		"rhds_query_tokens",
		"rhds_validate_markup",
	}
	for _, name := range want {
		if !slices.Contains(names, name) {
			t.Errorf("tools/list is missing %q: %v", name, names)
		}
	}
}

func TestDataToolsReturnStructuredErrorWithoutData(t *testing.T) {
	clientSession := connectTestClient(t,
		newServer(slog.New(slog.DiscardHandler), tools.New(nil, data.ErrNoData)))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calls := []struct {
		name string
		args map[string]any
	}{
		{"rhds_resolve_version", map[string]any{}},
		{"rhds_list_components", map[string]any{}},
		{"rhds_get_component_api", map[string]any{"tagName": "rh-button"}},
		{"rhds_search", map[string]any{"query": "button"}},
		{"rhds_validate_markup", map[string]any{"markup": "<rh-button></rh-button>"}},
		{"rhds_query_tokens", map[string]any{"token": "color-brand"}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
				Name:      call.name,
				Arguments: call.args,
			})
			if err != nil {
				t.Fatalf("CallTool(%s) error = %v", call.name, err)
			}
			if !result.IsError {
				t.Fatalf("CallTool(%s) IsError = false, want true", call.name)
			}
			encoded, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatalf("marshaling structured content: %v", err)
			}
			var structured struct {
				Error struct {
					Code string `json:"code"`
					Hint string `json:"hint"`
				} `json:"error"`
			}
			if err := json.Unmarshal(encoded, &structured); err != nil {
				t.Fatalf("decoding structured content %s: %v", encoded, err)
			}
			if structured.Error.Code != "no_bundled_data" {
				t.Errorf("error code = %q, want no_bundled_data (content: %s)",
					structured.Error.Code, encoded)
			}
			if structured.Error.Hint == "" {
				t.Errorf("structured error has no hint: %s", encoded)
			}
		})
	}
}
