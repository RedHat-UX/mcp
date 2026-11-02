// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 11 requires a roots/elicitation protocol matrix; the SDK marks roots deprecated but keeps it functional.

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/mod/semver"
)

// provenanceMetaKey is the tool, resource, and template _meta key carrying the
// upstream source and the version this binary bundled for it.
const provenanceMetaKey = "rhds-mcp/upstream"

// upstreamProvenance records which upstream supplied a mounted entry.
type upstreamProvenance struct {
	source  string
	version string
}

// upstreamDefinitions is the surface captured from an upstream through its
// internal MCP client.
type upstreamDefinitions struct {
	tools     []*mcp.Tool
	resources []*mcp.Resource
	templates []*mcp.ResourceTemplate
}

// upstreamForwarder sends a proxied request to the upstream implementation.
// Implementations must preserve the host request's cancellation by passing ctx
// through to the upstream client.
type upstreamForwarder interface {
	callTool(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error)
	readResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error)
}

// upstreamSource is one upstream surface together with the bundled version
// reported for it.
type upstreamSource struct {
	name        string
	version     string
	definitions upstreamDefinitions
	forward     upstreamForwarder
}

// ConflictPolicy selects how a mounted entry that collides with an existing
// host entry is resolved.
type ConflictPolicy string

const (
	// ConflictNewest replaces an entry only when the bundled version is newer.
	// Entries with equal versions are kept without a warning, newer host
	// entries are kept, and entries without usable provenance are replaced.
	// This is the default.
	ConflictNewest ConflictPolicy = "newest"
	// ConflictReplace always replaces an existing entry.
	ConflictReplace ConflictPolicy = "replace"
	// ConflictSkip always keeps an existing entry.
	ConflictSkip ConflictPolicy = "skip"
	// ConflictError fails the mount instead of resolving any collision.
	ConflictError ConflictPolicy = "error"
)

// normalizeConflictPolicy maps the zero value to the default and rejects
// unknown policy strings.
func normalizeConflictPolicy(policy ConflictPolicy) (ConflictPolicy, error) {
	switch policy {
	case "":
		return ConflictNewest, nil
	case ConflictNewest, ConflictReplace, ConflictSkip, ConflictError:
		return policy, nil
	default:
		return "", fmt.Errorf("unknown upstream conflict policy %q", policy)
	}
}

// UpstreamConflict describes one collision between a bundled entry and an
// entry already registered on the host server. Kind is one of
// [ConflictKindTool], [ConflictKindResource], or [ConflictKindTemplate].
type UpstreamConflict struct {
	Kind            string
	Source          string
	Name            string
	BundledVersion  string
	ExistingVersion string
}

// Kind values reported by [UpstreamConflict].
const (
	ConflictKindTool     = "tool"
	ConflictKindResource = "resource"
	ConflictKindTemplate = "template"
)

// UpstreamConflictError reports collisions under [ConflictError].
type UpstreamConflictError struct {
	Conflicts []UpstreamConflict
}

// Error implements error.
func (e *UpstreamConflictError) Error() string {
	if e == nil || len(e.Conflicts) == 0 {
		return "upstream registration conflict"
	}
	parts := make([]string, 0, len(e.Conflicts))
	for _, conflict := range e.Conflicts {
		parts = append(parts, fmt.Sprintf("%s %q from %s", conflict.Kind, conflict.Name, conflict.Source))
	}
	return "upstream registration conflicts with existing entries: " + strings.Join(parts, ", ")
}

// conflictAction is the outcome of one conflict decision.
type conflictAction int

const (
	actionRegister conflictAction = iota
	actionReplace
	actionKeep
)

// conflictReason explains a decision so the warning text stays consistent.
type conflictReason string

const (
	reasonOlder         conflictReason = "older"
	reasonShadowed      conflictReason = "shadowed"
	reasonEqual         conflictReason = "equal"
	reasonUnknown       conflictReason = "unknown"
	reasonIncomparable  conflictReason = "different-upstream"
	reasonPolicyReplace conflictReason = "policy-replace"
	reasonPolicyKeep    conflictReason = "policy-keep"
)

type conflictDecision struct {
	action          conflictAction
	reason          conflictReason
	existingVersion string
}

