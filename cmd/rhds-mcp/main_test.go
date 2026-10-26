// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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

func TestAdvertisedProtocolVersionsAreNewestFirst(t *testing.T) {
	want := []string{"2026-07-28", "2025-11-25", "2025-06-18"}
	if got := supportedProtocolVersions; !slices.Equal(got, want) {
		t.Fatalf("supportedProtocolVersions = %v, want %v", got, want)
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
			if result.Instructions == "" {
				t.Error("initialize result has no instructions")
			}
		})
	}
}

// TestStdoutCarriesOnlyJSONRPC builds the real binary and drives it over stdio.
// Every line written to stdout must be a JSON-RPC message; logs belong on
// stderr. The fake method call provokes an error log so the assertion covers a
// failing request as well as successful ones.
func TestStdoutCarriesOnlyJSONRPC(t *testing.T) {
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
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"stdout-test","version":"0.0.0"},"capabilities":{}}}`,
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
	if initializeVersion != "2025-11-25" {
		t.Errorf("initialize response protocol version = %q, want %q",
			initializeVersion, "2025-11-25")
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
