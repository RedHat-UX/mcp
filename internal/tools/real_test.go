// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/cem"
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

	// 82 components overflow the 8 KB budget, so follow the cursor to see the
	// whole listing instead of assuming one page contains every element.
	firstPage := service.ListComponents(context.Background(), ListComponentsInput{
		Version:           "5.0.0",
		IncludeDeprecated: true,
		PageSize:          100,
	})
	if firstPage.Error != nil {
		t.Fatalf("ListComponents() error = %+v", firstPage.Error)
	}
	if firstPage.Total == 0 {
		t.Fatal("ListComponents() returned no components for 5.0.0")
	}
	if firstPage.NextCursor == "" {
		t.Fatal("ListComponents() did not paginate an 82-component listing")
	}
	total := firstPage.Total
	all := append([]ComponentSummary(nil), firstPage.Components...)
	cursor := firstPage.NextCursor
	finished := false
	cursorOnlyPages := 0
	for page := 0; page < 100; page++ {
		// Cursor pages omit both version and pageSize: the cursor carries the
		// version and the default page size applies.
		next := service.ListComponents(context.Background(), ListComponentsInput{
			IncludeDeprecated: true,
			Cursor:            cursor,
		})
		if next.Error != nil {
			t.Fatalf("cursor page %d error = %+v", page+2, next.Error)
		}
		if next.Resolved.Source != "cursor" {
			t.Errorf("cursor page %d source = %q, want cursor", page+2, next.Resolved.Source)
		}
		if got := wireResponseBytes(next); got > defaultResponseBudget {
			t.Errorf("cursor page %d = %d bytes, over the %d byte default",
				page+2, got, defaultResponseBudget)
		}
		cursorOnlyPages++
		all = append(all, next.Components...)
		if next.NextCursor == "" {
			finished = true
			break
		}
		if next.NextCursor == cursor {
			t.Fatalf("cursor page %d returned a self-referential cursor", page+2)
		}
		cursor = next.NextCursor
	}
	if !finished {
		t.Fatal("pagination hit the page cap before the cursor was empty")
	}
	if cursorOnlyPages == 0 {
		t.Fatal("no page was fetched with only a cursor")
	}
	if len(all) != total {
		t.Errorf("collected %d components, want %d", len(all), total)
	}
	withDocs := 0
	withoutDocs := 0
	for _, component := range all {
		if component.DocsAvailable {
			withDocs++
		} else {
			withoutDocs++
		}
	}
	if withDocs == 0 || withoutDocs == 0 {
		t.Fatalf("docsAvailable is not per component: %d with docs, %d without", withDocs, withoutDocs)
	}
	button, ok := findComponent(all, "rh-button")
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
	if accent.Source != tokenSourceCSS {
		t.Errorf("theme-only token source = %q, want %q", accent.Source, tokenSourceCSS)
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
// bundled 5.0.0 component. The default must fit NFR-03, omit whole sections
// with a warning naming each omission, or return the documented follow-up
// error; silent truncation is not allowed. Contract fields (names, types, and
// defaults) and summary metadata must match the normalized manifest exactly.
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

	model, failure := service.loadModel("5.0.0")
	if failure != nil {
		t.Fatalf("loadModel(5.0.0) error = %+v", failure)
	}
	components, _ := allRealComponents(t, service, "5.0.0")
	for _, component := range components {
		modeled, ok := model.Component(component.TagName)
		if !ok {
			t.Errorf("%s is listed but missing from the normalized manifest", component.TagName)
			continue
		}
		out := service.GetComponentAPI(ctx, GetComponentAPIInput{
			TagName: component.TagName,
			Version: "5.0.0",
		})
		if out.Error != nil {
			// The documented follow-up error is the only acceptable failure.
			if out.Error.Code != "response_too_large" {
				t.Errorf("%s default summary error = %+v", component.TagName, out.Error)
			}
			continue
		}
		if size := estimatedResponseBytes(out); size > defaultResponseBudget {
			t.Errorf("%s default summary is %d bytes, want <= %d",
				component.TagName, size, defaultResponseBudget)
		}
		for _, section := range componentAPISections {
			if slices.Contains(out.Sections, section) {
				if !sectionHasPayload(&out, section) {
					t.Errorf("%s lists %s in Sections with no payload",
						component.TagName, section)
				}
				continue
			}
			if !modelSectionHasData(modeled, section) {
				continue
			}
			if sectionHasPayload(&out, section) {
				t.Errorf("%s dropped %s from Sections but still returned its data",
					component.TagName, section)
			}
			if !warnedForSection(out.Resolved.Warnings, section) {
				t.Errorf("%s omitted %s without a warning naming the section; warnings = %v",
					component.TagName, section, out.Resolved.Warnings)
			}
		}
		assertSummaryContractFidelity(t, component.TagName, modeled, out)
	}
}

