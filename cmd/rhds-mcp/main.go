// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Command rhds-mcp serves the Red Hat Design System MCP server over stdio.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "rhds-mcp"
	serverVersion = "0.0.1"
)

// supportedProtocolVersions is advertised newest first and mirrors the
// negotiated protocol matrix in docs/PLAN.md section 8.1.
var supportedProtocolVersions = []string{
	"2026-07-28",
	"2025-11-25",
	"2025-06-18",
}

const serverInstructions = `RHDS MCP server for the Red Hat Design System.

Supported protocol versions: 2026-07-28, 2025-11-25, 2025-06-18.

The server is offline by default. It reads versioned RHDS data embedded in the
binary and makes no network requests at runtime.

Version resolution order, first hit wins: an explicit version argument, a
workspace lockfile, an installed @rhds/elements package, project package.json
dependencies, a bounded scan of HTML and Twig templates, then the latest
bundled stable version with a warning.

Use search for discovery and exact lookup when the element, token, or topic is
already known. Every response carries the resolved versions and warnings.`

func newServer(logger *slog.Logger) *mcp.Server {
	return mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{
			Instructions:              serverInstructions,
			Logger:                    logger,
			SupportedProtocolVersions: supportedProtocolVersions,
		},
	)
}

func run(ctx context.Context, logger *slog.Logger) error {
	logger.Info("starting server", "name", serverName, "version", serverVersion)
	return newServer(logger).Run(ctx, &mcp.StdioTransport{})
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}
