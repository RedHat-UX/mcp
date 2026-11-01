// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"fmt"
	"log/slog"
	"os"

	asimfs "bennypowers.dev/asimonim/fs"
	asimmcp "bennypowers.dev/asimonim/mcp"
	cemmcp "bennypowers.dev/cem/mcp"
	cemworkspace "bennypowers.dev/cem/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MountOptions controls which upstream surfaces are exposed. Each list holds
// exact tool names, resource URIs, or URI templates. Matching is exact, not by
// prefix or glob, so a caller removes precisely the entries it names.
type MountOptions struct {
	DisableTools     []string
	DisableResources []string
	DisableTemplates []string
}

// MountWarning reports an upstream surface that could not be mounted. It is
// advisory: the server must still start and serve the surfaces that did mount.
type MountWarning struct {
	// Package names the upstream whose surface was skipped, for example "cem".
	Package string
	// Err is the underlying construction or registration failure.
	Err error
}

func (w *MountWarning) Error() string {
	if w == nil {
		return ""
	}
	return fmt.Sprintf("skipped %s upstream tools: %v", w.Package, w.Err)
}

func (w *MountWarning) Unwrap() error {
	if w == nil {
		return nil
	}
	return w.Err
}

// MountUpstreams registers cem's and asimonim's tools, resources, and resource
// templates on server.
//
// cem's surface is mounted for the process working directory when that
// directory holds a custom elements manifest. A missing or unusable workspace
// skips cem without failing startup: the skip is logged to stderr through slog
// and returned as a *MountWarning. asimonim is always mounted with a nil
// config, so each MCP session resolves its own config from the client's roots.
//
// Disable lists are applied after mounting. Diagnostics go to stderr only.
func MountUpstreams(server *mcp.Server, opts MountOptions) error {
	if server == nil {
		return fmt.Errorf("server must not be nil")
	}

	cwd, getwdErr := os.Getwd()
	var warning error
	switch {
	case getwdErr != nil:
		warning = &MountWarning{Package: "cem", Err: getwdErr}
		slog.Warn("skipping cem upstream tools", "error", getwdErr)
	default:
		if err := mountCEM(server, cwd); err != nil {
			warning = err
			slog.Warn("skipping cem upstream tools", "error", err)
		}
	}

	asimmcp.Register(server, asimfs.NewOSFileSystem(), nil, cwd)

	server.RemoveTools(opts.DisableTools...)
	server.RemoveResources(opts.DisableResources...)
	server.RemoveResourceTemplates(opts.DisableTemplates...)

	return warning
}

// mountCEM registers cem's surface when cwd has a custom elements manifest.
//
// A manifest that exists but fails to decode is left to cem's handlers, which
// report per-request errors: only a missing manifest skips mounting.
func mountCEM(server *mcp.Server, cwd string) error {
	workspace, err := cemworkspace.NewFileSystemWorkspaceContext(cwd)
	if err != nil {
		return &MountWarning{Package: "cem", Err: err}
	}

	manifestPath := workspace.CustomElementsManifestPath()
	if manifestPath == "" {
		slog.Warn("skipping cem upstream tools", "reason", "no custom elements manifest", "root", cwd)
		return nil
	}
	if _, err := workspace.Stat(manifestPath); err != nil {
		slog.Warn("skipping cem upstream tools",
			"reason", "custom elements manifest is missing",
			"path", manifestPath, "error", err)
		return nil
	}

	if err := cemmcp.Register(server, workspace, cemmcp.ServerConfig{}); err != nil {
		return &MountWarning{Package: "cem", Err: err}
	}
	return nil
}