// TestRealComponentAPISectionsReflectPayload checks every bundled component in
// both detail modes: Sections must list exactly the sections carried in the
// payload. Full detail may omit nothing for a successful response; summary
// omissions must be named in a warning.
func TestRealComponentAPISectionsReflectPayload(t *testing.T) {
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

	model, failure := service.loadModel("5.0.0")
	if failure != nil {
		t.Fatalf("loadModel(5.0.0) error = %+v", failure)
	}
	components, _ := allRealComponents(t, service, "5.0.0")
	for _, component := range components {
		modeled, ok := model.Component(component.TagName)
		if !ok {
			t.Errorf("%s is listed but missing from the normalized manifest", component.TagName)
			continue
		}
		for _, detail := range []string{"summary", "full"} {
			out := service.GetComponentAPI(ctx, GetComponentAPIInput{
				TagName: component.TagName,
				Version: "5.0.0",
				Detail:  detail,
			})
			if out.Error != nil {
				if out.Error.Code != "response_too_large" {
					t.Errorf("%s %s error = %+v", component.TagName, detail, out.Error)
					continue
				}
				if len(out.Sections) != 0 {
					t.Errorf("%s %s returned Sections %v with no payload",
						component.TagName, detail, out.Sections)
				}
				continue
			}
			for _, section := range out.Sections {
				if !sectionHasPayload(&out, section) {
					t.Errorf("%s %s lists %s in Sections with no payload",
						component.TagName, detail, section)
				}
			}
			for _, section := range componentAPISections {
				if !modelSectionHasData(modeled, section) ||
					slices.Contains(out.Sections, section) {
					continue
				}
				if detail == "full" {
					t.Errorf("%s full omitted populated section %s", component.TagName, section)
				} else if !warnedForSection(out.Resolved.Warnings, section) {
					t.Errorf("%s summary omitted populated section %s without a warning; warnings = %v",
						component.TagName, section, out.Resolved.Warnings)
				}
			}
		}
	}
}

func modelSectionHasData(component *cem.Component, section string) bool {
	switch section {
	case sectionAttributes:
		return len(component.Attributes) > 0
	case sectionProperties:
		return len(component.Properties) > 0
	case sectionEvents:
		return len(component.Events) > 0
	case sectionSlots:
		return len(component.Slots) > 0
	case sectionCSSParts:
		return len(component.CSSParts) > 0
	case sectionCSSProperties:
		return len(component.CSSProperties) > 0
	case sectionExamples:
		return len(component.Examples) > 0
	default:
		return false
	}
}

func warnedForSection(warnings []string, section string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, section) {
			return true
		}
	}
	return false
}

