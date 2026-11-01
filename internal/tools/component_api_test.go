// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/cem"
	"github.com/redhat-ux/mcp/internal/data"
)

func TestGetComponentAPISummaryDefaults(t *testing.T) {
	out := fixtureService(t).GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-button",
	})
	if out.Error != nil {
		t.Fatalf("GetComponentAPI() error = %+v", out.Error)
	}
	if out.Resolved.Elements != "5.0.0" {
		t.Errorf("elements = %q", out.Resolved.Elements)
	}
	if out.Name != "RhButton" {
		t.Errorf("name = %q", out.Name)
	}
	if out.Summary == "" {
		t.Error("summary is empty")
	}
	if out.Description != "" {
		t.Errorf("summary detail should omit the description, got %q", out.Description)
	}
	if len(out.Attributes) != 3 {
		t.Fatalf("attributes = %+v", out.Attributes)
	}
	if out.Attributes[0].Description != "" {
		t.Errorf("summary attribute description = %q, want empty", out.Attributes[0].Description)
	}
	if len(out.Properties) != 2 {
		t.Errorf("properties = %+v, want public fields only", out.Properties)
	}
	if len(out.Events) != 1 || out.Events[0].Name != "click" {
		t.Errorf("events = %+v", out.Events)
	}
	if len(out.Slots) != 2 {
		t.Errorf("slots = %+v", out.Slots)
	}
	if len(out.CSSParts) != 1 || len(out.CSSProperties) != 1 {
		t.Errorf("css = %+v / %+v", out.CSSParts, out.CSSProperties)
	}
	if len(out.Examples) != 1 {
		t.Fatalf("examples = %+v", out.Examples)
	}
	if out.Examples[0].Description != "A primary button." {
		t.Errorf("example description = %q", out.Examples[0].Description)
	}
	if out.Examples[0].URL != "" || out.Examples[0].Source != "" {
		t.Errorf("summary detail should omit example URLs: %+v", out.Examples[0])
	}
	if !slices.Equal(out.Sections, componentAPISections) {
		t.Errorf("sections = %v", out.Sections)
	}
}

func TestGetComponentAPIFull(t *testing.T) {
	out := fixtureService(t).GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-button",
		Detail:  "full",
	})
	if out.Error != nil {
		t.Fatalf("GetComponentAPI() error = %+v", out.Error)
	}
	if out.Description == "" {
		t.Error("full detail should include the description")
	}
	if len(out.Attributes) == 0 || out.Attributes[0].Description == "" {
		t.Errorf("attributes = %+v", out.Attributes)
	}
	if out.Attributes[2].Deprecated == nil {
		t.Errorf("deprecated attribute = %+v, want deprecation from the manifest", out.Attributes[2])
	}
	if out.Examples[0].URL == "" || out.Examples[0].Source == "" {
		t.Errorf("full example = %+v", out.Examples[0])
	}
}

func TestGetComponentAPISectionFilter(t *testing.T) {
	out := fixtureService(t).GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName:  "rh-button",
		Sections: []string{"attributes"},
	})
	if out.Error != nil {
		t.Fatalf("GetComponentAPI() error = %+v", out.Error)
	}
	if !slices.Equal(out.Sections, []string{"attributes"}) {
		t.Errorf("sections = %v", out.Sections)
	}
	if len(out.Attributes) == 0 {
		t.Error("attributes missing")
	}
	if out.Properties != nil || out.Events != nil || out.Slots != nil ||
		out.CSSParts != nil || out.CSSProperties != nil || out.Examples != nil {
		t.Errorf("unrequested sections leaked: %+v", out)
	}
}

func TestGetComponentAPIUnknownSection(t *testing.T) {
	out := fixtureService(t).GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName:  "rh-button",
		Sections: []string{"shadowDom"},
	})
	if out.Error == nil || out.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v", out.Error)
	}
}

func TestGetComponentAPIUnknownElement(t *testing.T) {
	out := fixtureService(t).GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-missing",
	})
	if out.Error == nil || out.Error.Code != "component_not_found" {
		t.Fatalf("error = %+v", out.Error)
	}
}

