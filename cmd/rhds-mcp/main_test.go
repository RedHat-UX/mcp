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

// buildServerBinary builds the command, passing extra arguments to `go build`.
// Tests that exercise build-time flags use extra to prove the flag wiring.
func buildServerBinary(t *testing.T, extra ...string) string {
	t.Helper()
	binaryName := "rhds-mcp"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(t.TempDir(), binaryName)
	args := []string{"build", "-trimpath"}
	args = append(args, extra...)
	args = append(args, "-o", binary, ".")
	build := exec.Command("go", args...)
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building binary: %v", err)
	}
	return binary
}

// TestAdvertisedProtocolVersions checks the list a 2026-07-28 client sees in
// server/discover. Comparing it with supportedProtocolVersions proves both the
// contents and the order, and fails if newServer stops applying the
// SupportedProtocolVersions option: the SDK default list also contains
// 2025-03-26 and 2024-11-05.
func TestAdvertisedProtocolVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server := newServer(slog.New(slog.DiscardHandler))
	var advertised []string
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if method == "server/discover" && err == nil {
				if discover, ok := result.(*mcp.DiscoverResult); ok {
					advertised = slices.Clone(discover.SupportedVersions)
				}
			}
			return result, err
		}
	})

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
		ProtocolVersion: supportedProtocolVersions[0],
	})
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	if !slices.Equal(advertised, supportedProtocolVersions) {
		t.Errorf("advertised protocol versions = %v, want %v",
			advertised, supportedProtocolVersions)
	}
}

// TestAdvertisedProtocolVersionsAreSupportedBySDK guards against an SDK bump
// that drops a version we advertise: mcp.NewServer panics on an unsupported
// version, so failing here names the cause instead of surfacing a panic.
func TestAdvertisedProtocolVersionsAreSupportedBySDK(t *testing.T) {
	sdkVersions := mcp.SupportedProtocolVersions()
	for _, version := range supportedProtocolVersions {
		if !slices.Contains(sdkVersions, version) {
			t.Errorf("advertised protocol version %q is not supported by the SDK (%v)",
				version, sdkVersions)
		}
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

			server := newServer(slog.New(slog.DiscardHandler))
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

// TestInitializeFallsBackForExcludedLegacyVersion proves the server restricts
// the protocol set rather than merely accepting the versions it lists. The
// client asks for 2025-03-26, which the SDK supports but this server
// deliberately excludes (PLAN section 8.1), and must be downgraded to the
// newest handshake version we advertise.
func TestInitializeFallsBackForExcludedLegacyVersion(t *testing.T) {
	const excluded = "2025-03-26"
	if slices.Contains(supportedProtocolVersions, excluded) {
		t.Fatalf("test assumption broken: %q is advertised", excluded)
	}
	if !slices.Contains(mcp.SupportedProtocolVersions(), excluded) {
		t.Skipf("SDK no longer supports %q; fallback behavior cannot be exercised", excluded)
	}
	want := supportedProtocolVersions[1]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server := newServer(slog.New(slog.DiscardHandler))
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
		ProtocolVersion: excluded,
	})
	if err != nil {
		t.Fatalf("initialize at %s: %v", excluded, err)
	}
	defer func() { _ = clientSession.Close() }()

	result := clientSession.InitializeResult()
	if result == nil {
		t.Fatal("initialize returned no result")
	}
	if result.ProtocolVersion != want {
		t.Errorf("negotiated protocol version for excluded %s = %q, want %q",
			excluded, result.ProtocolVersion, want)
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
// stderr. It also asserts the success and error response shapes: tools/list
// succeeds and the unknown method returns a JSON-RPC error.
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
		case "2":
			if len(message.Error) != 0 {
				t.Errorf("tools/list returned an error: %s", message.Error)
			}
			if len(message.Result) == 0 {
				t.Error("tools/list returned no result")
			}
			seen["2"] = true
		case "3":
			if len(message.Error) == 0 {
				t.Errorf("unknown method returned no error, result: %s", message.Result)
			}
			seen["3"] = true
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
	// A clean client disconnect (stdin EOF) is a normal shutdown and must exit
	// zero; editors and the npm wrapper treat a nonzero status as a crash.
	if waitErr != nil {
		t.Fatalf("server exit after stdin EOF: %v (stderr: %s)", waitErr, stderr.String())
	}

	if stderr.Len() == 0 {
		t.Error("expected diagnostic logs on stderr")
	}
}

// TestLdflagsSetServerVersion locks the linker symbol the Makefile uses to
// stamp VERSION into release binaries. A typo in either the Makefile or the
// variable name would otherwise ship binaries reporting the default version.
func TestLdflagsSetServerVersion(t *testing.T) {
	const want = "9.9.9"
	binary := buildServerBinary(t, "-ldflags=-X main.serverVersion="+want)
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
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	if _, err := fmt.Fprintln(stdin, initializeRequest(protocolVersion)); err != nil {
		t.Fatalf("writing initialize: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	if !scanner.Scan() {
		t.Fatalf("no initialize response: %v (stderr: %s)", scanner.Err(), stderr.String())
	}
	message := checkJSONRPCLine(t, scanner.Text())
	var result struct {
		ServerInfo struct {
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(message.Result, &result); err != nil {
		t.Fatalf("decoding initialize result: %v", err)
	}
	if result.ServerInfo.Version != want {
		t.Errorf("server version = %q, want %q", result.ServerInfo.Version, want)
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
