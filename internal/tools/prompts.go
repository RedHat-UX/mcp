// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterPrompts adds the Lit usage, HTML usage, and Twig slot template
// prompts.
func RegisterPrompts(server *mcp.Server, svc *Service) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "rhds-lit-usage",
		Title:       "Use an RHDS element in Lit",
		Description: "Produce a Lit usage example for one RHDS custom element.",
		Arguments: []*mcp.PromptArgument{
			{Name: "tagName", Title: "Tag name", Description: "Custom element tag name, for example rh-button", Required: true},
			{Name: "version", Title: "Version", Description: "Optional @rhds/elements version"},
		},
	}, func(_ context.Context, request *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		tag := promptArgument(request, "tagName")
		version := promptArgument(request, "version")
		text := fmt.Sprintf(`Write a Lit usage example for %s.

1. Call rhds_get_component_api with tagName %q, using sections attributes, properties, events, slots, cssParts, and cssProperties.
2. Call rhds_get_usage_examples with tagName %q and kind demo, docs, or pattern.
3. Import the element module from its package subpath.
4. Use Lit's html template tag and bind properties with .property syntax.
5. Listen for the element's events with @event syntax.

Do not invent attributes, slots, events, or CSS parts. Use only what the bundled manifest reports.`,
			promptTag(tag), tag, tag)
		if version != "" {
			text += fmt.Sprintf("\n\nUse @rhds/elements version %s.", version)
		}
		return promptResult(text), nil
	})

	server.AddPrompt(&mcp.Prompt{
		Name:        "rhds-html-usage",
		Title:       "Use an RHDS element in HTML",
		Description: "Produce a plain HTML usage example for one RHDS custom element.",
		Arguments: []*mcp.PromptArgument{
			{Name: "tagName", Title: "Tag name", Description: "Custom element tag name, for example rh-button", Required: true},
			{Name: "version", Title: "Version", Description: "Optional @rhds/elements version"},
		},
	}, func(_ context.Context, request *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		tag := promptArgument(request, "tagName")
		version := promptArgument(request, "version")
		text := fmt.Sprintf(`Write a plain HTML usage example for %s.

1. Call rhds_get_component_api with tagName %q to confirm attributes, slots, and events.
2. Call rhds_get_usage_examples with tagName %q and kind docs or pattern.
3. Import the element module as a script tag.
4. Use only documented attributes and slot names.

Do not invent a CDN URL or an import map entry. Ask for the workspace version with rhds_resolve_version when it matters.`,
			promptTag(tag), tag, tag)
		if version != "" {
			text += fmt.Sprintf("\n\nUse @rhds/elements version %s.", version)
		}
		return promptResult(text), nil
	})

	server.AddPrompt(&mcp.Prompt{
		Name:        "rhds-twig-slot-template",
		Title:       "Generic Twig slot template",
		Description: "Produce generic Twig markup for an RHDS element from its CEM slots. It does not claim a Drupal library key.",
		Arguments: []*mcp.PromptArgument{
			{Name: "tagName", Title: "Tag name", Description: "Custom element tag name, for example rh-card", Required: true},
			{Name: "version", Title: "Version", Description: "Optional @rhds/elements version"},
		},
	}, func(_ context.Context, request *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		tag := promptArgument(request, "tagName")
		version := promptArgument(request, "version")
		text := twigPromptText(svc, tag, version)
		return promptResult(text), nil
	})
}

func twigPromptText(svc *Service, tag, version string) string {
	tag = normalizeElementTag(tag)
	text := fmt.Sprintf(`Write generic Twig markup for %s.

Use the slot names in the CEM. This prompt does not claim a Drupal library key: do not invent a library name, a theme hook, or an attach_library call.`, promptTag(tag))
	if svc == nil || svc.store == nil {
		return text
	}
	resolved, failure := svc.resolveVersion(packageElements, kindManifest, version)
	if failure != nil {
		return text + "\n\nNo bundled CEM data is available for a slot list."
	}
	model, failure := svc.loadModel(resolved)
	if failure != nil {
		return text
	}
	component, ok := model.Component(tag)
	if !ok {
		return text + fmt.Sprintf("\n\n%s is not in elements@%s.", tag, resolved)
	}
	template := twigExample(component, resolved)
	return text + fmt.Sprintf("\n\nVersion: %s\n\n```twig\n%s```\n\n%s",
		resolved, template.Content, template.Note)
}

func promptTag(tag string) string {
	if strings.TrimSpace(tag) == "" {
		return "the requested RHDS element"
	}
	return normalizeElementTag(tag)
}

func promptArgument(request *mcp.GetPromptRequest, name string) string {
	if request == nil {
		return ""
	}
	return strings.TrimSpace(request.Params.Arguments[name])
}

func promptResult(text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    mcp.Role("user"),
			Content: &mcp.TextContent{Text: text},
		}},
	}
}
