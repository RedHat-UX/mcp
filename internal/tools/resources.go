// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
)

// migrationsResourceURI is the curated cross-major token migration document.
const migrationsResourceURI = "rhds://migrations/tokens"

type resourceEntry struct {
	artifact data.Artifact
	mimeType string
}

// RegisterResources adds one resource per versioned artifact: elements
// manifests, token files, docs pages, and release notes. The curated token
// migrations are always available, even in a build without generated data.
func RegisterResources(server *mcp.Server, svc *Service) {
	server.AddResource(&mcp.Resource{
		URI:         migrationsResourceURI,
		Name:        "tokens-migrations",
		Title:       "Curated cross-major token migrations",
		Description: "Token renames, removals, and deprecations across @rhds/tokens majors, with provenance and curation notes.",
		MIMEType:    "application/json",
	}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return textResource(request.Params.URI, "application/json", data.TokenMigrationsJSON()), nil
	})

	if svc == nil || svc.store == nil {
		return
	}

	entries := make(map[string]resourceEntry)
	for _, artifact := range svc.store.Index().Artifacts {
		uri, mimeType, ok := resourceForArtifact(artifact)
		if !ok {
			continue
		}
		if _, exists := entries[uri]; exists {
			continue
		}
		entries[uri] = resourceEntry{artifact: artifact, mimeType: mimeType}
		entry := entries[uri]
		server.AddResource(&mcp.Resource{
			URI:         uri,
			Name:        resourceName(entry.artifact),
			Title:       resourceName(entry.artifact),
			Description: resourceDescription(entry.artifact),
			MIMEType:    entry.mimeType,
		}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			payload, err := svc.store.Read(entry.artifact.Package, entry.artifact.Version, entry.artifact.Kind)
			if err != nil {
				return nil, mcp.ResourceNotFoundError(request.Params.URI)
			}
			return textResource(request.Params.URI, entry.mimeType, payload), nil
		})
	}
}

func textResource(uri, mimeType string, payload []byte) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI:      uri,
		MIMEType: mimeType,
		Text:     string(payload),
	}}}
}

func resourceForArtifact(artifact data.Artifact) (uri, mimeType string, ok bool) {
	switch artifact.Package {
	case packageElements:
		return fmt.Sprintf("rhds://elements/%s/%s", artifact.Version, artifact.Kind), "application/json", true
	case packageTokens:
		return fmt.Sprintf("rhds://tokens/%s/%s", artifact.Version, artifact.Kind), "application/json", true
	case packageDocs:
		if strings.HasPrefix(artifact.Kind, "doc:") {
			page := strings.TrimPrefix(artifact.Kind, "doc:")
			return "rhds://docs/" + artifact.Version + "/" + escapePath(page), "text/markdown", true
		}
		return fmt.Sprintf("rhds://docs/%s/%s.json", artifact.Version, artifact.Kind), "application/json", true
	case packageReleaseNotes:
		if artifact.Kind == kindReleaseNotes {
			return "rhds://release-notes/" + artifact.Version, "text/markdown", true
		}
		return fmt.Sprintf("rhds://release-notes/%s/%s", artifact.Version, artifact.Kind), "text/markdown", true
	default:
		return "", "", false
	}
}

func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func resourceName(artifact data.Artifact) string {
	if strings.HasPrefix(artifact.Kind, "doc:") {
		return artifact.Version + " " + strings.TrimPrefix(artifact.Kind, "doc:")
	}
	return artifact.Package + "@" + artifact.Version + " " + artifact.Kind
}

func resourceDescription(artifact data.Artifact) string {
	switch artifact.Package {
	case packageElements:
		return "Custom Elements Manifest for @rhds/elements " + artifact.Version
	case packageTokens:
		if artifact.Kind == kindThemes {
			return "Light and dark theme map for @rhds/tokens " + artifact.Version
		}
		return "Normalized DTCG tokens for @rhds/tokens " + artifact.Version
	case packageDocs:
		return "RHDS documentation snapshot " + artifact.Version
	case packageReleaseNotes:
		return "Release notes for @rhds/elements " + artifact.Version
	default:
		return artifact.Package + " " + artifact.Version
	}
}