// decideConflict applies policy to one collision. exists reports whether the
// host already has an entry with the same name or URI; existing is its
// provenance when the entry carries ours. Versions from different upstreams
// are not comparable and take the unknown-version path.
func decideConflict(policy ConflictPolicy, source, bundledVersion string, exists bool, existing *upstreamProvenance) conflictDecision {
	if !exists {
		return conflictDecision{action: actionRegister}
	}
	existingVersion := existingVersionOrUnknown(existing)

	if existing == nil || canonicalVersion(existing.version) == "" {
		if policy == ConflictSkip {
			return conflictDecision{action: actionKeep, reason: reasonUnknown, existingVersion: existingVersion}
		}
		return conflictDecision{action: actionReplace, reason: reasonUnknown, existingVersion: existingVersion}
	}

	switch policy {
	case ConflictReplace:
		return conflictDecision{action: actionReplace, reason: reasonPolicyReplace, existingVersion: existingVersion}
	case ConflictSkip:
		return conflictDecision{action: actionKeep, reason: reasonPolicyKeep, existingVersion: existingVersion}
	}

	if existing.source != source {
		// Versions from different modules are not comparable. Treat the
		// existing entry as unknown and let the default policy replace it.
		return conflictDecision{action: actionReplace, reason: reasonIncomparable, existingVersion: existingVersion}
	}

	bundledCanonical := canonicalVersion(bundledVersion)
	if bundledCanonical == "" {
		return conflictDecision{action: actionReplace, reason: reasonUnknown, existingVersion: existingVersion}
	}

	switch semver.Compare(bundledCanonical, canonicalVersion(existing.version)) {
	case -1:
		return conflictDecision{action: actionKeep, reason: reasonShadowed, existingVersion: existingVersion}
	case 1:
		return conflictDecision{action: actionReplace, reason: reasonOlder, existingVersion: existingVersion}
	default:
		return conflictDecision{action: actionKeep, reason: reasonEqual, existingVersion: existingVersion}
	}
}

// canonicalVersion returns a semver-canonical form, accepting versions with or
// without a leading v. An unknown, devel, empty, or malformed version returns
// the empty string, which callers treat as unusable for comparison.
func canonicalVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || version == unknownUpstreamVersion || version == "(devel)" {
		return ""
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	if !semver.IsValid(version) {
		return ""
	}
	return semver.Canonical(version)
}

func existingVersionOrUnknown(existing *upstreamProvenance) string {
	if existing == nil || canonicalVersion(existing.version) == "" {
		return unknownUpstreamVersion
	}
	return existing.version
}

func existingSourceOrUnknown(existing *upstreamProvenance) string {
	if existing == nil || existing.source == "" {
		return unknownUpstreamVersion
	}
	return existing.source
}

// provenanceFromMeta reads our provenance key from an entry's _meta. The value
// is always a JSON object: definitions cross the in-memory client wire before
// they reach the host surface.
func provenanceFromMeta(meta map[string]any) (upstreamProvenance, bool) {
	raw, ok := meta[provenanceMetaKey]
	if !ok {
		return upstreamProvenance{}, false
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return upstreamProvenance{}, false
	}
	source, _ := value["source"].(string)
	version, _ := value["version"].(string)
	return upstreamProvenance{source: source, version: version}, true
}

func cloneMeta(meta map[string]any) mcp.Meta {
	if len(meta) == 0 {
		return nil
	}
	cloned := make(mcp.Meta, len(meta))
	maps.Copy(cloned, meta)
	return cloned
}

// withUpstreamProvenance returns meta with the upstream source and version
// recorded under [provenanceMetaKey]. The input map is never mutated.
func withUpstreamProvenance(meta map[string]any, source, version string) mcp.Meta {
	cloned := cloneMeta(meta)
	if cloned == nil {
		cloned = make(mcp.Meta, 1)
	}
	cloned[provenanceMetaKey] = map[string]any{
		"source":  source,
		"version": version,
	}
	return cloned
}

func cloneTool(tool *mcp.Tool) *mcp.Tool {
	if tool == nil {
		return nil
	}
	cloned := *tool
	cloned.Meta = cloneMeta(tool.Meta)
	return &cloned
}

func cloneResource(resource *mcp.Resource) *mcp.Resource {
	if resource == nil {
		return nil
	}
	cloned := *resource
	cloned.Meta = cloneMeta(resource.Meta)
	return &cloned
}

