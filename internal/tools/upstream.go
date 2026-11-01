// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"errors"
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
// prefix or glob, so a caller excludes precisely the entries it names.
//
// The lists are mapped to each upstream's registration Selection before
// anything is registered, so an excluded entry is never mounted. Entries that
// name no item of a given upstream are ignored, which lets one deny list cover
// both upstreams and stay stable as their surfaces change. Only upstream
// surfaces are affected: a host tool, resource, or template that shares a name
// with a denied entry is left alone.
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
// Disable lists are passed to each upstream as registration exclusions, so the
// excluded surface is never registered. Both upstreams use their default
// conflict policy (ConflictReplace), matching the behavior before registration
// options existed. Diagnostics go to stderr only.
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
		if err := mountCEM(server, cwd, opts); err != nil {
			warning = err
			slog.Warn("skipping cem upstream tools", "error", err)
		}
	}

	if _, err := asimmcp.Register(server, asimfs.NewOSFileSystem(), nil, cwd, asimonimRegisterOptions(opts)...); err != nil {
		slog.Warn("skipping asimonim upstream tools", "error", err)
		warning = errors.Join(warning, &MountWarning{Package: "asimonim", Err: err})
	}

	return warning
}

// cemRegisterOptions maps MountOptions to cem's registration selection.
//
// Empty names are dropped before they reach downstream: cem rejects them as
// invalid selections, while the previous remove-by-name path treated them as a
// no-op. Dropping them keeps a malformed deny list from failing startup.
func cemRegisterOptions(opts MountOptions) []cemmcp.RegisterOption {
	return []cemmcp.RegisterOption{
		cemmcp.WithTools(cemmcp.Selection{Exclude: nonEmpty(opts.DisableTools)}),
		cemmcp.WithResources(cemmcp.Selection{Exclude: nonEmpty(opts.DisableResources)}),
		cemmcp.WithTemplates(cemmcp.Selection{Exclude: nonEmpty(opts.DisableTemplates)}),
	}
}

// asimonimRegisterOptions maps MountOptions to asimonim's registration
// selection. See cemRegisterOptions for the empty-name handling.
func asimonimRegisterOptions(opts MountOptions) []asimmcp.RegisterOption {
	return []asimmcp.RegisterOption{
		asimmcp.WithTools(asimmcp.Selection{Exclude: nonEmpty(opts.DisableTools)}),
		asimmcp.WithResources(asimmcp.Selection{Exclude: nonEmpty(opts.DisableResources)}),
		asimmcp.WithTemplates(asimmcp.Selection{Exclude: nonEmpty(opts.DisableTemplates)}),
	}
}

// nonEmpty returns the non-empty names in names, preserving order.
func nonEmpty(names []string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

// mountCEM registers cem's surface when cwd has a custom elements manifest.
//
// A manifest that exists but fails to decode is left to cem's handlers, which
// report per-request errors: only a missing manifest skips mounting.
func mountCEM(server *mcp.Server, cwd string, opts MountOptions) error {
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

	// The Registration handle is discarded: no reload hook consumes it yet.
	// A future reload integration can return it from here instead.
	if _, err := cemmcp.Register(server, workspace, cemmcp.ServerConfig{}, cemRegisterOptions(opts)...); err != nil {
		return &MountWarning{Package: "cem", Err: err}
	}
	return nil
}
