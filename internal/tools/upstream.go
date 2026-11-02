// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 11 requires a roots/elicitation protocol matrix; the SDK marks roots deprecated but keeps it functional.

package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

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

	// ConflictPolicy controls how an upstream entry that collides with an
	// existing host entry is resolved. The zero value means [ConflictNewest].
	ConflictPolicy ConflictPolicy
}

// MountWarning reports an upstream surface that could not be mounted. It is
// advisory: the returned Mount is still usable and the server must still start
// and serve the surfaces that did mount. It is the only error MountUpstreams
// returns alongside a non-nil Mount; use errors.As to distinguish it from a
// fatal mount failure.
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
	return fmt.Sprintf("skipped %s upstream surface: %v", w.Package, w.Err)
}

func (w *MountWarning) Unwrap() error {
	if w == nil {
		return nil
	}
	return w.Err
}

// Mount owns the internal servers and clients behind a mounted upstream
// surface. Close releases them when the host server stops; it is safe to call
// more than once.
type Mount struct {
	state *mountState
}

// Close releases the internal sessions owned by the mount.
func (m *Mount) Close() error {
	if m == nil || m.state == nil {
		return nil
	}
	return m.state.close()
}

// activeSessions reports how many per-host-session internal clients are
// currently open. It exists for lifecycle tests.
func (m *Mount) activeSessions() int {
	if m == nil || m.state == nil {
		return 0
	}
	return m.state.activeSessions()
}

// mountState tracks the internal sessions owned by one mount.
type mountState struct {
	mu       sync.Mutex
	closed   bool
	sessions map[*mcp.ServerSession]*asimonimSession
	static   []io.Closer
}

func newMountState() *mountState {
	return &mountState{sessions: make(map[*mcp.ServerSession]*asimonimSession)}
}

// own transfers closer to the mount. If the mount is already closed, the
// closer is closed immediately.
func (s *mountState) own(closers ...io.Closer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, closer := range closers {
		if closer == nil {
			continue
		}
		if s.closed {
			_ = closer.Close()
			continue
		}
		s.static = append(s.static, closer)
	}
}

func (s *mountState) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sessions := make([]*asimonimSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	clear(s.sessions)
	static := s.static
	s.static = nil
	s.mu.Unlock()

	for _, session := range sessions {
		session.close()
	}
	for _, closer := range static {
		_ = closer.Close()
	}
	return nil
}

