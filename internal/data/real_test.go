// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package data

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRealGeneratedData runs against the output of `make data`. It is skipped
// on a clean checkout, where dist contains only .gitkeep.
func TestRealGeneratedData(t *testing.T) {
	dist := filepath.Join("dist")
	if _, err := os.Stat(filepath.Join(dist, IndexPath)); err != nil {
		t.Skip("generated data is absent; run make data")
	}
	store, err := Open(os.DirFS(dist))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	index := store.Index()
	if len(index.Artifacts) < 1000 {
		t.Fatalf("index has %d artifacts, want at least 1000", len(index.Artifacts))
	}
	if len(index.LockSHA256) != 64 {
		t.Errorf("lock digest = %q, want a sha256", index.LockSHA256)
	}
	if got, ok := index.TokensForElements("5.0.0"); !ok || got != "3.2.0" {
		t.Errorf("elements 5.0.0 -> tokens %q (ok=%v), want 3.2.0", got, ok)
	}

	// Every artifact decompresses and matches its recorded checksum.
	for _, artifact := range index.Artifacts {
		if _, err := store.Read(artifact.Package, artifact.Version, artifact.Kind); err != nil {
			t.Fatalf("reading %s@%s %s: %v", artifact.Package, artifact.Version, artifact.Kind, err)
		}
	}

	manifest, err := store.Read("elements", "5.0.0", "manifest")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(manifest) {
		t.Error("elements 5.0.0 manifest is not valid JSON")
	}

	tokens, err := store.Read("tokens", "3.2.0", "tokens")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(tokens, []byte(`"light-dark(`)) {
		t.Error("tokens 3.2.0 still contains light-dark()")
	}
	if bytes.Contains(tokens, []byte(`"filePath"`)) {
		t.Error("tokens 3.2.0 still contains Style Dictionary fields")
	}

	themes, err := store.Read("tokens", "3.2.0", "themes")
	if err != nil {
		t.Fatal(err)
	}
	var themeMap map[string]map[string]string
	if err := json.Unmarshal(themes, &themeMap); err != nil {
		t.Fatal(err)
	}
	if got := themeMap["light"]["--rh-color-accent-base"]; got != "#0066cc" {
		t.Errorf("light accent-base = %q", got)
	}
	if got := themeMap["dark"]["--rh-color-accent-base"]; got != "#92c5f9" {
		t.Errorf("dark accent-base = %q", got)
	}

	docs, err := store.Read("docs", "v5.0.0", "doc:elements/rh-button/docs/00-overview.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(docs, []byte("<!-- source: RedHat-UX/red-hat-design-system@v5.0.0")) {
		t.Error("docs page has no source header")
	}

	aiDoc, err := store.Read("docs", "v5.0.0", "doc:docs/ai-guidelines/content/ai-design-principles.md")
	if err != nil {
		t.Fatalf("ai-guidelines page: %v", err)
	}
	if !bytes.Contains(aiDoc, []byte("project-felt/ai-guidelines@3e53486f83eba02fdecdb67390d1c4704bda5817")) {
		t.Error("ai-guidelines page does not record the pinned submodule commit")
	}

	patterns, err := store.Read("docs", "v5.0.0", "patterns")
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) == 0 {
		t.Error("patterns bundle is empty")
	}

	changelog, err := store.Read("release-notes", "latest", "changelog")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(changelog, []byte("Major Changes")) {
		t.Error("changelog does not look like a changelog")
	}
}
