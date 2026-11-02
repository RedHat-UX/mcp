// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
)

// migrationsResourceURI is the curated cross-major token migration document.
const migrationsResourceURI = "rhds://migrations/tokens"

// RegisterResources adds the two small static resources and the four
// progressive-disclosure artifact template families. The curated token
// migrations and the packages index are available even in a build without
// generated data.
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

	registerPackagesResource(server, svc)
	registerArtifactResourceTemplates(server, svc)
}

func textResource(uri, mimeType string, payload []byte) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI:      uri,
		MIMEType: mimeType,
		Text:     string(payload),
	}}}
}