func assertSummaryContractFidelity(
	t *testing.T,
	tagName string,
	want *cem.Component,
	got ComponentAPIOutput,
) {
	t.Helper()
	if got.Name != want.Name {
		t.Errorf("%s name = %q, want %q", tagName, got.Name, want.Name)
	}
	if got.Summary != want.Summary {
		t.Errorf("%s summary = %q, want %q", tagName, got.Summary, want.Summary)
	}
	if slices.Contains(got.Sections, sectionAttributes) {
		if len(got.Attributes) != len(want.Attributes) {
			t.Errorf("%s attributes = %d, want %d", tagName, len(got.Attributes), len(want.Attributes))
		} else {
			for i := range got.Attributes {
				actual, expected := got.Attributes[i], want.Attributes[i]
				if actual.Name != expected.Name || actual.Type != expected.Type ||
					actual.Default != expected.Default ||
					actual.FieldName != expected.FieldName ||
					actual.InheritedFrom != expected.InheritedFrom {
					t.Errorf("%s attribute[%d] = %+v, want contract fields from %+v",
						tagName, i, actual, expected)
				}
				if actual.Summary != "" || actual.Description != "" {
					t.Errorf("%s attribute[%d] summary prose was not omitted: %+v",
						tagName, i, actual)
				}
			}
		}
	}
	if slices.Contains(got.Sections, sectionProperties) {
		if len(got.Properties) != len(want.Properties) {
			t.Errorf("%s properties = %d, want %d", tagName, len(got.Properties), len(want.Properties))
		} else {
			for i := range got.Properties {
				actual, expected := got.Properties[i], want.Properties[i]
				if actual.Name != expected.Name || actual.Type != expected.Type ||
					actual.Default != expected.Default ||
					actual.Attribute != expected.Attribute ||
					actual.InheritedFrom != expected.InheritedFrom {
					t.Errorf("%s property[%d] = %+v, want contract fields from %+v",
						tagName, i, actual, expected)
				}
				if actual.Summary != "" || actual.Description != "" {
					t.Errorf("%s property[%d] summary prose was not omitted: %+v",
						tagName, i, actual)
				}
			}
		}
	}
	if slices.Contains(got.Sections, sectionEvents) {
		if len(got.Events) != len(want.Events) {
			t.Errorf("%s events = %d, want %d", tagName, len(got.Events), len(want.Events))
		} else {
			for i := range got.Events {
				actual, expected := got.Events[i], want.Events[i]
				if actual.Name != expected.Name || actual.Type != expected.Type ||
					actual.InheritedFrom != expected.InheritedFrom {
					t.Errorf("%s event[%d] = %+v, want contract fields from %+v",
						tagName, i, actual, expected)
				}
			}
		}
	}
	if slices.Contains(got.Sections, sectionSlots) {
		if len(got.Slots) != len(want.Slots) {
			t.Errorf("%s slots = %d, want %d", tagName, len(got.Slots), len(want.Slots))
		} else {
			for i := range got.Slots {
				actual, expected := got.Slots[i], want.Slots[i]
				if actual.Name != expected.Name ||
					actual.InheritedFrom != expected.InheritedFrom {
					t.Errorf("%s slot[%d] = %+v, want contract fields from %+v",
						tagName, i, actual, expected)
				}
			}
		}
	}
	if slices.Contains(got.Sections, sectionCSSParts) {
		if len(got.CSSParts) != len(want.CSSParts) {
			t.Errorf("%s cssParts = %d, want %d", tagName, len(got.CSSParts), len(want.CSSParts))
		} else {
			for i := range got.CSSParts {
				actual, expected := got.CSSParts[i], want.CSSParts[i]
				if actual.Name != expected.Name ||
					actual.InheritedFrom != expected.InheritedFrom {
					t.Errorf("%s cssPart[%d] = %+v, want contract fields from %+v",
						tagName, i, actual, expected)
				}
			}
		}
	}
	if slices.Contains(got.Sections, sectionCSSProperties) {
		if len(got.CSSProperties) != len(want.CSSProperties) {
			t.Errorf("%s cssProperties = %d, want %d",
				tagName, len(got.CSSProperties), len(want.CSSProperties))
		} else {
			for i := range got.CSSProperties {
				actual, expected := got.CSSProperties[i], want.CSSProperties[i]
				if actual.Name != expected.Name || actual.Default != expected.Default ||
					actual.Syntax != expected.Syntax ||
					actual.InheritedFrom != expected.InheritedFrom {
					t.Errorf("%s cssProperty[%d] = %+v, want contract fields from %+v",
						tagName, i, actual, expected)
				}
			}
		}
	}
	if slices.Contains(got.Sections, sectionExamples) {
		if len(got.Examples) != len(want.Examples) {
			t.Errorf("%s examples = %d, want %d", tagName, len(got.Examples), len(want.Examples))
		} else {
			for i := range got.Examples {
				actual, expected := got.Examples[i], want.Examples[i]
				if actual.Description != expected.Description {
					t.Errorf("%s example[%d] description = %q, want %q",
						tagName, i, actual.Description, expected.Description)
				}
				if actual.URL != "" || actual.Source != "" {
					t.Errorf("%s example[%d] summary should omit references: %+v",
						tagName, i, actual)
				}
			}
		}
	}
}