func cloneTemplate(template *mcp.ResourceTemplate) *mcp.ResourceTemplate {
	if template == nil {
		return nil
	}
	cloned := *template
	cloned.Meta = cloneMeta(template.Meta)
	return &cloned
}

// captureSurface enumerates every tool, resource, and template through the
// client, following pagination cursors, and captures each definition verbatim.
func captureSurface(ctx context.Context, client *mcp.ClientSession) (upstreamDefinitions, error) {
	var definitions upstreamDefinitions
	if client == nil {
		return definitions, fmt.Errorf("upstream client must not be nil")
	}

	for cursor := ""; ; {
		result, err := client.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return definitions, fmt.Errorf("list upstream tools: %w", err)
		}
		for _, tool := range result.Tools {
			if tool != nil {
				definitions.tools = append(definitions.tools, cloneTool(tool))
			}
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}

	for cursor := ""; ; {
		result, err := client.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return definitions, fmt.Errorf("list upstream resources: %w", err)
		}
		for _, resource := range result.Resources {
			if resource != nil {
				definitions.resources = append(definitions.resources, cloneResource(resource))
			}
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}

	for cursor := ""; ; {
		result, err := client.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return definitions, fmt.Errorf("list upstream resource templates: %w", err)
		}
		for _, template := range result.ResourceTemplates {
			if template != nil {
				definitions.templates = append(definitions.templates, cloneTemplate(template))
			}
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}

	return definitions, nil
}

// hostSurface is the set of entries already registered on the host server,
// keyed by name or URI.
type hostSurface struct {
	tools     map[string]*mcp.Tool
	resources map[string]*mcp.Resource
	templates map[string]*mcp.ResourceTemplate
}

// captureHostSurface reads the host's current surface through a short-lived
// in-memory client. The SDK exposes no direct registry query.
func captureHostSurface(ctx context.Context, host *mcp.Server) (*hostSurface, error) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := host.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect host probe server: %w", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "rhds-mcp-host-probe", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return nil, fmt.Errorf("connect host probe client: %w", err)
	}
	defer func() { _ = clientSession.Close() }()

	definitions, err := captureSurface(ctx, clientSession)
	if err != nil {
		return nil, err
	}
	surface := &hostSurface{
		tools:     make(map[string]*mcp.Tool, len(definitions.tools)),
		resources: make(map[string]*mcp.Resource, len(definitions.resources)),
		templates: make(map[string]*mcp.ResourceTemplate, len(definitions.templates)),
	}
	for _, tool := range definitions.tools {
		surface.tools[tool.Name] = tool
	}
	for _, resource := range definitions.resources {
		surface.resources[resource.URI] = resource
	}
	for _, template := range definitions.templates {
		surface.templates[template.URITemplate] = template
	}
	return surface, nil
}

// plannedEntry is one host registration deferred until the whole mount plan
// has been resolved, so [ConflictError] can leave the host untouched.
type plannedEntry struct {
	register func(*mcp.Server)
}