func TestGetComponentAPIInvalidDetail(t *testing.T) {
	out := fixtureService(t).GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-button",
		Detail:  "verbose",
	})
	if out.Error == nil || out.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v", out.Error)
	}
}

func TestGetComponentAPIHonorsVersion(t *testing.T) {
	service := fixtureService(t)
	card := service.GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-card",
		Version: "4.0.0",
	})
	if card.Error != nil {
		t.Fatalf("rh-card at 4.0.0 error = %+v", card.Error)
	}
	if card.Resolved.Elements != "4.0.0" || card.Resolved.Docs != "v4.2.2" {
		t.Errorf("resolved = %+v", card.Resolved)
	}

	button := service.GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-button",
		Version: "4.0.0",
	})
	if button.Error == nil || button.Error.Code != "component_not_found" {
		t.Errorf("rh-button should not exist at 4.0.0: %+v", button.Error)
	}
}

func TestGetComponentAPIDeprecationComesFromManifest(t *testing.T) {
	out := fixtureService(t).GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-dialog",
	})
	if out.Error != nil {
		t.Fatalf("GetComponentAPI() error = %+v", out.Error)
	}
	if out.Deprecated == nil {
		t.Fatal("deprecation = nil, want the manifest's deprecation")
	}
	if out.Deprecated.Message != "Use rh-dialog-v2 instead." {
		t.Errorf("deprecation = %+v", out.Deprecated)
	}
}

// TestSummarizeComponentAPISectionsPreserveContractData proves that names,
// types, and defaults are returned verbatim in every detail mode, that summary
// keeps fieldName and inheritedFrom, and that full returns every prose field
// without shortening it.
func TestSummarizeComponentAPISectionsPreserveContractData(t *testing.T) {
	description := strings.Repeat("prose ", 400)
	contract := strings.Repeat("contract", 40)

	attributes := summarizeAttributes([]cem.Attribute{{
		Name:          contract,
		Type:          contract,
		Default:       contract,
		Summary:       "summary",
		Description:   description,
		FieldName:     "fieldName",
		InheritedFrom: "BaseElement",
	}}, true)
	if got := attributes[0]; got.Name != contract || got.Type != contract ||
		got.Default != contract || got.Description != description {
		t.Errorf("full attribute = %+v, want verbatim contract and prose", got)
	}
	attributes = summarizeAttributes([]cem.Attribute{{
		Name:          contract,
		Type:          contract,
		Default:       contract,
		Summary:       "summary",
		Description:   description,
		FieldName:     "fieldName",
		InheritedFrom: "BaseElement",
	}}, false)
	if got := attributes[0]; got.Name != contract || got.Type != contract ||
		got.Default != contract || got.FieldName != "fieldName" ||
		got.InheritedFrom != "BaseElement" {
		t.Errorf("summary attribute = %+v, want verbatim contract fields", got)
	}
	if attributes[0].Summary != "" || attributes[0].Description != "" {
		t.Errorf("summary attribute prose = %+v, want omitted prose", attributes[0])
	}

	properties := summarizeProperties([]cem.Property{{
		Name:          contract,
		Type:          contract,
		Default:       contract,
		Attribute:     contract,
		Summary:       "summary",
		Description:   description,
		InheritedFrom: "BaseElement",
	}}, true)
	if got := properties[0]; got.Name != contract || got.Type != contract ||
		got.Default != contract || got.Attribute != contract ||
		got.Description != description {
		t.Errorf("full property = %+v, want verbatim contract and prose", got)
	}
	properties = summarizeProperties([]cem.Property{{
		Name:          contract,
		Type:          contract,
		Default:       contract,
		Attribute:     contract,
		Summary:       "summary",
		Description:   description,
		InheritedFrom: "BaseElement",
	}}, false)
	if got := properties[0]; got.Name != contract || got.Type != contract ||
		got.Default != contract || got.Attribute != contract ||
		got.InheritedFrom != "BaseElement" {
		t.Errorf("summary property = %+v, want verbatim contract fields", got)
	}

	if got := summarizeEvents([]cem.Event{{
		Name: contract, Type: contract, Description: description, InheritedFrom: "BaseElement",
	}}, true)[0]; got.Name != contract || got.Type != contract || got.Description != description {
		t.Errorf("full event = %+v", got)
	}
	if got := summarizeSlots([]cem.Slot{{
		Name: contract, Description: description, InheritedFrom: "BaseElement",
	}}, true)[0]; got.Name != contract || got.Description != description {
		t.Errorf("full slot = %+v", got)
	}
	if got := summarizeParts([]cem.CSSPart{{
		Name: contract, Description: description, InheritedFrom: "BaseElement",
	}}, true)[0]; got.Name != contract || got.Description != description {
		t.Errorf("full part = %+v", got)
	}
	if got := summarizeCSSProperties([]cem.CSSProperty{{
		Name: contract, Default: contract, Syntax: contract,
		Description: description, InheritedFrom: "BaseElement",
	}}, true)[0]; got.Name != contract || got.Default != contract ||
		got.Syntax != contract || got.Description != description {
		t.Errorf("full css property = %+v", got)
	}

	examples := summarizeExamples([]cem.Example{{
		Description: description,
		URL:         contract,
		Source:      contract,
	}}, true)
	if got := examples[0]; got.Description != description || got.URL != contract ||
		got.Source != contract {
		t.Errorf("full example = %+v, want verbatim fields", got)
	}
	examples = summarizeExamples([]cem.Example{{
		Description: description,
		URL:         contract,
		Source:      contract,
	}}, false)
	if got := examples[0]; got.Description != description || got.URL != "" || got.Source != "" {
		t.Errorf("summary example = %+v, want complete description and omitted references", got)
	}
}

