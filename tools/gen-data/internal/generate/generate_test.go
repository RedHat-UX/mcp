// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package generate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/redhat-ux/mcp/internal/data"
)

const lockDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testInputs() *Inputs {
	tokenJSON := []byte(`{
		"color": {
			"brand": {
				"$type": "color",
				"$value": "#ff0000",
				"$description": "Brand color",
				"attributes": {"category": "color", "hex": "ff0000", "rgb": {"a": 1}}
			},
			"accent": {
				"base": {
					"$type": "color",
					"$value": "light-dark(var(--rh-color-accent-base-on-light, #0066cc), var(--rh-color-accent-base-on-dark, #92c5f9))",
					"name": "rh-color-accent-base",
					"original": {"$value": ["{color.accent.base-on-light}", "{color.accent.base-on-dark}"]},
					"attributes": {"category": "color"}
				},
				"base-on-light": {"$type": "color", "$value": "#0066cc", "attributes": {"category": "color"}},
				"base-on-dark": {"$type": "color", "$value": "#92c5f9", "attributes": {"category": "color"}}
			}
		},
		"space": {"md": {"$type": "dimension", "$value": "1rem"}},
		"border": {"width": {"sm": {"$type": "dimension", "$value": "1px"}}}
	}`)
	themeCSS := []byte(`:root {
  --rh-color-accent-base: light-dark(var(--rh-color-accent-base-on-light, #0066cc), var(--rh-color-accent-base-on-dark, #92c5f9));
}`)
	return &Inputs{
		LockDigest: lockDigest,
		Elements: map[string]ElementInput{
			"1.0.0": {
				Manifest:    []byte(`{"schemaVersion":"1.0.0","modules":[]}`),
				PackageJSON: []byte(`{"name":"@rhds/elements","dependencies":{"@rhds/tokens":"^1.0.0"}}`),
			},
			"2.0.0": {
				Manifest:    []byte(`{"schemaVersion":"2.1.0","modules":[]}`),
				PackageJSON: []byte(`{"name":"@rhds/elements","dependencies":{"@rhds/tokens":"^2.0.0"}}`),
			},
			"3.0.0": {
				Manifest:    []byte(`{"schemaVersion":"2.1.0","modules":[]}`),
				PackageJSON: []byte(`{"name":"@rhds/elements","dependencies":{"@rhds/tokens":"^3.0.0"}}`),
			},
		},
		Tokens: map[string]TokenInput{
			"1.0.0": {JSON: tokenJSON},
			"2.0.0": {JSON: tokenJSON},
			"3.0.0": {JSON: tokenJSON, ThemeCSS: themeCSS, HasThemeCSS: true},
		},
		Docs: []DocsInput{{
			Tag:    "v1.0.0",
			Commit: "1111111111111111111111111111111111111111",
			Files: []DocFile{{
				Path:    "elements/rh-button/docs/guidelines.md",
				Content: []byte("---\ntitle: Button\n---\n# Button\n\nUse it.\n\n![x](x.png)\n"),
			}},
			Patterns: []PatternFile{{
				Path:    "docs/patterns/button/patterns/basic.html",
				Content: []byte("<rh-button>Click</rh-button>\n"),
			}},
			Changelog: []byte("# Changelog\n\n## 1.0.0\n\n- Initial release.\n"),
		}},
	}
}