// TestRealComponentAPIFullPreservesManifestText pins fidelity against the
// 5.0.0 manifest for fields that the previous per-field caps shortened. The
// lengths and strings are exact so a truncation regression cannot pass.
func TestRealComponentAPIFullPreservesManifestText(t *testing.T) {
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

	model, failure := service.loadModel("5.0.0")
	if failure != nil {
		t.Fatalf("loadModel(5.0.0) error = %+v", failure)
	}

	t.Run("accordion panel attribute description", func(t *testing.T) {
		component, ok := model.Component("rh-accordion-panel")
		if !ok {
			t.Fatal("rh-accordion-panel missing")
		}
		expected, ok := findModelAttribute(component.Attributes, "expanded")
		if !ok {
			t.Fatal("rh-accordion-panel attribute expanded missing")
		}
		if got := utf8.RuneCountInString(expected.Description); got != 895 {
			t.Fatalf("manifest expanded description = %d runes, want 895", got)
		}

		full := service.GetComponentAPI(ctx, GetComponentAPIInput{
			TagName:  "rh-accordion-panel",
			Version:  "5.0.0",
			Sections: []string{"attributes"},
			Detail:   "full",
		})
		if full.Error != nil {
			t.Fatalf("full error = %+v", full.Error)
		}
		actual, ok := findAttribute(full.Attributes, "expanded")
		if !ok {
			t.Fatal("expanded missing from full output")
		}
		if actual.Description != expected.Description {
			t.Errorf("expanded description differs from the manifest:\ngot  %q\nwant %q",
				actual.Description, expected.Description)
		}
		if got := utf8.RuneCountInString(actual.Description); got != 895 {
			t.Errorf("full expanded description = %d runes, want 895", got)
		}

		summary := service.GetComponentAPI(ctx, GetComponentAPIInput{
			TagName:  "rh-accordion-panel",
			Version:  "5.0.0",
			Sections: []string{"attributes"},
		})
		if summary.Error != nil {
			t.Fatalf("summary error = %+v", summary.Error)
		}
		actual, ok = findAttribute(summary.Attributes, "expanded")
		if !ok {
			t.Fatal("expanded missing from summary output")
		}
		if actual.Name != expected.Name || actual.Type != expected.Type ||
			actual.Default != expected.Default ||
			actual.FieldName != expected.FieldName ||
			actual.InheritedFrom != expected.InheritedFrom {
			t.Errorf("summary expanded = %+v, want contract fields from %+v", actual, expected)
		}
	})

	t.Run("non-static property default", func(t *testing.T) {
		audio, ok := model.Component("rh-audio-player")
		if !ok {
			t.Fatal("rh-audio-player missing")
		}
		// enUS is a static class field, not an instance property.
		if _, found := findModelProperty(audio.Properties, "enUS"); found {
			t.Error("static field enUS must not be reported as an instance property")
		}

		component, ok := model.Component("rh-footer")
		if !ok {
			t.Fatal("rh-footer missing")
		}
		expected, ok := findModelProperty(component.Properties, "screenSize")
		if !ok {
			t.Fatal("rh-footer property screenSize missing")
		}
		if got := utf8.RuneCountInString(expected.Default); got != 113 {
			t.Fatalf("manifest screenSize default = %d runes, want 113", got)
		}
		out := service.GetComponentAPI(ctx, GetComponentAPIInput{
			TagName:  "rh-footer",
			Version:  "5.0.0",
			Sections: []string{"properties"},
			Detail:   "full",
		})
		if out.Error != nil {
			t.Fatalf("error = %+v", out.Error)
		}
		actual, ok := findProperty(out.Properties, "screenSize")
		if !ok {
			t.Fatal("screenSize missing from output")
		}
		if actual.Default != expected.Default {
			t.Errorf("screenSize default differs from the manifest:\ngot  %q\nwant %q",
				actual.Default, expected.Default)
		}
		if got := utf8.RuneCountInString(actual.Default); got != 113 {
			t.Errorf("full screenSize default = %d runes, want 113", got)
		}
	})

	t.Run("tag attribute type", func(t *testing.T) {
		component, ok := model.Component("rh-tag")
		if !ok {
			t.Fatal("rh-tag missing")
		}
		expected, ok := findModelAttribute(component.Attributes, "color")
		if !ok {
			t.Fatal("rh-tag attribute color missing")
		}
		if got := utf8.RuneCountInString(expected.Type); got != 90 {
			t.Fatalf("manifest color type = %d runes, want 90", got)
		}
		for _, detail := range []string{"summary", "full"} {
			out := service.GetComponentAPI(ctx, GetComponentAPIInput{
				TagName:  "rh-tag",
				Version:  "5.0.0",
				Sections: []string{"attributes"},
				Detail:   detail,
			})
			if out.Error != nil {
				t.Fatalf("%s error = %+v", detail, out.Error)
			}
			actual, ok := findAttribute(out.Attributes, "color")
			if !ok {
				t.Fatalf("color missing from %s output", detail)
			}
			if actual.Type != expected.Type {
				t.Errorf("%s color type = %q, want %q", detail, actual.Type, expected.Type)
			}
			if got := utf8.RuneCountInString(actual.Type); got != 90 {
				t.Errorf("%s color type = %d runes, want 90", detail, got)
			}
		}
	})

	t.Run("accordion example description", func(t *testing.T) {
		component, ok := model.Component("rh-accordion")
		if !ok {
			t.Fatal("rh-accordion missing")
		}
		expected, ok := findModelExampleWithRunes(component.Examples, 233)
		if !ok {
			t.Fatal("rh-accordion 233-rune example missing")
		}
		full := service.GetComponentAPI(ctx, GetComponentAPIInput{
			TagName:  "rh-accordion",
			Version:  "5.0.0",
			Sections: []string{"examples"},
			Detail:   "full",
		})
		if full.Error != nil {
			t.Fatalf("full error = %+v", full.Error)
		}
		actual, ok := findExample(full.Examples, expected.Description)
		if !ok {
			t.Fatal("233-rune example missing from full output")
		}
		if actual.Description != expected.Description {
			t.Errorf("example description differs from the manifest:\ngot  %q\nwant %q",
				actual.Description, expected.Description)
		}
		if got := utf8.RuneCountInString(actual.Description); got != 233 {
			t.Errorf("full example description = %d runes, want 233", got)
		}
		if actual.URL != expected.URL || actual.Source != expected.Source {
			t.Errorf("full example references = %+v, want %+v", actual, expected)
		}

		summary := service.GetComponentAPI(ctx, GetComponentAPIInput{
			TagName:  "rh-accordion",
			Version:  "5.0.0",
			Sections: []string{"examples"},
		})
		if summary.Error != nil {
			t.Fatalf("summary error = %+v", summary.Error)
		}
		actual, ok = findExample(summary.Examples, expected.Description)
		if !ok {
			t.Fatal("233-rune example missing from summary output")
		}
		if actual.Description != expected.Description {
			t.Errorf("summary example description differs from the manifest:\ngot  %q\nwant %q",
				actual.Description, expected.Description)
		}
		if actual.URL != "" || actual.Source != "" {
			t.Errorf("summary example should omit references: %+v", actual)
		}
	})
}

