// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/redhat-ux/mcp/internal/data"
)

// TestRealEmbeddedData exercises the Phase B tools against `make data`
// output. It is skipped on a clean checkout.
func TestRealEmbeddedData(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "data", "dist", data.IndexPath)); err != nil {
		t.Skip("generated data is absent; run make data")
	}
	dist, err := fs.Sub(data.Embedded(), "dist")
	if err != nil {
		t.Fatal(err)
	}
	store, err := data.Open(dist)
	if err != nil {
		t.Fatalf("data.Open() = %v", err)
	}
	service := New(store, nil)

	components := service.ListComponents(context.Background(), ListComponentsInput{
		Version:           "5.0.0",
		IncludeDeprecated: true,
		PageSize:          100,
	})
	if components.Error != nil {
		t.Fatalf("ListComponents() error = %+v", components.Error)
	}
	if components.Total == 0 {
		t.Fatal("ListComponents() returned no components for 5.0.0")
	}
	withDocs := 0
	withoutDocs := 0
	for _, component := range components.Components {
		if component.DocsAvailable {
			withDocs++
		} else {
			withoutDocs++
		}
	}
	if withDocs == 0 || withoutDocs == 0 {
		t.Fatalf("docsAvailable is not per component: %d with docs, %d without", withDocs, withoutDocs)
	}
	button, ok := findComponent(components.Components, "rh-button")
	if !ok {
		t.Fatal("rh-button missing from the 5.0.0 listing")
	}
	if !button.DocsAvailable {
		t.Error("rh-button should have docs in the 5.0.0 snapshot")
	}

	tokens := service.QueryTokens(context.Background(), QueryTokensInput{
		Token:   "color-accent-base",
		Version: "3.2.0",
		Theme:   "dark",
	})
	if tokens.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", tokens.Error)
	}
	if len(tokens.Tokens) != 1 {
		t.Fatalf("tokens = %+v", tokens.Tokens)
	}
	accent := tokens.Tokens[0]
	if accent.CSSVariable != "--rh-color-accent-base" {
		t.Errorf("CSS variable = %q", accent.CSSVariable)
	}
	if accent.ThemeValue != "#92c5f9" {
		t.Errorf("dark theme value = %q", accent.ThemeValue)
	}

	subtle := service.QueryTokens(context.Background(), QueryTokensInput{
		Token:   "color-icon-subtle",
		Version: "3.2.0",
	})
	if subtle.Error != nil {
		t.Fatalf("QueryTokens(color-icon-subtle) error = %+v", subtle.Error)
	}
	if subtle.Tokens[0].CSSVariable != "--rh-color-icon-subtle" {
		t.Errorf("group-marker CSS variable = %q", subtle.Tokens[0].CSSVariable)
	}

	maxWidthToken := "breakpoint-" + "2xs" + "-max"
	breakpoint := service.QueryTokens(context.Background(), QueryTokensInput{
		Token:   maxWidthToken,
		Version: "3.2.0",
	})
	if breakpoint.Error != nil {
		t.Fatalf("QueryTokens(%s) error = %+v", maxWidthToken, breakpoint.Error)
	}
	if breakpoint.Tokens[0].CSSVariable != "--rh-breakpoint-2xs-max" {
		t.Errorf("camelCase CSS variable = %q", breakpoint.Tokens[0].CSSVariable)
	}
}

// TestRealManifestNormalization exercises both CEM schema versions against the
// generated data for the oldest and newest bundled majors.
func TestRealManifestNormalization(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "data", "dist", data.IndexPath)); err != nil {
		t.Skip("generated data is absent; run make data")
	}
	dist, err := fs.Sub(data.Embedded(), "dist")
	if err != nil {
		t.Fatal(err)
	}
	store, err := data.Open(dist)
	if err != nil {
		t.Fatalf("data.Open() = %v", err)
	}
	service := New(store, nil)

	for _, version := range []string{"1.0.0", "5.0.0"} {
		if !store.Has(packageElements, version, kindManifest) {
			t.Fatalf("elements@%s is not bundled", version)
		}
		model, failure := service.loadModel(version)
		if failure != nil {
			t.Fatalf("normalizing elements@%s: %+v", version, failure)
		}
		if len(model.Components) == 0 {
			t.Errorf("elements@%s normalized to no components", version)
		}
	}

	// A real component can exceed the 8k summary budget when every section is
	// requested. The tool must ask for a follow-up rather than truncate.
	if api := service.GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName: "rh-button",
		Version: "5.0.0",
	}); api.Error != nil && api.Error.Code != "response_too_large" {
		t.Fatalf("GetComponentAPI() error = %+v", api.Error)
	}
	api := service.GetComponentAPI(context.Background(), GetComponentAPIInput{
		TagName:  "rh-button",
		Version:  "5.0.0",
		Sections: []string{"attributes"},
	})
	if api.Error != nil {
		t.Fatalf("GetComponentAPI(attributes) error = %+v", api.Error)
	}
	if len(api.Attributes) == 0 {
		t.Error("rh-button has no attributes in the real manifest")
	}
}
