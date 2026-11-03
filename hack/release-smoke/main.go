// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Command release-smoke exercises a built binary over stdio before it is
// uploaded to a release. It checks the static resources, the eight artifact
// templates, one read per family, and the version resolution tool.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var requiredTemplates = []string{
	"rhds://data/elements{/path*}",
	"rhds://data/elements{/path*}?{+query}",
	"rhds://data/tokens{/path*}",
	"rhds://data/tokens{/path*}?{+query}",
	"rhds://data/docs{/path*}",
	"rhds://data/docs{/path*}?{+query}",
	"rhds://data/release-notes{/path*}",
	"rhds://data/release-notes{/path*}?{+query}",
}

var requiredResources = []string{
	"rhds://migrations/tokens",
	"rhds://packages/",
}

var requiredReads = []string{
	"rhds://packages/",
	"rhds://data/elements",
	"rhds://data/tokens",
	"rhds://data/docs",
	"rhds://data/release-notes",
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: release-smoke <path-to-rhds-mcp>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "release smoke failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("release smoke ok")
}

func run(binary string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "release-smoke", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(binary)}, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = session.Close() }()

	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		return fmt.Errorf("list resources: %w", err)
	}
	present := make(map[string]bool, len(resources.Resources))
	for _, resource := range resources.Resources {
		present[resource.URI] = true
	}
	for _, uri := range requiredResources {
		if !present[uri] {
			return fmt.Errorf("resource %s is missing", uri)
		}
	}

	templates, err := session.ListResourceTemplates(ctx, nil)
	if err != nil {
		return fmt.Errorf("list templates: %w", err)
	}
	present = make(map[string]bool, len(templates.ResourceTemplates))
	for _, template := range templates.ResourceTemplates {
		present[template.URITemplate] = true
	}
	for _, uri := range requiredTemplates {
		if !present[uri] {
			return fmt.Errorf("template %s is missing", uri)
		}
	}

	for _, uri := range requiredReads {
		result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			return fmt.Errorf("read %s: %w", uri, err)
		}
		size := 0
		for _, content := range result.Contents {
			size += len(content.Text) + len(content.Blob)
		}
		if size == 0 {
			return fmt.Errorf("read %s returned no content", uri)
		}
		if uri == "rhds://packages/" {
			if err := checkPackages(resourceText(result)); err != nil {
				return err
			}
		}
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "rhds_resolve_version"})
	if err != nil {
		return fmt.Errorf("resolve version: %w", err)
	}
	if result.IsError {
		return fmt.Errorf("resolve version returned a tool error")
	}
	if err := checkResolved(toolText(result)); err != nil {
		return err
	}
	return nil
}

func resourceText(result *mcp.ReadResourceResult) string {
	var builder strings.Builder
	for _, content := range result.Contents {
		builder.WriteString(content.Text)
	}
	return builder.String()
}

func checkPackages(body string) error {
	var payload struct {
		Error    json.RawMessage   `json:"error"`
		Packages []json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return fmt.Errorf("decode packages resource: %w", err)
	}
	if present(payload.Error) {
		return fmt.Errorf("packages resource reported an error: %s", payload.Error)
	}
	if len(payload.Packages) != 4 {
		return fmt.Errorf("packages resource lists %d packages, want 4", len(payload.Packages))
	}
	return nil
}

func checkResolved(body string) error {
	var payload struct {
		Resolved map[string]any  `json:"resolved"`
		Error    json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return fmt.Errorf("decode version resolution: %w", err)
	}
	if present(payload.Error) {
		return fmt.Errorf("version resolution reported an error: %s", payload.Error)
	}
	if len(payload.Resolved) == 0 {
		return fmt.Errorf("version resolution returned no resolved versions")
	}
	return nil
}

func toolText(result *mcp.CallToolResult) string {
	var builder strings.Builder
	for _, content := range result.Content {
		if typed, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(typed.Text)
		}
	}
	return builder.String()
}

func present(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}
