// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/resolve"
)

func TestSearchComponents(t *testing.T) {
	out := fixtureService(t).Search(context.Background(), SearchInput{
		Query: "button",
		Scope: scopeComponents,
	})
	if out.Error != nil {
		t.Fatalf("Search() error = %+v", out.Error)
	}
	if out.Total == 0 || len(out.Hits) == 0 {
		t.Fatal("no component hits")
	}
	hit := out.Hits[0]
	if hit.Type != "component" || hit.Title != "rh-button" {
		t.Errorf("hit = %+v", hit)
	}
	if hit.Version != "5.0.0" || hit.Path == "" || hit.Excerpt == "" {
		t.Errorf("hit = %+v", hit)
	}
}

func TestSearchTokens(t *testing.T) {
	out := fixtureService(t).Search(context.Background(), SearchInput{
		Query: "brand",
		Scope: scopeTokens,
	})
	if out.Error != nil {
		t.Fatalf("Search() error = %+v", out.Error)
	}
	if out.Total == 0 || len(out.Hits) == 0 {
		t.Fatal("no token hits")
	}
	for _, hit := range out.Hits {
		if hit.Type != "token" {
			t.Errorf("hit type = %q, want token", hit.Type)
		}
		if hit.Version != "3.2.0" {
			t.Errorf("hit version = %q", hit.Version)
		}
	}
}

func TestSearchDocs(t *testing.T) {
	out := fixtureService(t).Search(context.Background(), SearchInput{
		Query: "accessibility",
		Scope: scopeDocs,
	})
	if out.Error != nil {
		t.Fatalf("Search() error = %+v", out.Error)
	}
	if out.Total == 0 {
		t.Fatal("no docs hits")
	}
	hit := out.Hits[0]
	if hit.Type != "doc" || hit.Version != "v5.0.0" {
		t.Errorf("hit = %+v", hit)
	}
	if !strings.Contains(strings.ToLower(hit.Excerpt), "accessibility") {
		t.Errorf("excerpt = %q, want query context", hit.Excerpt)
	}
}

func TestSearchExamples(t *testing.T) {
	out := fixtureService(t).Search(context.Background(), SearchInput{
		Query: "card",
		Scope: scopeExamples,
	})
	if out.Error != nil {
		t.Fatalf("Search() error = %+v", out.Error)
	}
	if out.Total == 0 {
		t.Fatal("no example hits")
	}
	for _, hit := range out.Hits {
		if hit.Type != "example" {
			t.Errorf("hit type = %q", hit.Type)
		}
	}
}

func TestSearchAllRanksAndLimits(t *testing.T) {
	out := fixtureService(t).Search(context.Background(), SearchInput{
		Query: "button",
		Scope: scopeAll,
		Limit: 2,
	})
	if out.Error != nil {
		t.Fatalf("Search() error = %+v", out.Error)
	}
	if out.Total < len(out.Hits) {
		t.Errorf("total = %d, hits = %d", out.Total, len(out.Hits))
	}
	if len(out.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(out.Hits))
	}
	if out.Hits[0].Score < out.Hits[1].Score {
		t.Errorf("hits are not ranked: %+v", out.Hits)
	}
}

func TestSearchScopeFilter(t *testing.T) {
	out := fixtureService(t).Search(context.Background(), SearchInput{
		Query: "button",
		Scope: scopeTokens,
	})
	if out.Error != nil {
		t.Fatalf("Search() error = %+v", out.Error)
	}
	for _, hit := range out.Hits {
		if hit.Type != "token" {
			t.Errorf("scope=tokens leaked a %s hit", hit.Type)
		}
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	out := fixtureService(t).Search(context.Background(), SearchInput{Query: "  "})
	if out.Error == nil || out.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v", out.Error)
	}
}

func TestSearchRejectsUnknownScope(t *testing.T) {
	out := fixtureService(t).Search(context.Background(), SearchInput{
		Query: "button",
		Scope: "everything",
	})
	if out.Error == nil || out.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v", out.Error)
	}
}

func TestSearchLimitIsCapped(t *testing.T) {
	out := fixtureService(t).Search(context.Background(), SearchInput{
		Query: "button",
		Limit: maxSearchLimit + 1,
	})
	if out.Error != nil {
		t.Fatalf("Search() error = %+v", out.Error)
	}
	if !strings.Contains(strings.Join(out.Resolved.Warnings, " "), "capped") {
		t.Errorf("warnings = %v", out.Resolved.Warnings)
	}
}

