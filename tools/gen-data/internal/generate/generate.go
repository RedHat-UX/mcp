// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Package generate turns pinned inputs into the embedded data tree.
package generate

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bennypowers.dev/asimonim/parser"
	"bennypowers.dev/asimonim/resolver"
	"bennypowers.dev/asimonim/schema"
	"bennypowers.dev/asimonim/validator"
	"golang.org/x/mod/semver"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/tools/gen-data/internal/lock"
	"github.com/redhat-ux/mcp/tools/gen-data/internal/normalize"
)

// Package and artifact identifiers shared with internal/data and internal/tools.
const (
	PackageElements     = "elements"
	PackageTokens       = "tokens"
	PackageDocs         = "docs"
	PackageReleaseNotes = "release-notes"

	KindManifest  = "manifest"
	KindTokens    = "tokens"
	KindThemes    = "themes"
	KindDocs      = "docs"
	KindPatterns  = "patterns"
	KindChangelog = "changelog"
)

// GeneratorVersion changes whenever generation or normalization changes in a
// way that requires regenerating an existing dist tree.
const GeneratorVersion = 3

// Inputs is everything generation needs. Network fetching builds this value;
// tests construct it from fixtures.
type Inputs struct {
	LockDigest string
	Elements   map[string]ElementInput
	Tokens     map[string]TokenInput
	Docs       []DocsInput
}

// ElementInput is one elements release.
type ElementInput struct {
	Manifest    []byte
	PackageJSON []byte
}

// TokenInput is one tokens release.
type TokenInput struct {
	JSON        []byte
	ThemeCSS    []byte
	HasThemeCSS bool
}

// DocsInput is one documentation snapshot.
type DocsInput struct {
	Tag       string
	Commit    string
	Files     []DocFile
	Patterns  []PatternFile
	Changelog []byte
}

// DocFile is one processed markdown page.
type DocFile struct {
	Path string
	// Source overrides the default "repo@tag path" attribution. It is used
	// for the docs/ai-guidelines submodule so the pinned commit is recorded.
	Source string
	// Content is the raw markdown before frontmatter and image processing.
	Content []byte
}

// PatternFile is one pattern HTML example.
type PatternFile struct {
	Path    string
	Content []byte
}

type docRecord struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Source   string `json:"source"`
	Artifact string `json:"artifact"`
	Kind     string `json:"kind"`
}

type patternRecord struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	HTML   string `json:"html"`
}

// Generate writes the data tree and returns its index.
func Generate(inputs *Inputs, outDir string) (result *data.Index, err error) {
	if inputs == nil {
		return nil, errors.New("generate: no inputs")
	}
	if inputs.LockDigest == "" {
		return nil, errors.New("generate: inputs have no lock digest")
	}
	if len(inputs.Tokens) == 0 {
		return nil, errors.New("generate: no tokens versions")
	}
	absoluteOut, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	resolved, err := prepareOutputPath(absoluteOut)
	if err != nil {
		return nil, err
	}
	// Generate into a sibling staging directory and swap it in only after
	// validation and every write succeed. A failed run leaves the previous tree
	// untouched.
	staging, root, err := createStagingDir(resolved)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = root.Close()
		_ = os.RemoveAll(staging)
	}()
	index, err := writeOutputTree(inputs, root)
	if err != nil {
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, fmt.Errorf("closing staging directory: %w", err)
	}
	if err := commitStaging(staging, resolved); err != nil {
		return nil, err
	}
	return index, nil
}