func findModelAttribute(attributes []cem.Attribute, name string) (cem.Attribute, bool) {
	for _, attribute := range attributes {
		if attribute.Name == name {
			return attribute, true
		}
	}
	return cem.Attribute{}, false
}

func findAttribute(attributes []cem.Attribute, name string) (cem.Attribute, bool) {
	return findModelAttribute(attributes, name)
}

func findModelProperty(properties []cem.Property, name string) (cem.Property, bool) {
	for _, property := range properties {
		if property.Name == name {
			return property, true
		}
	}
	return cem.Property{}, false
}

func findProperty(properties []cem.Property, name string) (cem.Property, bool) {
	return findModelProperty(properties, name)
}

func findModelExampleWithRunes(examples []cem.Example, runes int) (cem.Example, bool) {
	for _, example := range examples {
		if utf8.RuneCountInString(example.Description) == runes {
			return example, true
		}
	}
	return cem.Example{}, false
}

func findExample(examples []cem.Example, description string) (cem.Example, bool) {
	for _, example := range examples {
		if example.Description == description {
			return example, true
		}
	}
	return cem.Example{}, false
}

// TestRealPhaseETools exercises the Phase E tools against `make data` output.
func TestRealPhaseETools(t *testing.T) {
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

	guidelines := service.GetDesignGuidelines(context.Background(), DesignGuidelinesInput{
		Element: "rh-button",
		Version: "5.0.0",
	})
	if guidelines.Error != nil {
		t.Fatalf("GetDesignGuidelines() error = %+v", guidelines.Error)
	}
	if len(guidelines.Chunks) == 0 {
		t.Fatal("GetDesignGuidelines() returned no chunks for rh-button")
	}
	if guidelines.Chunks[0].ReleaseTag != "v5.0.0" {
		t.Errorf("release tag = %q, want v5.0.0", guidelines.Chunks[0].ReleaseTag)
	}

	examples := service.GetUsageExamples(context.Background(), UsageExamplesInput{
		TagName: "rh-button",
		Version: "5.0.0",
	})
	if examples.Error != nil {
		t.Fatalf("GetUsageExamples() error = %+v", examples.Error)
	}
	if examples.Total == 0 {
		t.Fatal("GetUsageExamples() returned no examples for rh-button")
	}

	migration := service.GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "4.0.0",
		ToVersion:   "5.0.0",
	})
	if migration.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", migration.Error)
	}
	if migration.ToVersion != "5.0.0" {
		t.Errorf("to version = %q", migration.ToVersion)
	}
	if len(migration.Changes) == 0 || len(migration.ReleaseNotes) == 0 {
		t.Errorf("GetMigrationGuide() = %d changes, %d release notes; want both categories",
			len(migration.Changes), len(migration.ReleaseNotes))
	}

	tokens := service.GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
		Detail:      "full",
		Sections:    []string{"tokens"},
	})
	if tokens.Error != nil {
		t.Fatalf("GetMigrationGuide(1.0.0 -> 5.0.0) error = %+v", tokens.Error)
	}
	if len(tokens.TokenChanges) == 0 {
		t.Error("GetMigrationGuide() returned no token changes for tokens major 1 -> 3")
	}
	if tokens.TotalTokenChanges < len(tokens.TokenChanges) {
		t.Errorf("total token changes = %d, returned = %d",
			tokens.TotalTokenChanges, len(tokens.TokenChanges))
	}
}

