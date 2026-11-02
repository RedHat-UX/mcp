// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectToolClient(t *testing.T, svc *Service) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "rhds-mcp-test", Version: "0.0.0"},
		&mcp.ServerOptions{Logger: slog.New(slog.DiscardHandler)},
	)
	Register(server, svc)
	RegisterPrompts(server, svc)
	RegisterResources(server, svc)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "rhds-mcp-test", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func TestResourcesExposeProgressiveDisclosureSurface(t *testing.T) {
	client := connectToolClient(t, fixtureService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	list, err := client.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(list.Resources) != 2 {
		t.Fatalf("resources = %d, want exactly 2 static resources: %+v",
			len(list.Resources), list.Resources)
	}
	uris := make(map[string]bool)
	for _, resource := range list.Resources {
		uris[resource.URI] = true
	}
	for _, uri := range []string{migrationsResourceURI, packagesResourceURI} {
		if !uris[uri] {
			t.Errorf("resources are missing %q", uri)
		}
	}

	manifest, err := client.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhds://data/elements?version=5.0.0",
	})
	if err != nil {
		t.Fatalf("ReadResource(manifest) error = %v", err)
	}
	if len(manifest.Contents) != 1 || manifest.Contents[0].MIMEType != "application/json" {
		t.Errorf("manifest contents = %+v", manifest.Contents)
	}
	if len(manifest.Contents[0].Text) == 0 {
		t.Error("manifest resource is empty")
	}

	migrations, err := client.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: migrationsResourceURI,
	})
	if err != nil {
		t.Fatalf("ReadResource(migrations) error = %v", err)
	}
	if len(migrations.Contents) != 1 || len(migrations.Contents[0].Text) == 0 {
		t.Errorf("migrations contents = %+v", migrations.Contents)
	}
}

func TestReadUnknownResourceIsNotFound(t *testing.T) {
	client := connectToolClient(t, fixtureService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhds://nope/1.0.0/thing",
	})
	if err == nil {
		t.Fatal("ReadResource() error = nil, want not found")
	}
}

func TestPromptsAreRegistered(t *testing.T) {
	client := connectToolClient(t, fixtureService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	list, err := client.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("ListPrompts() error = %v", err)
	}
	names := make(map[string]bool)
	for _, prompt := range list.Prompts {
		names[prompt.Name] = true
	}
	for _, want := range []string{"rhds-lit-usage", "rhds-html-usage", "rhds-twig-slot-template"} {
		if !names[want] {
			t.Errorf("prompts are missing %q", want)
		}
	}
}

func TestLitAndHTMLPromptsIncludeTagName(t *testing.T) {
	client := connectToolClient(t, fixtureService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, name := range []string{"rhds-lit-usage", "rhds-html-usage"} {
		result, err := client.GetPrompt(ctx, &mcp.GetPromptParams{
			Name:      name,
			Arguments: map[string]string{"tagName": "rh-button", "version": "5.0.0"},
		})
		if err != nil {
			t.Fatalf("GetPrompt(%s) error = %v", name, err)
		}
		if len(result.Messages) == 0 {
			t.Fatalf("GetPrompt(%s) returned no messages", name)
		}
		text := result.Messages[0].Content.(*mcp.TextContent).Text
		if !containsFold(text, "rh-button") || !containsFold(text, "5.0.0") {
			t.Errorf("%s prompt = %q", name, text)
		}
	}
}

func TestLitAndHTMLPromptsNormalizeShorthandTag(t *testing.T) {
	client := connectToolClient(t, fixtureService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, name := range []string{"rhds-lit-usage", "rhds-html-usage"} {
		result, err := client.GetPrompt(ctx, &mcp.GetPromptParams{
			Name:      name,
			Arguments: map[string]string{"tagName": "button"},
		})
		if err != nil {
			t.Fatalf("GetPrompt(%s) error = %v", name, err)
		}
		if len(result.Messages) == 0 {
			t.Fatalf("GetPrompt(%s) returned no messages", name)
		}
		text := result.Messages[0].Content.(*mcp.TextContent).Text
		if got := strings.Count(text, `"rh-button"`); got < 2 {
			t.Errorf("%s prompt interpolates the raw tag %d times, want the normalized rh-button twice:\n%s",
				name, got, text)
		}
		if strings.Contains(text, `"button"`) {
			t.Errorf("%s prompt interpolates the raw tag:\n%s", name, text)
		}
	}
}

func TestTwigPromptIncludesGenericSlots(t *testing.T) {
	client := connectToolClient(t, fixtureService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := client.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "rhds-twig-slot-template",
		Arguments: map[string]string{"tagName": "rh-card", "version": "5.0.0"},
	})
	if err != nil {
		t.Fatalf("GetPrompt() error = %v", err)
	}
	text := result.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"rh-card", "slot=\"header\"", "not a Drupal library key"} {
		if !containsFold(text, want) {
			t.Errorf("twig prompt missing %q:\n%s", want, text)
		}
	}
}
