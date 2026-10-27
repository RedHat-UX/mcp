// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/redhat-ux/mcp/internal/data"
)

func fixtureStore(t *testing.T) *data.Store {
	t.Helper()
	store, err := data.Open(os.DirFS("testdata"))
	if err != nil {
		t.Fatalf("opening fixture store: %v", err)
	}
	return store
}

func fixtureService(t *testing.T) *Service {
	t.Helper()
	return New(fixtureStore(t), nil)
}

func memoryService(t *testing.T, manifestJSON []byte, version string) *Service {
	t.Helper()
	sum := sha256.Sum256(manifestJSON)
	index := data.Index{
		SchemaVersion: data.SchemaVersion,
		Artifacts: []data.Artifact{{
			Package: "elements",
			Version: version,
			Kind:    "manifest",
			Path:    "elements/custom-elements.json",
			SHA256:  hex.EncodeToString(sum[:]),
			Bytes:   int64(len(manifestJSON)),
		}},
	}
	indexJSON, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshaling index: %v", err)
	}
	store, err := data.Open(fstest.MapFS{
		"index.json":                    &fstest.MapFile{Data: indexJSON},
		"elements/custom-elements.json": &fstest.MapFile{Data: manifestJSON},
	})
	if err != nil {
		t.Fatalf("opening memory store: %v", err)
	}
	return New(store, nil)
}

func largeManifest(t *testing.T, count int) []byte {
	t.Helper()
	modules := make([]any, 0, count)
	for i := range count {
		modules = append(modules, map[string]any{
			"kind": "javascript-module",
			"path": fmt.Sprintf("./rh-test-%03d.js", i),
			"declarations": []any{map[string]any{
				"kind":          "class",
				"customElement": true,
				"name":          fmt.Sprintf("RhTest%03d", i),
				"tagName":       fmt.Sprintf("rh-test-%03d", i),
				"summary":       "Test component.",
			}},
		})
	}
	encoded, err := json.Marshal(map[string]any{
		"schemaVersion": "2.1.0",
		"modules":       modules,
	})
	if err != nil {
		t.Fatalf("marshaling manifest: %v", err)
	}
	return encoded
}

func TestListComponentsDefaultPage(t *testing.T) {
	out := fixtureService(t).ListComponents(context.Background(), ListComponentsInput{})

	if out.Error != nil {
		t.Fatalf("ListComponents() error = %+v", out.Error)
	}
	if out.Resolved.Elements != "5.0.0" {
		t.Errorf("resolved version = %q, want 5.0.0", out.Resolved.Elements)
	}
	// rh-dialog is deprecated, so it is excluded by default.
	if out.Total != 6 {
		t.Errorf("total = %d, want 6", out.Total)
	}
	if len(out.Components) != 6 {
		t.Fatalf("components = %d, want 6", len(out.Components))
	}
	if out.NextCursor != "" {
		t.Errorf("next cursor = %q, want empty", out.NextCursor)
	}
	if !slices.IsSortedFunc(out.Components, func(a, b ComponentSummary) int {
		switch {
		case a.TagName < b.TagName:
			return -1
		case a.TagName > b.TagName:
			return 1
		default:
			return 0
		}
	}) {
		t.Error("components are not sorted by tag name")
	}

	button, ok := findComponent(out.Components, "rh-button")
	if !ok {
		t.Fatal("rh-button missing from results")
	}
	if button.Summary == "" {
		t.Error("rh-button summary is empty")
	}
	if button.ModulePath != "./rhds/elements/rh-button/rh-button.js" {
		t.Errorf("module path = %q", button.ModulePath)
	}
	if button.PackageSubpath != "rhds/elements/rh-button/rh-button.js" {
		t.Errorf("package subpath = %q", button.PackageSubpath)
	}
	if !button.DocsAvailable {
		t.Error("rh-button docs should be available")
	}
	if button.Deprecated {
		t.Error("rh-button should not be deprecated")
	}
}

func TestListComponentsPaginatesWithOpaqueCursor(t *testing.T) {
	svc := fixtureService(t)
	ctx := context.Background()

	var tagNames []string
	cursor := ""
	pages := 0
	for {
		out := svc.ListComponents(ctx, ListComponentsInput{Cursor: cursor, PageSize: 2})
		if out.Error != nil {
			t.Fatalf("page %d error = %+v", pages, out.Error)
		}
		if len(out.Components) > 2 {
			t.Fatalf("page %d returned %d components, want at most 2", pages, len(out.Components))
		}
		for _, component := range out.Components {
			tagNames = append(tagNames, component.TagName)
		}
		pages++
		if out.NextCursor == "" {
			break
		}
		cursor = out.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3", pages)
	}
	if len(tagNames) != 6 {
		t.Fatalf("collected %d tag names, want 6: %v", len(tagNames), tagNames)
	}
	if !slices.IsSorted(tagNames) {
		t.Errorf("tag names are not sorted across pages: %v", tagNames)
	}
	seen := make(map[string]bool, len(tagNames))
	for _, tagName := range tagNames {
		if seen[tagName] {
			t.Errorf("duplicate tag name across pages: %s", tagName)
		}
		seen[tagName] = true
	}
}

func TestListComponentsRejectsCursorFromAnotherVersion(t *testing.T) {
	out := fixtureService(t).ListComponents(context.Background(), ListComponentsInput{
		Version: "5.0.0",
		Cursor:  encodeCursor("4.0.0", 0, false),
	})
	if out.Error == nil {
		t.Fatal("error = nil, want cursor_version_mismatch")
	}
	if out.Error.Code != "cursor_version_mismatch" {
		t.Errorf("error code = %q, want cursor_version_mismatch", out.Error.Code)
	}
}

