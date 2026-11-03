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

// buildInstructions derives the version statement from the negotiated
// protocol list and the bundled data index. It never repeats a second literal
// version list.
func buildInstructions(protocols []string, service *tools.Service) string {
	var builder strings.Builder
	builder.WriteString("RHDS MCP server for the Red Hat Design System.\n\n")
	builder.WriteString("Supported protocol versions: ")
	builder.WriteString(strings.Join(protocols, ", "))
	builder.WriteString(".\n\n")

	elements, tokens := service.SupportedVersions()
	if len(elements) > 0 && len(tokens) > 0 {
		fmt.Fprintf(&builder,
			"Bundled stable versions: @rhds/elements %s through %s (%d versions); @rhds/tokens %s through %s (%d versions). Call rhds_resolve_version to list every candidate.\n\n",
			elements[len(elements)-1], elements[0], len(elements),
			tokens[len(tokens)-1], tokens[0], len(tokens))
	} else {
		builder.WriteString("This build has no bundled RHDS data; data tools return no_bundled_data. Build with generated data: make data build.\n\n")
	}

	builder.WriteString("The server is offline by default. It reads versioned RHDS data embedded in the binary and makes no network requests at runtime.\n\n")
	builder.WriteString("Version resolution order, first hit wins: an explicit version argument, a workspace lockfile, an installed @rhds/elements package, project package.json dependencies, a bounded scan of HTML and Twig templates, then the latest bundled stable version with a warning.\n\n")
	builder.WriteString("Use search for discovery. Use exact lookup when the element, token, or topic is already known: rhds_get_component_api, rhds_query_tokens with token, and rhds_get_design_guidelines with element. Every response carries the resolved versions and warnings; tools respect the response budget and report truncation or provide a follow-up control instead of truncating silently.\n\n")
	builder.WriteString("Resources use progressive disclosure. Read rhds://packages/ for bundled package versions, kinds, counts, and the resolved workspace version. Artifact data is available through four template families: rhds://data/elements{/path*}, rhds://data/tokens{/path*}, rhds://data/docs{/path*}, and rhds://data/release-notes{/path*}. Each family has a no-query form for workspace resolution, such as rhds://data/docs, and a query form for a pinned version, such as rhds://data/docs?version=5.0.0. An empty path reads the package default: the manifest for elements, tokens for tokens, the docs index for docs, and release notes for release-notes. Read rhds://data/tokens/themes for the theme map and rhds://data/docs/patterns.json for patterns. Read a Markdown docs page by its path, for example rhds://data/docs/get-started/introduction.md?version=5.0.0. Pass ?version= to pin a bundled version, with an optional v prefix for docs tags. Component sections keep their existing rhds://elements/{version}/components/{tag}/{section} URI.")
	return builder.String()
}

// serverInstructions returns the instructions for a build without bundled data.
func serverInstructions() string {
	return buildInstructions(supportedProtocolVersions, nil)
}

func newServer(logger *slog.Logger, service *tools.Service) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{
			Instructions:              buildInstructions(supportedProtocolVersions, service),
			Logger:                    logger,
			SupportedProtocolVersions: supportedProtocolVersions,
		},
	)
	tools.Register(server, service)
	// Mount upstream surfaces first so the RHDS host registrations below win
	// any name or URI collision under the SDK's default conflict policy.
	if err := tools.MountUpstreams(server, tools.MountOptions{}); err != nil {
		// A skipped upstream surface is advisory: the RHDS tools still serve.
		logger.Warn("upstream surface not mounted", "error", err)
	}
	tools.RegisterPrompts(server, service)
	tools.RegisterResources(server, service)
	tools.RegisterComponentResources(server, service)
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

// versionRequested reports whether args ask for the build version. The flag
// runs without a server and without reading embedded data.
func versionRequested(args []string) bool {
	if len(args) != 2 {
		return false
	}
	return args[1] == "--version" || args[1] == "-version"
}

func main() {
	if versionRequested(os.Args) {
		fmt.Println(serverVersion)
		return
	}

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