// writeOutputTree writes a complete data tree into root.
func writeOutputTree(inputs *Inputs, root *os.Root) (*data.Index, error) {
	// Keep the embed pattern valid even if the tree is later cleaned of
	// generated artifacts.
	if err := root.WriteFile(".gitkeep", nil, 0o644); err != nil {
		return nil, err
	}

	index := &data.Index{
		SchemaVersion:    data.SchemaVersion,
		GeneratorVersion: GeneratorVersion,
		LockSHA256:       inputs.LockDigest,
		ElementsTokens:   make(map[string]string),
	}
	tokensVersions := sortedKeys(inputs.Tokens)

	if err := generateElements(inputs, tokensVersions, root, index); err != nil {
		return nil, err
	}
	if err := generateTokens(inputs, tokensVersions, root, index); err != nil {
		return nil, err
	}
	if err := generateDocs(inputs.Docs, root, index); err != nil {
		return nil, err
	}

	sortArtifacts(index.Artifacts)
	encoded, err := marshalJSON(index)
	if err != nil {
		return nil, err
	}
	if err := root.WriteFile(data.IndexPath, encoded, 0o644); err != nil {
		return nil, err
	}
	return index, nil
}

// prepareOutputPath validates outDir, requires positive gen-data provenance
// before it may be replaced, and returns the resolved output path. It does not
// modify the directory: Generate writes to a sibling staging directory that is
// swapped in only after every write succeeds.
func prepareOutputPath(absolute string) (string, error) {
	resolved, err := resolveOutputPath(absolute)
	if err != nil {
		return "", err
	}
	if err := validateOutputPath(absolute, resolved); err != nil {
		return "", err
	}
	before, beforeErr := os.Stat(resolved)
	if beforeErr != nil && !errors.Is(beforeErr, os.ErrNotExist) {
		return "", fmt.Errorf("generate: inspecting output %q: %w", resolved, beforeErr)
	}
	if beforeErr != nil {
		// Nothing exists yet; staging plus a rename creates the directory.
		return resolved, nil
	}
	// Any path component, including the final one, can change until OpenRoot.
	// For an existing directory, the inode captured here is compared with the
	// opened root, and confirmSameDirectory rechecks the current path and
	// refuses a symlink after opening. Every later read uses that descriptor.
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", absolute, err)
	}
	defer func() { _ = root.Close() }()
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(before, rootInfo) {
		return "", fmt.Errorf("generate: refusing output %q because it changed while opening", resolved)
	}
	if err := confirmSameDirectory(resolved, root); err != nil {
		return "", err
	}
	if err := validateOutputContents(root, absolute); err != nil {
		return "", err
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return "", fmt.Errorf("generate: cannot inspect output %q: %w", absolute, err)
	}
	if isEmptyOutput(entries) {
		return resolved, nil
	}
	if err := checkProvenance(root, entries, absolute); err != nil {
		return "", err
	}
	sources, err := findSourceFiles(root, entries)
	if err != nil {
		return "", fmt.Errorf("generate: scanning output %q: %w", absolute, err)
	}
	if len(sources) > 0 {
		return "", fmt.Errorf(
			"generate: refusing to replace %q because it contains %s",
			absolute, strings.Join(sources, ", "))
	}
	return resolved, nil
}

// createStagingDir creates a fresh sibling directory for a generation run.
// Being a sibling keeps the final rename on the same filesystem.
func createStagingDir(resolved string) (string, *os.Root, error) {
	parent := filepath.Dir(resolved)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", nil, fmt.Errorf("creating %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(resolved)+".stage-")
	if err != nil {
		return "", nil, fmt.Errorf("creating staging directory: %w", err)
	}
	root, err := os.OpenRoot(staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		return "", nil, fmt.Errorf("opening staging directory: %w", err)
	}
	return staging, root, nil
}

