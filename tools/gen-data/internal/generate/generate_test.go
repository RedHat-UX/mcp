// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package generate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/tools/gen-data/internal/lock"
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

func TestValidateOutputDirRejectsUnsafePaths(t *testing.T) {
	for _, out := range []string{".", "..", "/", "internal/data"} {
		if err := validateOutputDir(out); err == nil {
			t.Errorf("validateOutputDir(%q) accepted an unsafe output directory", out)
		}
	}
}

func TestValidateOutputDirRejectsProtectedRoots(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{os.TempDir(), home, cwd} {
		err := validateOutputDir(out)
		if err == nil {
			t.Errorf("validateOutputDir(%q) accepted a protected directory", out)
			continue
		}
		if !strings.Contains(err.Error(), "protected directory") {
			t.Errorf("validateOutputDir(%q) error = %v, want a protected-directory refusal", out, err)
		}
	}
}

func TestValidateOutputDirRejectsRepositoryRoot(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := findRepositoryRoot(cwd)
	if repoRoot == "" {
		t.Fatal("findRepositoryRoot() did not find the module root")
	}
	err = validateOutputDir(repoRoot)
	if err == nil {
		t.Fatalf("validateOutputDir(%q) accepted the repository root", repoRoot)
	}
	if !strings.Contains(err.Error(), "contains the repository") {
		t.Fatalf("validateOutputDir(%q) error = %v, want a repository-ancestor refusal", repoRoot, err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Fatalf("repository go.mod is missing after validation: %v", err)
	}
}

func TestValidateOutputDirFindsRepositoryRootFromOutput(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	t.Chdir(outside)

	err := validateOutputDir(repo)
	if err == nil {
		t.Fatalf("validateOutputDir(%q) accepted a repository root found from the output path", repo)
	}
	if !strings.Contains(err.Error(), "contains the repository") {
		t.Fatalf("validateOutputDir(%q) error = %v, want a repository-ancestor refusal", repo, err)
	}
}

func TestIsInternalDataDistAnchoredToActiveRepository(t *testing.T) {
	active := t.TempDir()
	activeDist := filepath.Join(active, "internal", "data", "dist")
	if !isInternalDataDist(activeDist, active) {
		t.Error("isInternalDataDist() rejected the active repository's dist")
	}
	other := filepath.Join(t.TempDir(), "internal", "data", "dist")
	if isInternalDataDist(other, active) {
		t.Error("isInternalDataDist() accepted another repository's dist")
	}
	if isInternalDataDist(activeDist, "") {
		t.Error("isInternalDataDist() accepted a dist without a repository root")
	}
}

func TestGenerateAcceptsGitkeepOnlyDirectory(t *testing.T) {
	out, err := os.MkdirTemp("", "rhds-gen-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(out) }()
	if err := os.WriteFile(filepath.Join(out, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(testInputs(), out); err != nil {
		t.Fatalf("Generate() rejected a clean-checkout .gitkeep directory: %v", err)
	}
}

func TestGenerateRefusesNameCollisionWithoutMarker(t *testing.T) {
	out, err := os.MkdirTemp("", "rhds-gen-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(out) }()

	files := map[string]string{
		filepath.Join("docs", "user.md"):     "user docs",
		filepath.Join("tokens", "user.json"): "user tokens",
	}
	for name, content := range files {
		full := filepath.Join(out, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Generate(testInputs(), out); err == nil {
		t.Fatal("Generate() accepted name-colliding user directories without a marker")
	}
	for name, want := range files {
		content, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("Generate() removed %s: %v", name, err)
		}
		if string(content) != want {
			t.Errorf("%s content = %q, want %q", name, content, want)
		}
	}
}

func TestGenerateRefusesSymlinkOutput(t *testing.T) {
	target, err := os.MkdirTemp("", "rhds-gen-target-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(target) }()
	linkDir, err := os.MkdirTemp("", "rhds-gen-link-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(linkDir) }()
	link := filepath.Join(linkDir, "output")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(testInputs(), link); err == nil {
		t.Fatal("Generate() accepted an output path that is a symlink")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target was removed: %v", err)
	}
}

func TestGenerateDoesNotFollowOutputEntrySymlink(t *testing.T) {
	out, err := os.MkdirTemp("", "rhds-gen-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(out) }()
	if _, err := Generate(testInputs(), out); err != nil {
		t.Fatalf("first Generate() = %v", err)
	}
	outside, err := os.MkdirTemp("", "rhds-gen-outside-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(outside) }()
	userFile := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(userFile, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	elements := filepath.Join(out, "elements")
	if err := os.RemoveAll(elements); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, elements); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(testInputs(), out); err != nil {
		t.Fatalf("Generate() with an entry symlink = %v", err)
	}
	content, err := os.ReadFile(userFile)
	if err != nil {
		t.Fatalf("Generate() followed the entry symlink: %v", err)
	}
	if string(content) != "keep" {
		t.Fatalf("outside file content = %q, want %q", content, "keep")
	}
}

func TestGenerateRefusesUnexpectedEntryAfterValidMarker(t *testing.T) {
	out, err := os.MkdirTemp("", "rhds-gen-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(out) }()
	if _, err := Generate(testInputs(), out); err != nil {
		t.Fatalf("first Generate() = %v", err)
	}
	unexpected := filepath.Join(out, "unrelated.txt")
	if err := os.WriteFile(unexpected, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Generate(testInputs(), out)
	if err == nil {
		t.Fatal("Generate() accepted an unexpected top-level entry")
	}
	if !strings.Contains(err.Error(), "unrelated.txt") {
		t.Errorf("Generate() error = %v, want the unexpected entry name", err)
	}
	if _, err := os.Stat(unexpected); err != nil {
		t.Fatalf("unexpected entry was removed: %v", err)
	}
}

func TestGenerateRefusesNestedSourceFiles(t *testing.T) {
	out, err := os.MkdirTemp("", "rhds-gen-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(out) }()
	if _, err := Generate(testInputs(), out); err != nil {
		t.Fatalf("first Generate() = %v", err)
	}
	nested := filepath.Join(out, "elements", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(nested, "go.mod")
	if err := os.WriteFile(sourceFile, []byte("module example.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Generate(testInputs(), out)
	if err == nil {
		t.Fatal("Generate() accepted a nested go.mod under an output entry")
	}
	if !strings.Contains(err.Error(), "elements/nested/go.mod") {
		t.Errorf("Generate() error = %v, want the nested go.mod path", err)
	}
	if _, err := os.Stat(sourceFile); err != nil {
		t.Fatalf("nested go.mod was removed: %v", err)
	}
}

func TestGenerateRefusesUnrelatedUserFile(t *testing.T) {
	out, err := os.MkdirTemp("", "rhds-gen-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(out) }()

	userFile := filepath.Join(out, "notes.txt")
	if err := os.WriteFile(userFile, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(testInputs(), out); err == nil {
		t.Fatal("Generate() accepted a directory containing an unrelated file")
	}
	content, err := os.ReadFile(userFile)
	if err != nil {
		t.Fatalf("Generate() removed the user file: %v", err)
	}
	if string(content) != "user data" {
		t.Fatalf("user file content = %q, want %q", content, "user data")
	}
}

func TestGenerateLeavesContentsIntactWhenValidationFails(t *testing.T) {
	out, err := os.MkdirTemp("", "rhds-gen-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(out) }()

	files := map[string]string{
		"go.mod":   "module example.test\n",
		"keep.txt": "keep me",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(out, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Generate(testInputs(), out); err == nil {
		t.Fatal("Generate() accepted an output directory containing go.mod")
	}
	for name, want := range files {
		content, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("Generate() removed %s: %v", name, err)
		}
		if string(content) != want {
			t.Errorf("%s content = %q, want %q", name, content, want)
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

func TestParallelVersionErrorsAreDeterministic(t *testing.T) {
	items := []lock.PackageVersion{
		{Version: "3.0.0"},
		{Version: "1.0.0"},
		{Version: "2.0.0"},
	}
	for i := 0; i < 20; i++ {
		_, err := parallelVersions(items, 3, func(version lock.PackageVersion) (struct{}, error) {
			return struct{}{}, fmt.Errorf("failed %s", version.Version)
		})
		if err == nil {
			t.Fatal("parallelVersions() = nil error")
		}
		if got, want := err.Error(), "failed 1.0.0"; got != want {
			t.Fatalf("parallelVersions() error = %q, want %q", got, want)
		}
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