// TestDirectToolFailuresAreBounded covers the direct service entry points,
// which do not go through the MCP request path. A hostile or corrupt data
// error must not escape the response budget.
func TestDirectToolFailuresAreBounded(t *testing.T) {
	huge := strings.Repeat("e", 20000)
	service := New(nil, &data.Error{
		Code:    "data_unavailable",
		Message: huge,
		Hint:    huge,
	})

	component := service.GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-button",
	})
	if component.Error == nil {
		t.Fatal("GetComponentAPI() error = nil, want the data failure")
	}
	if size := estimatedResponseBytes(component); size > defaultResponseBudget {
		t.Errorf("GetComponentAPI() failure is %d bytes, want <= %d",
			size, defaultResponseBudget)
	}
	if got := utf8.RuneCountInString(component.Error.Message); got > maxWarningRunes+len("...") {
		t.Errorf("GetComponentAPI() error message is %d runes, want <= %d",
			got, maxWarningRunes+len("..."))
	}

	search := service.Search(context.Background(), SearchInput{Query: "button"})
	if search.Error == nil {
		t.Fatal("Search() error = nil, want the data failure")
	}
	if size := estimatedResponseBytes(search); size > defaultResponseBudget {
		t.Errorf("Search() failure is %d bytes, want <= %d",
			size, defaultResponseBudget)
	}
}

