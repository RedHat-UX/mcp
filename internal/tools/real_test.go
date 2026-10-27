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

func allRealComponents(t *testing.T, service *Service, version string) ([]ComponentSummary, int) {
	t.Helper()
	var all []ComponentSummary
	cursor := ""
	total := 0
	for page := 0; ; page++ {
		out := service.ListComponents(context.Background(), ListComponentsInput{
			Version:           version,
			IncludeDeprecated: true,
			PageSize:          100,
			Cursor:            cursor,
		})
		if out.Error != nil {
			t.Fatalf("ListComponents(%s, cursor=%q) error = %+v", version, cursor, out.Error)
		}
		if page == 0 {
			total = out.Total
		}
		all = append(all, out.Components...)
		if out.NextCursor == "" {
			return all, total
		}
		cursor = out.NextCursor
		if page > 20 {
			t.Fatalf("pagination did not terminate for %s", version)
		}
	}
}

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

	allComponents, total := allRealComponents(t, service, "5.0.0")
	if total == 0 || len(allComponents) != total {
		t.Fatalf("ListComponents() returned %d of %d components for 5.0.0", len(allComponents), total)
	}
	withDocs := 0
	withoutDocs := 0
	for _, component := range allComponents {
		if component.DocsAvailable {
			withDocs++
		} else {
			withoutDocs++
		}
	}
	if withDocs == 0 || withoutDocs == 0 {
		t.Fatalf("docsAvailable is not per component: %d with docs, %d without", withDocs, withoutDocs)
	}
	button, ok := findComponent(allComponents, "rh-button")
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

// TestRealListComponentsDefaultPagesFitBudget pages every bundled version with
// the default page size. Each page must fit the 8k budget, and the returned
// cursor must continue the listing.
func TestRealListComponentsDefaultPagesFitBudget(t *testing.T) {
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
	ctx := context.Background()

	for _, version := range service.bundledElements() {
		cursor := ""
		seen := 0
		for page := 0; ; page++ {
			out := service.ListComponents(ctx, ListComponentsInput{
				Version: version,
				Cursor:  cursor,
			})
			if out.Error != nil {
				t.Errorf("%s page %d error = %+v", version, page, out.Error)
				break
			}
			if size := estimatedResponseBytes(out); size > defaultResponseBudget {
				t.Errorf("%s page %d is %d bytes, want <= %d",
					version, page, size, defaultResponseBudget)
			}
			seen += len(out.Components)
			if out.NextCursor == "" {
				if seen != out.Total {
					t.Errorf("%s collected %d of %d components", version, seen, out.Total)
				}
				break
			}
			cursor = out.NextCursor
			if page > 50 {
				t.Errorf("%s pagination did not terminate", version)
				break
			}
		}
	}
}

// TestRealComponentAPISummariesFitBudget checks the default summary for every
// bundled 5.0.0 component. The default must fit NFR-03 or return a follow-up
// error; silent truncation is not allowed.
func TestRealComponentAPISummariesFitBudget(t *testing.T) {
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
	ctx := context.Background()

	components, _ := allRealComponents(t, service, "5.0.0")
	for _, component := range components {
		out := service.GetComponentAPI(ctx, GetComponentAPIInput{
			TagName: component.TagName,
			Version: "5.0.0",
		})
		if out.Error != nil {
			t.Errorf("%s default summary error = %+v", component.TagName, out.Error)
			continue
		}
		if size := estimatedResponseBytes(out); size > defaultResponseBudget {
			t.Errorf("%s default summary is %d bytes, want <= %d",
				component.TagName, size, defaultResponseBudget)
		}
	}
}
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
