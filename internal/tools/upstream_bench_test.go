// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const benchResourceURI = "bench://resource"

// benchDefinitions is the minimal upstream surface used by the proxy
// benchmarks.
func benchDefinitions() upstreamDefinitions {
	return upstreamDefinitions{
		tools: []*mcp.Tool{{
			Name:        "bench_tool",
			Description: "benchmark tool",
			InputSchema: map[string]any{"type": "object"},
		}},
		resources: []*mcp.Resource{{
			URI:         benchResourceURI,
			Name:        "bench",
			Description: "benchmark resource",
		}},
	}
}

// newBenchHost registers the benchmark surface directly on a server.
func newBenchHost(definitions upstreamDefinitions, emitProgress bool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "bench-host", Version: "0.0.0"}, nil)
	server.AddTool(definitions.tools[0], func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if emitProgress {
			_ = request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: request.Params.GetProgressToken(),
				Message:       "bench",
				Progress:      1,
				Total:         2,
			})
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	server.AddResource(definitions.resources[0], func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if emitProgress {
			_ = request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: request.Params.GetProgressToken(),
				Message:       "bench",
				Progress:      1,
				Total:         2,
			})
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: benchResourceURI, Text: "ok"}},
		}, nil
	})
	return server
}

// benchConnect connects a client to server and cleans both sessions up.
func benchConnect(b *testing.B, server *mcp.Server) *mcp.ClientSession {
	b.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		b.Fatalf("connecting server: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "bench-client", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{
		ProtocolVersion: "2025-11-25",
	})
	if err != nil {
		b.Fatalf("connecting client: %v", err)
	}
	b.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	return clientSession
}

// newBenchProxy mounts the benchmark surface through the in-process proxy.
func newBenchProxy(b *testing.B, definitions upstreamDefinitions, emitProgress bool) *mcp.ClientSession {
	b.Helper()
	internal := newBenchHost(definitions, emitProgress)
	relay := newProgressRelay()
	internalSession, serverSession, err := connectInternalClient(
		context.Background(), internal, "bench-proxy", "2025-11-25", nil, false, relay,
	)
	if err != nil {
		b.Fatalf("connecting internal client: %v", err)
	}
	b.Cleanup(func() {
		_ = internalSession.Close()
		_ = serverSession.Close()
	})

	host := mcp.NewServer(&mcp.Implementation{Name: "bench-host", Version: "0.0.0"}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := mountSources(host, []upstreamSource{{
		name:        "bench",
		version:     "v1.0.0",
		definitions: definitions,
		forward:     &clientForwarder{client: internalSession, relay: relay},
	}}, ConflictNewest, logger); err != nil {
		b.Fatalf("mounting proxy: %v", err)
	}
	return benchConnect(b, host)
}

const benchProgressToken = "bench-progress-token"

func benchmarkToolCalls(b *testing.B, session *mcp.ClientSession, withProgress bool) {
	b.Helper()
	ctx := context.Background()
	params := &mcp.CallToolParams{
		Name:      "bench_tool",
		Arguments: map[string]any{"x": 1},
	}
	if withProgress {
		params.SetProgressToken(benchProgressToken)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := session.CallTool(ctx, params); err != nil {
			b.Fatalf("CallTool() error = %v", err)
		}
	}
}

func benchmarkResourceReads(b *testing.B, session *mcp.ClientSession, withProgress bool) {
	b.Helper()
	ctx := context.Background()
	params := &mcp.ReadResourceParams{URI: benchResourceURI}
	if withProgress {
		params.SetProgressToken(benchProgressToken)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := session.ReadResource(ctx, params); err != nil {
			b.Fatalf("ReadResource() error = %v", err)
		}
	}
}

// BenchmarkToolCallDirect measures a tool call with the handler registered
// directly on the host server.
func BenchmarkToolCallDirect(b *testing.B) {
	benchmarkToolCalls(b, benchConnect(b, newBenchHost(benchDefinitions(), false)), false)
}

// BenchmarkToolCallProxied measures the same tool call through the in-process
// proxy. The difference from BenchmarkToolCallDirect is the proxy's extra
// in-process round trip plus request/result copying.
func BenchmarkToolCallProxied(b *testing.B) {
	benchmarkToolCalls(b, newBenchProxy(b, benchDefinitions(), false), false)
}

// BenchmarkToolCallDirectWithProgress measures a direct tool call that carries
// a progress token and emits one progress notification.
func BenchmarkToolCallDirectWithProgress(b *testing.B) {
	benchmarkToolCalls(b, benchConnect(b, newBenchHost(benchDefinitions(), true)), true)
}

// BenchmarkToolCallProxiedWithProgress measures the same progress-bearing tool
// call through the proxy. It additionally pays the relay registration, the
// drain barrier, and the notification relay.
func BenchmarkToolCallProxiedWithProgress(b *testing.B) {
	benchmarkToolCalls(b, newBenchProxy(b, benchDefinitions(), true), true)
}

// BenchmarkResourceReadDirect measures a resource read with the handler
// registered directly on the host server.
func BenchmarkResourceReadDirect(b *testing.B) {
	benchmarkResourceReads(b, benchConnect(b, newBenchHost(benchDefinitions(), false)), false)
}

// BenchmarkResourceReadProxied measures the same resource read through the
// in-process proxy.
func BenchmarkResourceReadProxied(b *testing.B) {
	benchmarkResourceReads(b, newBenchProxy(b, benchDefinitions(), false), false)
}

// BenchmarkResourceReadDirectWithProgress measures a direct resource read that
// carries a progress token and emits one progress notification.
func BenchmarkResourceReadDirectWithProgress(b *testing.B) {
	benchmarkResourceReads(b, benchConnect(b, newBenchHost(benchDefinitions(), true)), true)
}

// BenchmarkResourceReadProxiedWithProgress measures the same progress-bearing
// resource read through the proxy.
func BenchmarkResourceReadProxiedWithProgress(b *testing.B) {
	benchmarkResourceReads(b, newBenchProxy(b, benchDefinitions(), true), true)
}