func TestExcerptTextHandlesMultibytePrefix(t *testing.T) {
	text := "İstanbul café " + strings.Repeat("x", 200) + " accessibility guidance"
	got := excerptText(text, "accessibility", 40)
	if !strings.Contains(strings.ToLower(got), "accessibility") {
		t.Errorf("excerpt = %q, want the query at a valid rune boundary", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("excerpt is not valid UTF-8: %q", got)
	}
}

// failingReadFS makes one file unreadable while the rest of the fixture tree
// stays intact.
type failingReadFS struct {
	base     fs.FS
	failPath string
	err      error
}

func (f failingReadFS) Open(name string) (fs.File, error) {
	if name == f.failPath {
		return nil, f.err
	}
	return f.base.Open(name)
}

// tamperedFS changes one file's bytes without updating the fixture index, so
// the store reports a checksum mismatch.
type tamperedFS struct {
	base     fs.FS
	path     string
	contents []byte
}

func (f tamperedFS) Open(name string) (fs.File, error) {
	if name == f.path {
		return fstest.MapFS{name: &fstest.MapFile{Data: f.contents}}.Open(name)
	}
	return f.base.Open(name)
}

func searchServiceFrom(t *testing.T, fsys fs.FS) *Service {
	t.Helper()
	store, err := data.Open(fsys)
	if err != nil {
		t.Fatalf("data.Open() error = %v", err)
	}
	return New(store, nil)
}

func TestSearchExamplesWarnsWhenElementsManifestUnavailable(t *testing.T) {
	service := searchServiceFrom(t, failingReadFS{
		base:     os.DirFS("testdata"),
		failPath: "elements/custom-elements.json",
		err:      fs.ErrPermission,
	})

	out := service.Search(context.Background(), SearchInput{
		Query: "button",
		Scope: scopeExamples,
	})
	if out.Error != nil {
		t.Fatalf("Search() error = %+v", out.Error)
	}
	if !hasWarning(out.Resolved.Warnings, "component example search skipped") {
		t.Errorf("warnings = %v, want a skipped component example search notice", out.Resolved.Warnings)
	}
	if len(out.Hits) == 0 {
		t.Fatalf("hits = 0, want the pattern hits collected before the manifest failure")
	}
}

func TestSearchDocsWarnsWhenContentIsUnavailable(t *testing.T) {
	tests := []struct {
		name string
		fsys fs.FS
	}{
		{
			name: "read failure",
			fsys: failingReadFS{
				base:     os.DirFS("testdata"),
				failPath: "docs/v5.0.0/patterns/button.md",
				err:      fs.ErrPermission,
			},
		},
		{
			name: "checksum mismatch",
			fsys: tamperedFS{
				base:     os.DirFS("testdata"),
				path:     "docs/v5.0.0/patterns/button.md",
				contents: []byte("# Tampered\n"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := searchServiceFrom(t, test.fsys)
			out := service.Search(context.Background(), SearchInput{
				Query: "button",
				Scope: scopeDocs,
			})
			if out.Error != nil {
				t.Fatalf("Search() error = %+v", out.Error)
			}
			if !hasWarning(out.Resolved.Warnings, "docs content unavailable") {
				t.Errorf("warnings = %v, want a docs content warning", out.Resolved.Warnings)
			}
			if len(out.Hits) == 0 {
				t.Fatal("hits = 0, want title-based hits for the records that remain")
			}
		})
	}
}

func TestSearchValidatesInputBeforeResolution(t *testing.T) {
	service := fixtureService(t)
	calls := 0
	service.resolveWorkspace = func(resolve.Options) (resolve.Result, error) {
		calls++
		return resolve.Result{}, nil
	}

	tests := []struct {
		name  string
		input SearchInput
	}{
		{"empty query", SearchInput{Query: "  "}},
		{"unknown scope", SearchInput{Query: "button", Scope: "everything"}},
		{"negative limit", SearchInput{Query: "button", Limit: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls = 0
			_, out := service.searchWithEnv(
				context.Background(), &mcp.CallToolRequest{}, test.input)
			if calls != 0 {
				t.Errorf("workspace resolver calls = %d, want 0 before validation", calls)
			}
			if out.Error == nil || out.Error.Code != "invalid_argument" {
				t.Errorf("error = %+v, want invalid_argument", out.Error)
			}
		})
	}
}