func (s *mountState) activeSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// MountUpstreams mounts cem's and asimonim's tools, resources, and resource
// templates on server through an in-memory proxy.
//
// Each upstream is registered on an internal MCP server; its surface is
// enumerated through an internal client and re-registered on server with
// [provenanceMetaKey] metadata. Proxied handlers forward calls and reads to the
// internal client, preserving content, structured content, _meta, errors, and
// cancellation.
//
// cem's surface is mounted for the process working directory when that
// directory holds a custom elements manifest. A missing or unusable workspace
// skips cem without failing startup. asimonim keeps one internal client per
// host session, created lazily with that session's MCP roots, so clients with
// different roots resolve different token workspaces.
//
// Disable lists are passed to each upstream as registration exclusions, so the
// excluded surface is never registered. Collisions with existing host entries
// are resolved by opts.ConflictPolicy. Diagnostics go to stderr only.
//
// The returned error is a *MountWarning when an upstream could not be mounted
// but the host server is still usable; any other error is fatal to mounting
// upstreams, and the returned Mount is then nil.
func MountUpstreams(server *mcp.Server, opts MountOptions) (*Mount, error) {
	if server == nil {
		return nil, fmt.Errorf("server must not be nil")
	}
	policy, err := normalizeConflictPolicy(opts.ConflictPolicy)
	if err != nil {
		return nil, err
	}

	mount := &Mount{state: newMountState()}
	var sources []upstreamSource
	var warning error

	cwd, getwdErr := os.Getwd()
	switch {
	case getwdErr != nil:
		warning = &MountWarning{Package: "cem", Err: getwdErr}
		slog.Warn("skipping cem upstream tools", "error", getwdErr)
	default:
		source, err := buildCEMSource(cwd, opts, mount)
		switch {
		case err != nil:
			warning = errors.Join(warning, &MountWarning{Package: "cem", Err: err})
			slog.Warn("skipping cem upstream tools", "error", err)
		case source != nil:
			sources = append(sources, *source)
		}
	}

	source, err := buildAsimonimSource(cwd, opts, mount)
	switch {
	case err != nil:
		warning = errors.Join(warning, &MountWarning{Package: "asimonim", Err: err})
		slog.Warn("skipping asimonim upstream tools", "error", err)
	default:
		sources = append(sources, *source)
	}

	if len(sources) > 0 {
		if err := mountSources(server, sources, policy, slog.Default()); err != nil {
			_ = mount.Close()
			// Warnings were already logged. Returning them alongside a fatal
			// error would blur the advisory and fatal contracts.
			return nil, err
		}
	}
	return mount, warning
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

// buildCEMSource registers cem on an internal server bound to cwd and captures
// its surface. A missing manifest skips cem and returns a nil source.
func buildCEMSource(cwd string, opts MountOptions, mount *Mount) (*upstreamSource, error) {
	workspace, err := cemworkspace.NewFileSystemWorkspaceContext(cwd)
	if err != nil {
		return nil, err
	}

	manifestPath := workspace.CustomElementsManifestPath()
	if manifestPath == "" {
		slog.Warn("skipping cem upstream tools", "reason", "no custom elements manifest", "root", cwd)
		return nil, nil
	}
	if _, err := workspace.Stat(manifestPath); err != nil {
		slog.Warn("skipping cem upstream tools",
			"reason", "custom elements manifest is missing",
			"path", manifestPath, "error", err)
		return nil, nil
	}

	version := bundledUpstreamVersion(cemModulePath)
	internal := mcp.NewServer(&mcp.Implementation{Name: "cem-internal", Version: version}, nil)
	// The Registration handle is discarded: no reload hook consumes it yet.
	if _, err := cemmcp.Register(internal, workspace, cemmcp.ServerConfig{}, cemRegisterOptions(opts)...); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, serverSession, err := connectInternalClient(ctx, internal, "rhds-mcp-cem-proxy", "2025-11-25", nil, false)
	if err != nil {
		return nil, err
	}
	definitions, err := captureSurface(ctx, client)
	if err != nil {
		_ = client.Close()
		_ = serverSession.Close()
		return nil, err
	}
	mount.state.own(client, serverSession)

	return &upstreamSource{
		name:        "cem",
		version:     version,
		definitions: definitions,
		forward:     &clientForwarder{client: client},
	}, nil
}

// buildAsimonimSource registers asimonim on an internal server and captures
// its static surface. Per-host-session clients are created lazily by the
// returned forwarder.
func buildAsimonimSource(cwd string, opts MountOptions, mount *Mount) (*upstreamSource, error) {
	version := bundledUpstreamVersion(asimonimModulePath)
	internal := mcp.NewServer(&mcp.Implementation{Name: "asimonim-internal", Version: version}, nil)
	if _, err := asimmcp.Register(internal, asimfs.NewOSFileSystem(), nil, cwd, asimonimRegisterOptions(opts)...); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, serverSession, err := connectInternalClient(ctx, internal, "rhds-mcp-asimonim-capture", "2025-11-25", nil, false)
	if err != nil {
		return nil, err
	}
	definitions, err := captureSurface(ctx, client)
	_ = client.Close()
	_ = serverSession.Close()
	if err != nil {
		return nil, err
	}

	forwarder := &asimonimForwarder{
		state:   mount.state,
		server:  internal,
		version: version,
	}
	return &upstreamSource{
		name:        "asimonim",
		version:     version,
		definitions: definitions,
		forward:     forwarder,
	}, nil
}

// connectInternalClient connects an in-memory client to an internal server.
// The caller owns both returned sessions.
func connectInternalClient(
	ctx context.Context,
	server *mcp.Server,
	clientName string,
	protocol string,
	roots []*mcp.Root, //nolint:staticcheck // roots required by PLAN section 8.1
	advertiseRoots bool,
) (*mcp.ClientSession, *mcp.ServerSession, error) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("connect internal upstream server: %w", err)
	}

	options := &mcp.ClientOptions{}
	if advertiseRoots {
		options.Capabilities = &mcp.ClientCapabilities{
			RootsV2: &mcp.RootCapabilities{}, //nolint:staticcheck // roots required by PLAN section 8.1
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "0.0.0"}, options)
	if advertiseRoots {
		client.AddRoots(roots...) //nolint:staticcheck // roots required by PLAN section 8.1
	}
	clientSession, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if err != nil {
		_ = serverSession.Close()
		return nil, nil, fmt.Errorf("connect internal upstream client: %w", err)
	}
	return clientSession, serverSession, nil
}
