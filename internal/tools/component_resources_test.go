// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newComponentResourceServer(t *testing.T) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	RegisterComponentResources(server, fixtureService(t))
	return server
}

func TestRegisterComponentResourcesListsEverySection(t *testing.T) {
	session := connectServer(t, newComponentResourceServer(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates() error = %v", err)
	}
	var templates []string
	for _, template := range result.ResourceTemplates {
		if template.URITemplate == "" {
			continue
		}
		templates = append(templates, template.URITemplate)
	}
	for _, section := range componentResourceSections {
		want := "rhds://elements/{version}/components/{tag}/" + section
		if !slices.Contains(templates, want) {
			t.Errorf("resource templates are missing %q: %v", want, templates)
		}
	}
	if len(templates) != len(componentResourceSections) {
		t.Errorf("resource templates = %v, want exactly %d entries",
			templates, len(componentResourceSections))
	}
}

func TestReadComponentSectionResource(t *testing.T) {
	session := connectServer(t, newComponentResourceServer(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cases := []struct {
		section string
		want    int
	}{
		{sectionAttributes, 3},
		{sectionEvents, 1},
		{sectionSlots, 2},
		{sectionCSSParts, 1},
		{sectionCSSProperties, 1},
		{sectionExamples, 1},
	}
	for _, tc := range cases {
		t.Run(tc.section, func(t *testing.T) {
			uri := "rhds://elements/5.0.0/components/rh-button/" + tc.section
			result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
			if err != nil {
				t.Fatalf("ReadResource(%s) error = %v", uri, err)
			}
			if len(result.Contents) != 1 {
				t.Fatalf("contents = %d, want 1", len(result.Contents))
			}
			contents := result.Contents[0]
			if contents.MIMEType != "application/json" {
				t.Errorf("MIMEType = %q, want application/json", contents.MIMEType)
			}
			var payload []json.RawMessage
			if err := json.Unmarshal([]byte(contents.Text), &payload); err != nil {
				t.Fatalf("section payload is not a JSON array: %v\n%s", err, contents.Text)
			}
			if len(payload) != tc.want {
				t.Errorf("%s entries = %d, want %d", tc.section, len(payload), tc.want)
			}
		})
	}
}

func TestComponentSectionResourceUnknownVersion(t *testing.T) {
	session := connectServer(t, newComponentResourceServer(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhds://elements/9.9.9/components/rh-button/attributes",
	})
	if err == nil {
		t.Fatal("ReadResource() error = nil, want an unknown-version error")
	}
}

func TestComponentSectionResourceUnknownTag(t *testing.T) {
	session := connectServer(t, newComponentResourceServer(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhds://elements/5.0.0/components/rh-missing/attributes",
	})
	if err == nil {
		t.Fatal("ReadResource() error = nil, want an unknown-tag error")
	}
}

func TestComponentSectionResourceUnknownSection(t *testing.T) {
	session := connectServer(t, newComponentResourceServer(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhds://elements/5.0.0/components/rh-button/properties",
	})
	if err == nil {
		t.Fatal("ReadResource() error = nil, want an unknown-section error")
	}
}