// TestRealQueryTokensAllVersions checks exact lookup, theme reporting, and
// source attribution across every bundled tokens release.
func TestRealQueryTokensAllVersions(t *testing.T) {
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
	versions := store.Index().Versions(packageTokens, kindTokens)
	if len(versions) != 18 {
		t.Fatalf("bundled tokens versions = %d, want 18", len(versions))
	}
	for _, version := range versions {
		t.Run(version, func(t *testing.T) {
			out := service.QueryTokens(context.Background(), QueryTokensInput{
				Token:   "color-red-50",
				Version: version,
				Theme:   "light",
			})
			if out.Error != nil {
				t.Fatalf("QueryTokens() error = %+v", out.Error)
			}
			if len(out.Tokens) != 1 {
				t.Fatalf("tokens = %+v", out.Tokens)
			}
			if out.Tokens[0].CSSVariable != "--rh-color-red-50" {
				t.Errorf("css variable = %q", out.Tokens[0].CSSVariable)
			}
			if out.Tokens[0].Source != tokenSourceJSON {
				t.Errorf("source = %q", out.Tokens[0].Source)
			}
		})
	}
}

// TestRealThemeLookups checks light and dark lookups for the tokens majors
// that publish a theme map.
func TestRealThemeLookups(t *testing.T) {
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

	for _, version := range []string{"2.2.4", "3.0.0", "3.2.0"} {
		t.Run(version, func(t *testing.T) {
			light := service.QueryTokens(context.Background(), QueryTokensInput{
				Token: "color-accent-base", Version: version, Theme: "light",
			})
			dark := service.QueryTokens(context.Background(), QueryTokensInput{
				Token: "color-accent-base", Version: version, Theme: "dark",
			})
			if light.Error != nil || dark.Error != nil {
				t.Fatalf("errors = %+v / %+v", light.Error, dark.Error)
			}
			if light.Tokens[0].ThemeValue == "" || dark.Tokens[0].ThemeValue == "" {
				t.Fatalf("theme values missing: light=%+v dark=%+v",
					light.Tokens[0], dark.Tokens[0])
			}
			if light.Tokens[0].ThemeValue == dark.Tokens[0].ThemeValue {
				t.Errorf("light and dark values are identical: %q", light.Tokens[0].ThemeValue)
			}
		})
	}
}