func TestGenerateProducesIndexAndArtifacts(t *testing.T) {
	out := t.TempDir()
	index, err := Generate(testInputs(), out)
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if index.SchemaVersion != data.SchemaVersion {
		t.Errorf("schema version = %d", index.SchemaVersion)
	}
	if index.LockSHA256 != lockDigest {
		t.Errorf("lock digest = %q", index.LockSHA256)
	}
	if got := index.ElementsTokens["1.0.0"]; got != "1.0.0" {
		t.Errorf("elements 1.0.0 -> tokens %q", got)
	}
	if got := index.ElementsTokens["3.0.0"]; got != "3.0.0" {
		t.Errorf("elements 3.0.0 -> tokens %q", got)
	}

	store, err := data.Open(os.DirFS(out))
	if err != nil {
		t.Fatalf("data.Open() = %v", err)
	}
	for _, artifact := range index.Artifacts {
		if _, err := store.Read(artifact.Package, artifact.Version, artifact.Kind); err != nil {
			t.Errorf("reading %s@%s %s: %v", artifact.Package, artifact.Version, artifact.Kind, err)
		}
	}

	manifest, err := store.Read("elements", "3.0.0", "manifest")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(manifest, []byte(`"schemaVersion":"2.1.0"`)) {
		t.Errorf("manifest = %s", manifest)
	}

	tokens, err := store.Read("tokens", "3.0.0", "tokens")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(tokens, []byte(`"$type": "color"`)) {
		t.Errorf("normalized tokens = %s", tokens)
	}
	if bytes.Contains(tokens, []byte("light-dark(")) {
		t.Errorf("normalized tokens still contain light-dark(): %s", tokens)
	}

	themes, err := store.Read("tokens", "3.0.0", "themes")
	if err != nil {
		t.Fatal(err)
	}
	var themeMap map[string]map[string]string
	if err := json.Unmarshal(themes, &themeMap); err != nil {
		t.Fatal(err)
	}
	if got := themeMap["light"]["--rh-color-accent-base"]; got != "#0066cc" {
		t.Errorf("light theme value = %q", got)
	}

	docs, err := store.Read("docs", "v1.0.0", "doc:elements/rh-button/docs/guidelines.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(docs, []byte("<!-- source: RedHat-UX/red-hat-design-system@v1.0.0 elements/rh-button/docs/guidelines.md -->")) {
		t.Errorf("doc source header missing: %s", docs)
	}
	if bytes.Contains(docs, []byte("x.png")) {
		t.Errorf("doc image was not removed: %s", docs)
	}

	patterns, err := store.Read("docs", "v1.0.0", "patterns")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(patterns, []byte("<rh-button>Click</rh-button>")) {
		t.Errorf("patterns = %s", patterns)
	}

	changelog, err := store.Read("release-notes", "latest", "changelog")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(changelog, []byte("Initial release")) {
		t.Errorf("changelog = %s", changelog)
	}

	docsIndex, err := store.Read("docs", "v1.0.0", "docs")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(docsIndex, []byte(`"title": "Button"`)) {
		t.Errorf("docs manifest = %s", docsIndex)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	if _, err := Generate(testInputs(), first); err != nil {
		t.Fatalf("first Generate() = %v", err)
	}
	if _, err := Generate(testInputs(), second); err != nil {
		t.Fatalf("second Generate() = %v", err)
	}
	compareTrees(t, first, second)
}

func TestIsUpToDate(t *testing.T) {
	out := t.TempDir()
	if upToDate, err := IsUpToDate(out, lockDigest); err != nil || upToDate {
		t.Fatalf("IsUpToDate(empty) = %v, %v; want false", upToDate, err)
	}
	if _, err := Generate(testInputs(), out); err != nil {
		t.Fatal(err)
	}
	if upToDate, err := IsUpToDate(out, lockDigest); err != nil || !upToDate {
		t.Fatalf("IsUpToDate(fresh) = %v, %v; want true", upToDate, err)
	}
	if upToDate, err := IsUpToDate(out, "different"); err != nil || upToDate {
		t.Fatalf("IsUpToDate(other digest) = %v, %v; want false", upToDate, err)
	}

	// A stale generator version forces regeneration even with the same lock.
	indexPath := filepath.Join(out, data.IndexPath)
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded data.Index
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.GeneratorVersion = GeneratorVersion + 1
	encoded, err := marshalJSON(&decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if upToDate, err := IsUpToDate(out, lockDigest); err != nil || upToDate {
		t.Fatalf("IsUpToDate(stale generator) = %v, %v; want false", upToDate, err)
	}
	if _, err := Generate(testInputs(), out); err != nil {
		t.Fatal(err)
	}

	// Missing artifacts make the tree stale even when the digest matches.
	index, err := readIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Artifacts) == 0 {
		t.Fatal("no artifacts")
	}
	if err := os.Remove(filepath.Join(out, index.Artifacts[0].Path)); err != nil {
		t.Fatal(err)
	}
	if upToDate, err := IsUpToDate(out, lockDigest); err != nil || upToDate {
		t.Fatalf("IsUpToDate(missing artifact) = %v, %v; want false", upToDate, err)
	}
}

func TestGenerateRejectsUnparseablePackageJSON(t *testing.T) {
	inputs := testInputs()
	inputs.Elements["3.0.0"] = ElementInput{
		Manifest:    []byte(`{"schemaVersion":"2.1.0"}`),
		PackageJSON: []byte(`{`),
	}
	if _, err := Generate(inputs, t.TempDir()); err == nil {
		t.Fatal("Generate() accepted malformed package.json")
	}
}

func TestGenerateRejectsDocTagTraversal(t *testing.T) {
	for _, tag := range []string{"../../escape", "v1.4.5/../../escape", "v1.4.5-beta.1", "1.4.5"} {
		inputs := testInputs()
		inputs.Docs[0].Tag = tag
		if _, err := Generate(inputs, t.TempDir()); err == nil {
			t.Errorf("Generate() accepted docs tag %q", tag)
		}
	}
}

func TestGenerateRejectsUnsafeOutputDirectory(t *testing.T) {
	for _, out := range []string{".", "..", "/", "internal/data"} {
		if _, err := Generate(testInputs(), out); err == nil {
			t.Errorf("Generate(out=%q) accepted an unsafe output directory", out)
		}
	}
}

func TestGenerateRejectsOutputDirectoriesContainingSource(t *testing.T) {
	for _, name := range []string{"go.mod", "sources.lock.json"} {
		out := t.TempDir()
		if err := os.WriteFile(filepath.Join(out, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Generate(testInputs(), out); err == nil {
			t.Errorf("Generate() accepted an output directory containing %s", name)
		}
	}
}

func TestGenerateRequiresLockDigest(t *testing.T) {
	inputs := testInputs()
	inputs.LockDigest = ""
	if _, err := Generate(inputs, t.TempDir()); err == nil {
		t.Fatal("Generate() accepted an empty lock digest")
	}
}

func compareTrees(t *testing.T, first, second string) {
	t.Helper()
	firstFiles := listFiles(t, first)
	secondFiles := listFiles(t, second)
	if len(firstFiles) != len(secondFiles) {
		t.Fatalf("file counts differ: %d != %d", len(firstFiles), len(secondFiles))
	}
	for i := range firstFiles {
		if firstFiles[i] != secondFiles[i] {
			t.Fatalf("file lists differ at %d: %q != %q", i, firstFiles[i], secondFiles[i])
		}
		a, err := os.ReadFile(filepath.Join(first, firstFiles[i]))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(second, secondFiles[i]))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s is not byte-identical", firstFiles[i])
		}
	}
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