// commitStaging replaces resolved with the staged tree. The previous tree is
// moved aside first, so if the staged tree cannot be installed the previous one
// is restored.
func commitStaging(staging, resolved string) error {
	info, err := os.Lstat(resolved)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Rename(staging, resolved); err != nil {
			return fmt.Errorf("installing staged output: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("generate: inspecting output %q: %w", resolved, err)
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("generate: refusing output %q because it is a symlink", resolved)
	case !info.IsDir():
		return fmt.Errorf("generate: refusing output %q because it is not a directory", resolved)
	}
	// staging is unique, so staging+".old" cannot already exist.
	backup := staging + ".old"
	if err := os.Rename(resolved, backup); err != nil {
		return fmt.Errorf("moving previous output aside: %w", err)
	}
	if err := os.Rename(staging, resolved); err != nil {
		if restoreErr := os.Rename(backup, resolved); restoreErr != nil {
			return fmt.Errorf("installing staged output: %v (restoring previous output: %v)", err, restoreErr)
		}
		return fmt.Errorf("installing staged output: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("removing previous output: %w", err)
	}
	return nil
}

func confirmSameDirectory(resolved string, root *os.Root) error {
	info, err := os.Lstat(resolved)
	if err != nil {
		return fmt.Errorf("generate: inspecting output %q: %w", resolved, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("generate: refusing output %q because it is a symlink", resolved)
	}
	pathInfo, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("generate: inspecting output %q: %w", resolved, err)
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("generate: inspecting output %q: %w", resolved, err)
	}
	if !os.SameFile(pathInfo, rootInfo) {
		return fmt.Errorf("generate: refusing output %q because it changed while opening", resolved)
	}
	return nil
}

func isEmptyOutput(entries []fs.DirEntry) bool {
	return len(entries) == 0 ||
		(len(entries) == 1 && entries[0].Name() == ".gitkeep" && !entries[0].IsDir())
}

// checkProvenance accepts a non-empty directory only when index.json carries a
// gen-data index for this schema and every top-level entry is expected from
// that index.
func checkProvenance(root *os.Root, entries []fs.DirEntry, absolute string) error {
	encoded, err := root.ReadFile(data.IndexPath)
	if err != nil {
		return fmt.Errorf(
			"generate: refusing to clear %q: not a gen-data output directory (missing or unreadable %s; found: %s)",
			absolute, data.IndexPath, strings.Join(entryNames(entries), ", "))
	}
	var index data.Index
	if err := json.Unmarshal(encoded, &index); err != nil ||
		index.SchemaVersion != data.SchemaVersion ||
		index.GeneratorVersion <= 0 ||
		!isLowerHexDigest(index.LockSHA256) {
		return fmt.Errorf(
			"generate: refusing to clear %q: not a gen-data output directory (invalid %s; found: %s)",
			absolute, data.IndexPath, strings.Join(entryNames(entries), ", "))
	}
	expected := map[string]bool{
		".gitkeep":     true,
		data.IndexPath: true,
	}
	for _, artifact := range index.Artifacts {
		if top, _, _ := strings.Cut(artifact.Path, "/"); top != "" {
			expected[top] = true
		}
	}
	var unexpected []string
	for _, entry := range entries {
		if !expected[entry.Name()] {
			unexpected = append(unexpected, entry.Name())
		}
	}
	if len(unexpected) > 0 {
		return fmt.Errorf(
			"generate: refusing to clear %q: not a gen-data output directory (unexpected entries: %s)",
			absolute, strings.Join(unexpected, ", "))
	}
	return nil
}

func entryNames(entries []fs.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

// findSourceFiles refuses a tree that contains a checkout or lock at any
// depth under an entry that would be deleted.
func findSourceFiles(root *os.Root, entries []fs.DirEntry) ([]string, error) {
	isSource := func(name string) bool {
		base := filepath.Base(name)
		return base == "go.mod" || base == "sources.lock.json" || base == ".git"
	}
	var found []string
	for _, entry := range entries {
		if !entry.IsDir() {
			if isSource(entry.Name()) {
				found = append(found, entry.Name())
			}
			continue
		}
		err := fs.WalkDir(root.FS(), entry.Name(), func(name string, item fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if isSource(name) {
				found = append(found, filepath.ToSlash(name))
				if item.IsDir() {
					return fs.SkipDir
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}

// resolveOutputPath refuses an output path that is itself a symlink, then
// resolves parent symlinks once. Callers must use the returned path for both
// validation and directory access.
func resolveOutputPath(absolute string) (string, error) {
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("generate: refusing output %q because it is a symlink", absolute)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	return resolveExisting(absolute)
}

// validateOutputDir is the standalone validator used by tests. It performs the
// path checks and, when the directory exists, the contents checks through an
// opened root.
func validateOutputDir(absolute string) error {
	resolved, err := resolveOutputPath(absolute)
	if err != nil {
		return err
	}
	if err := validateOutputPath(absolute, resolved); err != nil {
		return err
	}
	root, err := os.OpenRoot(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := confirmSameDirectory(resolved, root); err != nil {
		return err
	}
	return validateOutputContents(root, absolute)
}

// validateOutputPath refuses protected paths. It uses the already resolved
// path so later directory access refers to the same location.
func validateOutputPath(absolute, resolved string) error {
	if isFilesystemRoot(absolute) || isFilesystemRoot(resolved) {
		return fmt.Errorf("generate: refusing to clear filesystem root")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwd, err = resolveExisting(cwd)
	if err != nil {
		return err
	}
	temp, err := resolveExisting(os.TempDir())
	if err != nil {
		return err
	}
	home := ""
	if value, err := os.UserHomeDir(); err == nil {
		home, _ = resolveExisting(value)
	}
	for _, protected := range []struct {
		name string
		path string
	}{
		{"temporary root", temp},
		{"home directory", home},
		{"current directory", cwd},
	} {
		if protected.path != "" && resolved == protected.path {
			return fmt.Errorf("generate: refusing to clear protected directory %q (%s)", protected.path, protected.name)
		}
	}
	roots := repositoryRoots(resolved, cwd)
	for _, root := range roots {
		if isAncestorOrSelf(resolved, root) {
			return fmt.Errorf("generate: refusing to clear %q because it contains the repository", absolute)
		}
	}
	if !isTempPath(resolved, temp) && !isInternalDataDist(resolved, findRepositoryRoot(cwd)) {
		return fmt.Errorf(
			"generate: output %q is not under internal/data/dist or a temporary directory",
			absolute)
	}
	return nil
}

// validateOutputContents checks direct children through an opened root so the
// check cannot race with a path replacement.
func validateOutputContents(root *os.Root, absolute string) error {
	for _, name := range []string{"go.mod", "sources.lock.json"} {
		if _, err := root.Stat(name); err == nil {
			return fmt.Errorf(
				"generate: refusing to clear %q because it contains %s", absolute, name)
		}
	}
	return nil
}

// repositoryRoots finds repository roots by walking up from the output and
// from the current directory. Both matter when gen-data runs outside the
// module but targets a directory inside one.
func repositoryRoots(output, cwd string) []string {
	var roots []string
	seen := map[string]bool{}
	for _, start := range []string{output, cwd} {
		root := findRepositoryRoot(start)
		if root != "" && !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	return roots
}

// findRepositoryRoot walks up from start looking for a repository marker. The
// internal/data/dist exception is anchored to the root discovered from cwd,
// so another checkout's dist directory is not accepted.
func findRepositoryRoot(start string) string {
	dir := filepath.Clean(start)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		for _, marker := range []string{".git", "go.mod"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				if resolved, err := resolveExisting(dir); err == nil {
					return resolved
				}
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isFilesystemRoot(path string) bool {
	return filepath.Dir(path) == path
}

func isAncestorOrSelf(ancestor, target string) bool {
	ancestor = filepath.Clean(ancestor)
	target = filepath.Clean(target)
	return ancestor == target || strings.HasPrefix(target, ancestor+string(filepath.Separator))
}

func isLowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func isTempPath(resolved, temp string) bool {
	return strings.HasPrefix(resolved, filepath.Clean(temp)+string(filepath.Separator))
}

func isInternalDataDist(resolved, repoRoot string) bool {
	if repoRoot == "" {
		return false
	}
	return resolved == filepath.Join(repoRoot, "internal", "data", "dist")
}

// resolveExisting resolves symlinks for the nearest existing ancestor and
// reattaches the non-existent suffix.
func resolveExisting(path string) (string, error) {
	current := path
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func generateElements(inputs *Inputs, tokensVersions []string, root *os.Root, index *data.Index) error {
	versions := sortedKeys(inputs.Elements)
	for _, version := range versions {
		input := inputs.Elements[version]
		if !json.Valid(input.Manifest) {
			return fmt.Errorf("elements@%s manifest is not valid JSON", version)
		}

		var packageJSON struct {
			Dependencies map[string]string `json:"dependencies"`
		}
		if err := json.Unmarshal(input.PackageJSON, &packageJSON); err != nil {
			return fmt.Errorf("elements@%s package.json: %w", version, err)
		}
		rangeSpec := packageJSON.Dependencies["@rhds/tokens"]
		if rangeSpec == "" {
			return fmt.Errorf("elements@%s does not depend on @rhds/tokens", version)
		}
		tokensVersion, ok := lock.HighestSatisfying(rangeSpec, tokensVersions)
		if !ok {
			return fmt.Errorf(
				"elements@%s requires @rhds/tokens %s, but no bundled tokens version satisfies it",
				version, rangeSpec)
		}
		index.ElementsTokens[version] = tokensVersion

		artifact, err := writeGzipArtifact(
			root,
			path.Join(PackageElements, version, "custom-elements.json.gz"),
			PackageElements, version, KindManifest, input.Manifest,
		)
		if err != nil {
			return err
		}
		index.Artifacts = append(index.Artifacts, artifact)
	}
	return nil
}

func generateTokens(inputs *Inputs, versions []string, root *os.Root, index *data.Index) error {
	for _, version := range versions {
		input := inputs.Tokens[version]
		result, err := normalize.NormalizeTokens(input.JSON)
		if err != nil {
			return fmt.Errorf("tokens@%s: %w", version, err)
		}
		if problems := validator.ValidateConsistency(result.Document, schema.Draft); len(problems) > 0 {
			return fmt.Errorf("tokens@%s: validator rejected normalized DTCG: %v", version, problems)
		}
		tokens, err := parser.NewJSONParser().Parse(result.Document, parser.Options{
			Prefix:        "rh",
			SkipPositions: true,
			SchemaVersion: schema.Draft,
		})
		if err != nil {
			return fmt.Errorf("tokens@%s: parsing normalized DTCG: %w", version, err)
		}
		if len(tokens) == 0 {
			return fmt.Errorf("tokens@%s: normalized DTCG produced no tokens", version)
		}
		if err := resolver.ResolveAliases(tokens, schema.Draft); err != nil {
			return fmt.Errorf("tokens@%s: resolving aliases: %w", version, err)
		}

		artifact, err := writeGzipArtifact(
			root,
			path.Join(PackageTokens, version, "rhds.tokens.json.gz"),
			PackageTokens, version, KindTokens, result.Document,
		)
		if err != nil {
			return err
		}
		index.Artifacts = append(index.Artifacts, artifact)

		themes := result.Themes
		if input.HasThemeCSS {
			cssThemes, err := normalize.ExtractThemeMap(input.ThemeCSS)
			if err != nil {
				return fmt.Errorf("tokens@%s: extracting theme map: %w", version, err)
			}
			// The generated CSS is authoritative for theme values. Upstream
			// token JSON has drifted from it for rh-surface in at least one
			// release, so do not treat a disagreement as fatal.
			themes = cssThemes
		} else if len(themes["light"]) == 0 {
			// tokens 1.x has no light/dark composites.
			continue
		}
		themesJSON, err := marshalJSON(themes)
		if err != nil {
			return err
		}
		artifact, err = writeGzipArtifact(
			root,
			path.Join(PackageTokens, version, "themes.json.gz"),
			PackageTokens, version, KindThemes, themesJSON,
		)
		if err != nil {
			return err
		}
		index.Artifacts = append(index.Artifacts, artifact)
	}
	return nil
}

func generateDocs(snapshots []DocsInput, root *os.Root, index *data.Index) error {
	sorted := append([]DocsInput(nil), snapshots...)
	sort.Slice(sorted, func(i, j int) bool {
		return compareVersions(sorted[i].Tag, sorted[j].Tag) < 0
	})

	var changelog DocsInput
	for _, snapshot := range sorted {
		if len(snapshot.Changelog) > 0 {
			changelog = snapshot
		}
		if err := writeDocsSnapshot(snapshot, root, index); err != nil {
			return err
		}
	}
	if len(changelog.Changelog) > 0 {
		source := "RedHat-UX/red-hat-design-system@" + changelog.Tag + " CHANGELOG.md"
		processed := normalize.ProcessMarkdown(changelog.Changelog, source)
		artifact, err := writeGzipArtifact(
			root,
			path.Join(PackageReleaseNotes, "latest", "CHANGELOG.md.gz"),
			PackageReleaseNotes, "latest", KindChangelog, processed.Content,
		)
		if err != nil {
			return err
		}
		index.Artifacts = append(index.Artifacts, artifact)
	}
	return nil
}

func writeDocsSnapshot(snapshot DocsInput, root *os.Root, index *data.Index) error {
	tag, err := cleanDocTag(snapshot.Tag)
	if err != nil {
		return err
	}
	sourceRepo := "RedHat-UX/red-hat-design-system"
	records := make([]docRecord, 0, len(snapshot.Files))
	seen := make(map[string]bool, len(snapshot.Files))
	for _, file := range snapshot.Files {
		cleaned, err := cleanRelative(file.Path)
		if err != nil {
			return err
		}
		if seen[cleaned] {
			return fmt.Errorf("docs snapshot %s has duplicate path %s", tag, cleaned)
		}
		seen[cleaned] = true
		source := file.Source
		if source == "" {
			source = sourceRepo + "@" + tag + " " + cleaned
		}
		processed := normalize.ProcessMarkdown(file.Content, source)
		kind := "doc:" + cleaned
		artifactPath := path.Join(PackageDocs, tag, cleaned+".gz")
		artifact, err := writeGzipArtifact(
			root, artifactPath, PackageDocs, tag, kind, processed.Content)
		if err != nil {
			return err
		}
		index.Artifacts = append(index.Artifacts, artifact)
		records = append(records, docRecord{
			Path:     cleaned,
			Title:    processed.Title,
			Source:   source,
			Artifact: artifactPath,
			Kind:     kind,
		})
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	manifest, err := marshalJSON(records)
	if err != nil {
		return err
	}
	artifact, err := writeGzipArtifact(
		root,
		path.Join(PackageDocs, tag, "index.json.gz"),
		PackageDocs, tag, KindDocs, manifest)
	if err != nil {
		return err
	}
	index.Artifacts = append(index.Artifacts, artifact)

	if len(snapshot.Patterns) > 0 {
		patterns := make([]patternRecord, 0, len(snapshot.Patterns))
		for _, pattern := range snapshot.Patterns {
			cleaned, err := cleanRelative(pattern.Path)
			if err != nil {
				return err
			}
			content := strings.ReplaceAll(string(pattern.Content), "\r\n", "\n")
			patterns = append(patterns, patternRecord{
				Path:   cleaned,
				Source: sourceRepo + "@" + tag + " " + cleaned,
				HTML:   content,
			})
		}
		sort.Slice(patterns, func(i, j int) bool { return patterns[i].Path < patterns[j].Path })
		encoded, err := marshalJSON(patterns)
		if err != nil {
			return err
		}
		artifact, err := writeGzipArtifact(
			root,
			path.Join(PackageDocs, tag, "patterns.json.gz"),
			PackageDocs, tag, KindPatterns, encoded)
		if err != nil {
			return err
		}
		index.Artifacts = append(index.Artifacts, artifact)
	}
	return nil
}

func writeGzipArtifact(root *os.Root, artifactPath, pkg, version, kind string, payload []byte) (data.Artifact, error) {
	compressed, err := gzipBytes(payload)
	if err != nil {
		return data.Artifact{}, err
	}
	relative := filepath.FromSlash(artifactPath)
	if err := root.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
		return data.Artifact{}, err
	}
	if err := root.WriteFile(relative, compressed, 0o644); err != nil {
		return data.Artifact{}, err
	}
	sum := sha256.Sum256(compressed)
	return data.Artifact{
		Package: pkg,
		Version: version,
		Kind:    kind,
		Path:    artifactPath,
		SHA256:  hex.EncodeToString(sum[:]),
		Bytes:   int64(len(compressed)),
	}, nil
}

// marshalJSON writes stable, human-readable JSON without escaping HTML. HTML
// examples and documentation snippets should stay readable in the archive.
func marshalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// gzipBytes is deterministic: fixed header fields, no timestamps.
func gzipBytes(payload []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	writer.ModTime = time.Time{}
	writer.OS = 255
	writer.Name = ""
	writer.Comment = ""
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// IsUpToDate reports whether outDir has an index for the lock digest and every
// artifact it names is readable and matches its recorded SHA-256. Content is
// read, not just sized, so a same-size change still forces regeneration.
func IsUpToDate(outDir, lockDigest string) (bool, error) {
	if lockDigest == "" {
		return false, nil
	}
	index, err := readIndex(filepath.Join(outDir, data.IndexPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, nil
	}
	if index.SchemaVersion != data.SchemaVersion || index.LockSHA256 != lockDigest ||
		index.GeneratorVersion != GeneratorVersion {
		return false, nil
	}
	for _, artifact := range index.Artifacts {
		if !isLowerHexDigest(artifact.SHA256) {
			return false, nil
		}
		content, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(artifact.Path)))
		if err != nil {
			return false, nil
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != artifact.SHA256 {
			return false, nil
		}
	}
	return true, nil
}

func readIndex(indexPath string) (*data.Index, error) {
	encoded, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	var index data.Index
	if err := json.Unmarshal(encoded, &index); err != nil {
		return nil, err
	}
	return &index, nil
}

func sortArtifacts(artifacts []data.Artifact) {
	sort.Slice(artifacts, func(i, j int) bool {
		a, b := artifacts[i], artifacts[j]
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		if comparison := compareVersions(a.Version, b.Version); comparison != 0 {
			return comparison < 0
		}
		return a.Kind < b.Kind
	})
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return compareVersions(keys[i], keys[j]) < 0
	})
	return keys
}

func compareVersions(a, b string) int {
	ta := "v" + strings.TrimPrefix(a, "v")
	tb := "v" + strings.TrimPrefix(b, "v")
	if semver.IsValid(ta) && semver.IsValid(tb) {
		return semver.Compare(ta, tb)
	}
	return strings.Compare(a, b)
}

func cleanDocTag(tag string) (string, error) {
	if !lock.ValidDocTag(tag) {
		return "", fmt.Errorf("invalid docs tag %q", tag)
	}
	cleaned, err := cleanRelative(tag)
	if err != nil {
		return "", err
	}
	if cleaned != tag {
		return "", fmt.Errorf("invalid docs tag %q", tag)
	}
	return cleaned, nil
}

func cleanRelative(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	cleaned := path.Clean(strings.TrimPrefix(value, "/"))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", fmt.Errorf("invalid relative path %q", value)
	}
	return cleaned, nil
}
