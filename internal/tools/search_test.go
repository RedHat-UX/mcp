// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"strings"
	"testing"
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
