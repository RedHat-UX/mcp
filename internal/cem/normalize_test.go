// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package cem

import (
	"os"
	"path/filepath"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return payload
}

func component(t *testing.T, model *Model, tagName string) *Component {
	t.Helper()
	found, ok := model.Component(tagName)
	if !ok {
		t.Fatalf("component %q missing from normalized model", tagName)
	}
	return found
}

func TestNormalizeSchema2_1(t *testing.T) {
	model, err := Parse(readFixture(t, "2.1.0.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if model.SchemaVersion != "2.1.0" {
		t.Errorf("schema version = %q, want 2.1.0", model.SchemaVersion)
	}
	if len(model.Components) != 2 {
		t.Fatalf("components = %d, want 2", len(model.Components))
	}
	// Components are sorted by tag name for deterministic output.
	if model.Components[0].TagName != "rh-button" || model.Components[1].TagName != "rh-icon" {
		t.Errorf("component order = %q, %q", model.Components[0].TagName, model.Components[1].TagName)
	}

	button := component(t, model, "rh-button")
	if button.Name != "RhButton" {
		t.Errorf("name = %q", button.Name)
	}
	if button.Summary != "Triggers an action." {
		t.Errorf("summary = %q", button.Summary)
	}
	if button.ModulePath != "./rh-button/rh-button.js" {
		t.Errorf("module path = %q", button.ModulePath)
	}
	if button.PackageSubpath != "rh-button/rh-button.js" {
		t.Errorf("package subpath = %q", button.PackageSubpath)
	}
	if button.Deprecated != nil {
		t.Errorf("deprecated = %+v, want nil", button.Deprecated)
	}
	if len(button.Attributes) != 1 {
		t.Fatalf("attributes = %d, want 1", len(button.Attributes))
	}
	attribute := button.Attributes[0]
	if attribute.Name != "variant" || attribute.Type != `"primary" | "secondary"` {
		t.Errorf("attribute = %+v", attribute)
	}
	if attribute.Default != "primary" {
		t.Errorf("attribute default = %q", attribute.Default)
	}
	if len(button.Properties) != 2 {
		t.Fatalf("properties = %d, want 2", len(button.Properties))
	}
	if button.Properties[0].Name != "variant" || button.Properties[0].Attribute != "variant" {
		t.Errorf("property = %+v", button.Properties[0])
	}
	if len(button.Events) != 1 || button.Events[0].Name != "click" {
		t.Errorf("events = %+v", button.Events)
	}
	if len(button.Slots) != 1 || button.Slots[0].Name != "" {
		t.Errorf("slots = %+v", button.Slots)
	}
	if len(button.CSSParts) != 1 || button.CSSParts[0].Name != "button" {
		t.Errorf("css parts = %+v", button.CSSParts)
	}
	if len(button.CSSProperties) != 1 || button.CSSProperties[0].Name != "--rh-button-color" {
		t.Errorf("css properties = %+v", button.CSSProperties)
	}
	if len(button.Examples) != 1 {
		t.Fatalf("examples = %d, want 1", len(button.Examples))
	}
	if button.Examples[0].Description != "A primary button." {
		t.Errorf("example = %+v", button.Examples[0])
	}

	icon := component(t, model, "rh-icon")
	if icon.Deprecated == nil {
		t.Fatal("rh-icon should be deprecated")
	}
	if icon.Deprecated.Message != "Use rh-icon-v2." {
		t.Errorf("deprecation message = %q", icon.Deprecated.Message)
	}
	// rh-icon's deprecated attribute is supplied by the manifest.
	if len(icon.Attributes) != 2 {
		t.Fatalf("attributes = %d, want 2", len(icon.Attributes))
	}
	if icon.Attributes[1].Deprecated == nil {
		t.Error("legacy attribute should carry its deprecation")
	}
}

func TestNormalizeSchema1_0(t *testing.T) {
	model, err := Parse(readFixture(t, "1.0.0.json"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if model.SchemaVersion != "1.0.0" {
		t.Errorf("schema version = %q, want 1.0.0", model.SchemaVersion)
	}
	if len(model.Components) != 1 {
		t.Fatalf("components = %d, want 1", len(model.Components))
	}

	card := component(t, model, "rh-card")
	if card.Deprecated != nil {
		t.Errorf("deprecated = %+v, want nil (the manifest supplies none)", card.Deprecated)
	}
	// 1.0.0 has no demos. The normalizer must not invent any.
	if len(card.Examples) != 0 {
		t.Errorf("examples = %+v, want none", card.Examples)
	}
	if len(card.Attributes) != 1 || card.Attributes[0].Name != "header" {
		t.Errorf("attributes = %+v", card.Attributes)
	}
	if len(card.Properties) != 1 || card.Properties[0].Name != "header" {
		t.Errorf("properties = %+v", card.Properties)
	}
	if len(card.Slots) != 2 || card.Slots[1].Name != "header" {
		t.Errorf("slots = %+v", card.Slots)
	}
}

func TestNormalizeRejectsUnknownSchemaVersion(t *testing.T) {
	_, err := Parse([]byte(`{"schemaVersion":"3.0.0","modules":[]}`))
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported schema version error")
	}
}

func TestNormalizeIgnoresDeclarationsThatAreNotCustomElements(t *testing.T) {
	payload := []byte(`{
		"schemaVersion": "2.1.0",
		"modules": [{
			"kind": "javascript-module",
			"path": "./mixin.js",
			"declarations": [
				{"kind": "mixin", "name": "FancyMixin"},
				{"kind": "class", "name": "PlainClass"},
				{"kind": "class", "customElement": true, "name": "RhReal", "tagName": "rh-real"}
			]
		}]
	}`)
	model, err := Parse(payload)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(model.Components) != 1 || model.Components[0].TagName != "rh-real" {
		t.Errorf("components = %+v", model.Components)
	}
}

func TestNormalizeHandlesMissingOptionalFields(t *testing.T) {
	payload := []byte(`{
		"schemaVersion": "1.0.0",
		"modules": [{"kind": "javascript-module", "path": "./x.js", "declarations": [
			{"kind": "class", "customElement": true, "name": "RhBare", "tagName": "rh-bare"}
		]}]
	}`)
	model, err := Parse(payload)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	bare := component(t, model, "rh-bare")
	if bare.Attributes != nil || bare.Properties != nil || bare.Events != nil ||
		bare.Slots != nil || bare.CSSParts != nil || bare.CSSProperties != nil ||
		bare.Examples != nil {
		t.Errorf("optional sections should be nil, got %+v", bare)
	}
	if bare.Deprecated != nil {
		t.Errorf("deprecation should be nil, got %+v", bare.Deprecated)
	}
}