// TestComponentAPIResourceLinksReachToolContent drives rhds_get_component_api
// over an in-memory MCP session. The response must omit the oversized optional
// section, keep the JSON text fallback, and add a resource_link pointing at the
// section resource.
func TestComponentAPIResourceLinksReachToolContent(t *testing.T) {
	demos := make([]map[string]any, 80)
	for i := range demos {
		demos[i] = map[string]any{
			"name":        "demo",
			"description": strings.Repeat("example prose ", 20),
			"url":         "https://example.test/demo/",
		}
	}
	manifestJSON, err := json.Marshal(map[string]any{
		"schemaVersion": "2.1.0",
		"modules": []any{map[string]any{
			"kind": "javascript-module",
			"path": "./rh-big.js",
			"declarations": []any{map[string]any{
				"kind":          "class",
				"customElement": true,
				"name":          "RhBig",
				"tagName":       "rh-big",
				"summary":       "A large fixture element.",
				"attributes": []any{map[string]any{
					"name": "variant",
					"type": map[string]any{"text": "string"},
				}},
				"demos": demos,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("marshaling manifest: %v", err)
	}

	service := memoryService(t, manifestJSON, "5.0.0")
	service.maxResponseBytes = 4096
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	Register(server, service)
	session := connectServer(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "rhds_get_component_api",
		Arguments: map[string]any{"tagName": "rh-big"},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() IsError = true: %s", contentText(result))
	}

	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structured content: %v", err)
	}
	var out ComponentAPIOutput
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decoding structured content: %v", err)
	}
	if len(out.Examples) != 0 {
		t.Errorf("examples = %d, want the oversized optional section omitted", len(out.Examples))
	}
	if !warnedForSection(out.Resolved.Warnings, sectionExamples) {
		t.Errorf("warnings = %v, want one naming examples", out.Resolved.Warnings)
	}

	wantURI := "rhds://elements/5.0.0/components/rh-big/examples"
	var found bool
	var haveText bool
	for _, content := range result.Content {
		switch typed := content.(type) {
		case *mcp.TextContent:
			haveText = typed.Text != ""
		case *mcp.ResourceLink:
			if typed.URI == wantURI {
				found = true
				if typed.MIMEType != "application/json" {
					t.Errorf("resource link MIMEType = %q", typed.MIMEType)
				}
			}
		}
	}
	if !haveText {
		t.Error("tool content has no JSON text fallback")
	}
	if !found {
		t.Errorf("tool content = %+v, want a resource_link for %s", result.Content, wantURI)
	}
}

func TestGetComponentAPIAsksForFollowUpInsteadOfTruncating(t *testing.T) {
	service := fixtureService(t)
	service.maxResponseBytes = 512
	out := service.GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-button",
	})
	if out.Error == nil {
		t.Fatal("error = nil, want response_too_large")
	}
	if out.Error.Code != "response_too_large" {
		t.Errorf("error code = %q", out.Error.Code)
	}
	// Optional sections are dropped first; their resource URIs must appear in
	// the error hint so the caller can read them directly.
	for _, section := range []string{sectionExamples, sectionCSSProperties, sectionCSSParts} {
		uri := componentSectionURI(out.Resolved.Elements, out.TagName, section)
		if uri != "" && !strings.Contains(out.Error.Hint, uri) {
			t.Errorf("hint = %q, want the omitted section URI %q", out.Error.Hint, uri)
		}
	}
	if out.Attributes != nil {
		t.Errorf("partial data returned alongside the error: %+v", out.Attributes)
	}
}

// TestComponentAPIResultLinksOmittedSections proves the MCP handler turns
// omitted sections into resource_link content items pointing at the matching
// section resource.
func TestComponentAPIResultLinksOmittedSections(t *testing.T) {
	out := ComponentAPIOutput{
		Resolved: ResolvedVersion{Elements: "5.0.0"},
		TagName:  "rh-huge",
		Sections: []string{sectionAttributes},
		sectionLinks: []sectionLink{{
			section: sectionExamples,
			uri:     "rhds://elements/5.0.0/components/rh-huge/examples",
		}},
	}
	result := componentAPIResult(out)
	if result == nil || len(result.Content) == 0 {
		t.Fatalf("componentAPIResult() = %+v, want resource_link content", result)
	}
	if _, ok := result.Content[0].(*mcp.TextContent); !ok {
		t.Errorf("content[0] = %T, want the JSON text fallback", result.Content[0])
	}
	var link *mcp.ResourceLink
	for _, content := range result.Content {
		if candidate, ok := content.(*mcp.ResourceLink); ok {
			link = candidate
			break
		}
	}
	if link == nil {
		t.Fatalf("content = %+v, want a *mcp.ResourceLink", result.Content)
	}
	if link.URI != "rhds://elements/5.0.0/components/rh-huge/examples" {
		t.Errorf("link URI = %q", link.URI)
	}
	if link.MIMEType != "application/json" {
		t.Errorf("link MIMEType = %q", link.MIMEType)
	}
}
