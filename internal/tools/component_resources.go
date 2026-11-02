// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/cem"
)

// componentResourceSections are the sections served as per-component
// resources. It mirrors the sections accepted by rhds_get_component_api, so a
// section that the tool omits always has a resource to read instead.
var componentResourceSections = []string{
	sectionAttributes,
	sectionProperties,
	sectionEvents,
	sectionSlots,
	sectionCSSParts,
	sectionCSSProperties,
	sectionExamples,
}

// RegisterComponentResources adds one resource template per component API
// section. Each template serves
// rhds://elements/{version}/components/{tag}/{section} as application/json
// with just that section.
func RegisterComponentResources(server *mcp.Server, service *Service) {
	handler := componentSectionResourceHandler(service)
	for _, section := range componentResourceSections {
		server.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: "rhds://elements/{version}/components/{tag}/" + section,
			Name:        "component-" + section,
			Description: fmt.Sprintf(
				"The %s section of an RHDS custom element at a bundled elements version.", section),
			MIMEType: "application/json",
		}, handler)
	}
}

func componentSectionResourceHandler(service *Service) mcp.ResourceHandler {
	return func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if request == nil || request.Params == nil {
			return nil, fmt.Errorf("component resource request has no parameters")
		}
		uri := request.Params.URI
		version, tagName, section, err := parseComponentSectionURI(uri)
		if err != nil {
			return nil, err
		}

		resolved, failure := service.resolveVersion(packageElements, kindManifest, version)
		if failure != nil {
			return nil, fmt.Errorf("component resource %s: %s", uri, failure.Message)
		}
		model, failure := service.loadModel(resolved)
		if failure != nil {
			return nil, fmt.Errorf("component resource %s: %s", uri, failure.Message)
		}
		component, ok := model.Component(tagName)
		if !ok {
			return nil, fmt.Errorf(
				"component resource %s: %q is not a custom element in elements@%s",
				uri, tagName, resolved)
		}
		payload, ok := componentSectionPayload(component, section)
		if !ok {
			return nil, fmt.Errorf("component resource %s: unknown section %q", uri, section)
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("component resource %s: %w", uri, err)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      uri,
				MIMEType: "application/json",
				Text:     string(encoded),
			}},
		}, nil
	}
}

// parseComponentSectionURI splits rhds://elements/{version}/components/{tag}/{section}.
func parseComponentSectionURI(uri string) (version, tagName, section string, err error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", "", "", fmt.Errorf("component resource %s: %w", uri, err)
	}
	if parsed.Scheme != "rhds" || parsed.Host != "elements" {
		return "", "", "", fmt.Errorf("component resource %s: want an rhds://elements URI", uri)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[1] != "components" {
		return "", "", "", fmt.Errorf(
			"component resource %s: want rhds://elements/{version}/components/{tag}/{section}", uri)
	}
	version, err = url.PathUnescape(parts[0])
	if err != nil {
		return "", "", "", fmt.Errorf("component resource %s: invalid version: %w", uri, err)
	}
	tagName, err = url.PathUnescape(parts[2])
	if err != nil {
		return "", "", "", fmt.Errorf("component resource %s: invalid tag: %w", uri, err)
	}
	section = parts[3]
	if !slices.Contains(componentResourceSections, section) {
		return "", "", "", fmt.Errorf("component resource %s: unknown section %q", uri, section)
	}
	return version, strings.ToLower(tagName), section, nil
}

// componentSectionPayload returns one section as a JSON array. A manifest that
// leaves a section empty is served as an empty array, not null.
func componentSectionPayload(component *cem.Component, section string) (any, bool) {
	switch section {
	case sectionAttributes:
		return nonNilSlice(component.Attributes), true
	case sectionProperties:
		return nonNilSlice(component.Properties), true
	case sectionEvents:
		return nonNilSlice(component.Events), true
	case sectionSlots:
		return nonNilSlice(component.Slots), true
	case sectionCSSParts:
		return nonNilSlice(component.CSSParts), true
	case sectionCSSProperties:
		return nonNilSlice(component.CSSProperties), true
	case sectionExamples:
		return nonNilSlice(component.Examples), true
	default:
		return nil, false
	}
}

func nonNilSlice[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