// mountSources registers sources on host, resolving collisions against the
// host's existing surface and against entries planned by earlier sources.
//
// With [ConflictError], a single collision aborts the whole call before any
// entry is registered.
func mountSources(host *mcp.Server, sources []upstreamSource, policy ConflictPolicy, logger *slog.Logger) error {
	if host == nil {
		return fmt.Errorf("server must not be nil")
	}
	normalized, err := normalizeConflictPolicy(policy)
	if err != nil {
		return err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if len(sources) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	surface, err := captureHostSurface(ctx, host)
	if err != nil {
		return fmt.Errorf("inspect host surface: %w", err)
	}

	var (
		plan      []plannedEntry
		conflicts []UpstreamConflict
	)

	consider := func(
		kind string,
		name string,
		source upstreamSource,
		exists bool,
		existing *upstreamProvenance,
		commit func(),
		register func(*mcp.Server),
	) {
		if exists {
			conflicts = append(conflicts, UpstreamConflict{
				Kind:            kind,
				Source:          source.name,
				Name:            name,
				BundledVersion:  source.version,
				ExistingVersion: existingVersionOrUnknown(existing),
			})
		}
		if normalized == ConflictError && exists {
			return
		}

		decision := decideConflict(normalized, source.name, source.version, exists, existing)
		switch decision.action {
		case actionRegister, actionReplace:
			commit()
			plan = append(plan, plannedEntry{register: register})
			if exists {
				logConflictDecision(logger, decision, kind, name, source, existing)
			}
		case actionKeep:
			if decision.reason != reasonEqual {
				logConflictDecision(logger, decision, kind, name, source, existing)
			}
		}
	}

	for _, source := range sources {
		if source.forward == nil {
			return fmt.Errorf("upstream %q has no forwarder", source.name)
		}
		for _, tool := range source.definitions.tools {
			if tool == nil {
				continue
			}
			existing := surface.tools[tool.Name]
			var existingProvenance *upstreamProvenance
			if existing != nil {
				if provenance, ok := provenanceFromMeta(existing.GetMeta()); ok {
					existingProvenance = &provenance
				}
			}

			definition := cloneTool(tool)
			definition.Meta = withUpstreamProvenance(definition.Meta, source.name, source.version)
			forward := source.forward
			consider(ConflictKindTool, tool.Name, source, existing != nil, existingProvenance,
				func() { surface.tools[tool.Name] = definition },
				func(host *mcp.Server) {
					host.AddTool(definition, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						return forward.callTool(ctx, request)
					})
				})
		}
		for _, resource := range source.definitions.resources {
			if resource == nil {
				continue
			}
			existing := surface.resources[resource.URI]
			var existingProvenance *upstreamProvenance
			if existing != nil {
				if provenance, ok := provenanceFromMeta(existing.GetMeta()); ok {
					existingProvenance = &provenance
				}
			}

			definition := cloneResource(resource)
			definition.Meta = withUpstreamProvenance(definition.Meta, source.name, source.version)
			forward := source.forward
			consider(ConflictKindResource, resource.URI, source, existing != nil, existingProvenance,
				func() { surface.resources[resource.URI] = definition },
				func(host *mcp.Server) {
					host.AddResource(definition, func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
						return forward.readResource(ctx, request)
					})
				})
		}
		for _, template := range source.definitions.templates {
			if template == nil {
				continue
			}
			existing := surface.templates[template.URITemplate]
			var existingProvenance *upstreamProvenance
			if existing != nil {
				if provenance, ok := provenanceFromMeta(existing.GetMeta()); ok {
					existingProvenance = &provenance
				}
			}

			definition := cloneTemplate(template)
			definition.Meta = withUpstreamProvenance(definition.Meta, source.name, source.version)
			forward := source.forward
			consider(ConflictKindTemplate, template.URITemplate, source, existing != nil, existingProvenance,
				func() { surface.templates[template.URITemplate] = definition },
				func(host *mcp.Server) {
					host.AddResourceTemplate(definition, func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
						return forward.readResource(ctx, request)
					})
				})
		}
	}

	if normalized == ConflictError && len(conflicts) > 0 {
		return &UpstreamConflictError{Conflicts: conflicts}
	}

	for _, entry := range plan {
		entry.register(host)
	}
	return nil
}

// logConflictDecision emits exactly one warning per resolved collision.
func logConflictDecision(
	logger *slog.Logger,
	decision conflictDecision,
	kind string,
	name string,
	source upstreamSource,
	existing *upstreamProvenance,
) {
	action := "replacing"
	if decision.action == actionKeep {
		action = "keeping"
	}

	var message string
	switch decision.reason {
	case reasonOlder:
		message = "upstream conflict: replacing older " + kind
	case reasonShadowed:
		message = "upstream conflict: bundled " + kind + " shadowed by newer existing entry"
	case reasonUnknown:
		message = "upstream conflict: " + action + " " + kind + " with unknown version"
	case reasonIncomparable:
		message = "upstream conflict: " + action + " " + kind + " from a different upstream"
	case reasonPolicyReplace:
		message = "upstream conflict: replacing " + kind + " by policy"
	case reasonPolicyKeep:
		message = "upstream conflict: keeping existing " + kind + " by policy"
	default:
		message = "upstream conflict: " + action + " " + kind
	}

	logger.Warn(message,
		"source", source.name,
		"kind", kind,
		"name", name,
		"bundledVersion", source.version,
		"existingVersion", decision.existingVersion,
		"existingSource", existingSourceOrUnknown(existing),
	)
}
