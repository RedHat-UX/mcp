// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Command rhds-mcp serves the Red Hat Design System MCP server over stdio.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/tools"
)

const serverName = "rhds-mcp"

// serverVersion is overridden at build time with
// -ldflags "-X main.serverVersion=<version>".
var serverVersion = "0.0.1"

// supportedProtocolVersions is advertised newest first and mirrors the
// negotiated protocol matrix in docs/PLAN.md section 8.1.
var supportedProtocolVersions = []string{
	"2026-07-28",
	"2025-11-25",
	"2025-06-18",
}

func serverInstructions() string {
	return fmt.Sprintf(`RHDS MCP server for the Red Hat Design System.

Supported protocol versions: %s.

The server is offline by default. It reads versioned RHDS data embedded in the
binary and makes no network requests at runtime.

Version resolution order, first hit wins: an explicit version argument, a
workspace lockfile, an installed @rhds/elements package, project package.json
dependencies, a bounded scan of HTML and Twig templates, then the latest
bundled stable version with a warning.

Use search for discovery and exact lookup when the element, token, or topic is
already known. Every response carries the resolved versions and warnings.`,
		strings.Join(supportedProtocolVersions, ", "))
}

func newServer(logger *slog.Logger, service *tools.Service) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{
			Instructions:              serverInstructions(),
			Logger:                    logger,
			SupportedProtocolVersions: supportedProtocolVersions,
		},
	)
	tools.Register(server, service)
	return server
}

// loadService opens the embedded data index. Reading the index is the only
// embedded-data work performed at startup; artifacts are loaded lazily.
func loadService(logger *slog.Logger) *tools.Service {
	dist, err := fs.Sub(data.Embedded(), "dist")
	if err != nil {
		logger.Error("opening embedded data tree", "error", err)
		return tools.New(nil, err).WithLogger(logger)
	}
	store, err := data.Open(dist)
	switch {
	case errors.Is(err, data.ErrNoData):
		logger.Warn("starting without bundled RHDS data",
			"hint", "build with generated data: make data build")
	case err != nil:
		logger.Error("reading bundled data index", "error", err)
	}
	return tools.New(store, err).WithLogger(logger)
}

func run(ctx context.Context, logger *slog.Logger, service *tools.Service) error {
	logger.Info("starting server", "name", serverName, "version", serverVersion)
	return newServer(logger, service).Run(ctx, &mcp.StdioTransport{})
}

// exitCode maps an error returned by run to a process exit code. A canceled
// context is the normal shutdown path for an interrupt or SIGTERM.
func exitCode(err error) int {
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	return 1
}

// cleanShutdownHandler demotes the SDK's cancellation error to INFO. The SDK
// logs "server run cancelled" at ERROR when the signal context fires, but an
// interrupt or SIGTERM is the normal shutdown path and must not look like a
// failure to log collectors.
type cleanShutdownHandler struct {
	slog.Handler
}

func (h cleanShutdownHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level == slog.LevelError && record.Message == "server run cancelled" {
		var err error
		record.Attrs(func(attribute slog.Attr) bool {
			if attribute.Key == "error" {
				err, _ = attribute.Value.Any().(error)
				return false
			}
			return true
		})
		if errors.Is(err, context.Canceled) {
			record.Level = slog.LevelInfo
		}
	}
	return h.Handler.Handle(ctx, record)
}

func (h cleanShutdownHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return cleanShutdownHandler{h.Handler.WithAttrs(attrs)}
}

func (h cleanShutdownHandler) WithGroup(name string) slog.Handler {
	return cleanShutdownHandler{h.Handler.WithGroup(name)}
}

func main() {
	logger := slog.New(cleanShutdownHandler{slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})})
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	service := loadService(logger)
	err := run(ctx, logger, service)
	code := exitCode(err)
	switch {
	case err == nil:
	case code == 0:
		logger.Info("server stopped", "reason", err)
	default:
		logger.Error("server exited", "error", err)
	}

	stop()
	if code != 0 {
		os.Exit(code)
	}
}