// TestRealResourcesRegister checks the MCP resource surface against the full
// generated data tree.
func TestRealResourcesRegister(t *testing.T) {
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
	client := connectToolClient(t, New(store, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	uris := make(map[string]bool)
	total := 0
	cursor := ""
	for {
		list, err := client.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			t.Fatalf("ListResources() error = %v", err)
		}
		total += len(list.Resources)
		for _, resource := range list.Resources {
			uris[resource.URI] = true
		}
		if list.NextCursor == "" {
			break
		}
		cursor = list.NextCursor
	}
	// Progressive disclosure keeps resources/list to the two static
	// resources; artifact data is read through the rhds://data templates.
	if total != 2 {
		t.Errorf("resources = %d, want 2 static resources", total)
	}
	for _, want := range []string{
		migrationsResourceURI,
		packagesResourceURI,
	} {
		if !uris[want] {
			t.Errorf("resources are missing %q", want)
		}
	}

	notes, err := client.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhds://data/release-notes?version=5.0.0",
	})
	if err != nil {
		t.Fatalf("ReadResource(release notes) error = %v", err)
	}
	if len(notes.Contents) != 1 || !bytes.Contains([]byte(notes.Contents[0].Text), []byte("## 5.0.0")) {
		t.Errorf("release-note resource = %+v", notes.Contents)
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

// TestRealSearchTruncationFlag pins that a budget-trimmed search page marks
// the output truncated. The 5.0.0 "button" search has more hits than fit in
// the default budget.
func TestRealSearchTruncationFlag(t *testing.T) {
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
	out := New(store, nil).Search(context.Background(), SearchInput{
		Query:   "button",
		Version: "5.0.0",
		Limit:   50,
	})
	if out.Error != nil {
		t.Fatalf("Search() error = %+v", out.Error)
	}
	if out.Total <= len(out.Hits) {
		t.Fatalf("search returned %d of %d hits; want a budget-trimmed page", len(out.Hits), out.Total)
	}
	if !out.Truncated {
		t.Errorf("search dropped %d of %d hits with truncated=false", out.Total-len(out.Hits), out.Total)
	}
	if size := wireResponseBytes(out); size > defaultResponseBudget {
		t.Errorf("response = %d bytes, over the %d byte default", size, defaultResponseBudget)
	}
}