func TestListComponentsRejectsInvalidCursor(t *testing.T) {
	out := fixtureService(t).ListComponents(context.Background(), ListComponentsInput{
		Cursor: "not-a-cursor",
	})
	if out.Error == nil {
		t.Fatal("error = nil, want invalid_cursor")
	}
	if out.Error.Code != "invalid_cursor" {
		t.Errorf("error code = %q, want invalid_cursor", out.Error.Code)
	}
}

func TestListComponentsIncludesDeprecated(t *testing.T) {
	out := fixtureService(t).ListComponents(context.Background(), ListComponentsInput{
		IncludeDeprecated: true,
	})
	if out.Error != nil {
		t.Fatalf("ListComponents() error = %+v", out.Error)
	}
	if out.Total != 7 {
		t.Errorf("total = %d, want 7", out.Total)
	}
	dialog, ok := findComponent(out.Components, "rh-dialog")
	if !ok {
		t.Fatal("rh-dialog missing when includeDeprecated is true")
	}
	if !dialog.Deprecated {
		t.Error("rh-dialog should be deprecated")
	}
}

func TestListComponentsUnknownVersion(t *testing.T) {
	out := fixtureService(t).ListComponents(context.Background(), ListComponentsInput{
		Version: "9.9.9",
	})
	if out.Error == nil {
		t.Fatal("error = nil, want artifact_not_found")
	}
	if out.Error.Code != "artifact_not_found" {
		t.Errorf("error code = %q, want artifact_not_found", out.Error.Code)
	}
}

func TestListComponentsWithoutDataReturnsStructuredError(t *testing.T) {
	out := New(nil, data.ErrNoData).ListComponents(context.Background(), ListComponentsInput{})
	if out.Error == nil {
		t.Fatal("error = nil, want no_bundled_data")
	}
	if out.Error.Code != "no_bundled_data" {
		t.Errorf("error code = %q, want no_bundled_data", out.Error.Code)
	}
	if out.Error.Hint == "" {
		t.Error("structured error should carry a hint")
	}
}

func findComponent(components []ComponentSummary, tagName string) (ComponentSummary, bool) {
	for _, component := range components {
		if component.TagName == tagName {
			return component, true
		}
	}
	return ComponentSummary{}, false
}

func TestListComponentsExplicitOlderVersion(t *testing.T) {
	out := fixtureService(t).ListComponents(context.Background(), ListComponentsInput{
		Version: "4.0.0",
	})
	if out.Error != nil {
		t.Fatalf("ListComponents() error = %+v", out.Error)
	}
	if out.Resolved.Elements != "4.0.0" {
		t.Errorf("elements = %q", out.Resolved.Elements)
	}
	if out.Resolved.Docs != "v4.2.2" {
		t.Errorf("docs = %q, want v4.2.2", out.Resolved.Docs)
	}
	if out.Total != 1 || len(out.Components) != 1 || out.Components[0].TagName != "rh-card" {
		t.Errorf("components = %+v, want only rh-card", out.Components)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	cursor := encodeCursor("5.0.0", 25, true)
	payload, toolErr := decodeCursor(cursor)
	if toolErr != nil {
		t.Fatalf("decodeCursor() error = %+v", toolErr)
	}
	if payload.Version != "5.0.0" || payload.Offset != 25 || !payload.IncludeDeprecated {
		t.Errorf("payload = %+v, want version 5.0.0 offset 25 includeDeprecated true", payload)
	}
}

func TestListComponentsRejectsCursorFilterMismatch(t *testing.T) {
	svc := fixtureService(t)
	first := svc.ListComponents(context.Background(), ListComponentsInput{PageSize: 2})
	if first.Error != nil {
		t.Fatalf("first page error = %+v", first.Error)
	}
	if first.NextCursor == "" {
		t.Fatal("first page has no cursor")
	}

	out := svc.ListComponents(context.Background(), ListComponentsInput{
		Cursor:            first.NextCursor,
		PageSize:          2,
		IncludeDeprecated: true,
	})
	if out.Error == nil {
		t.Fatal("error = nil, want cursor_filter_mismatch")
	}
	if out.Error.Code != "cursor_filter_mismatch" {
		t.Errorf("error code = %q, want cursor_filter_mismatch", out.Error.Code)
	}
}

func TestListComponentsDefaultPageSize(t *testing.T) {
	svc := memoryService(t, largeManifest(t, 130), "5.0.0")
	out := svc.ListComponents(context.Background(), ListComponentsInput{})
	if out.Error != nil {
		t.Fatalf("ListComponents() error = %+v", out.Error)
	}
	if out.Total != 130 {
		t.Errorf("total = %d, want 130", out.Total)
	}
	if len(out.Components) != defaultPageSize {
		t.Errorf("components = %d, want default %d", len(out.Components), defaultPageSize)
	}
	if out.NextCursor == "" {
		t.Error("expected a next cursor for 130 components")
	}
}

func TestListComponentsMaxPageSize(t *testing.T) {
	svc := memoryService(t, largeManifest(t, 130), "5.0.0")
	out := svc.ListComponents(context.Background(), ListComponentsInput{PageSize: 1000})
	if out.Error != nil {
		t.Fatalf("ListComponents() error = %+v", out.Error)
	}
	if len(out.Components) != maxPageSize {
		t.Errorf("components = %d, want maximum %d", len(out.Components), maxPageSize)
	}
	if len(out.Resolved.Warnings) == 0 {
		t.Error("expected a pageSize cap warning")
	}
}
