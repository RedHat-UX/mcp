// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestGetComponentAPIAsksForFollowUpInsteadOfTruncating(t *testing.T) {
	service := fixtureService(t)
	service.maxResponseBytes = 512
	out := service.GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-button",
		Detail:  "full",
	})
	if out.Error == nil {
		t.Fatal("error = nil, want response_too_large")
	}
	if out.Error.Code != "response_too_large" {
		t.Errorf("error code = %q", out.Error.Code)
	}
	if !strings.Contains(out.Error.Hint, "detail=summary") {
		t.Errorf("hint = %q, want follow-up guidance", out.Error.Hint)
	}
	if out.Attributes != nil {
		t.Errorf("partial data returned alongside the error: %+v", out.Attributes)
	}
}
