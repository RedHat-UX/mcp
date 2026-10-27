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
func Generate(inputs *Inputs, outDir string) (*data.Index, error) {
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
	if absoluteOut == string(filepath.Separator) || filepath.Dir(absoluteOut) == absoluteOut {
		return nil, fmt.Errorf("generate: refusing to clear unsafe output directory %q", outDir)
	}
	if err := os.RemoveAll(outDir); err != nil {
		return nil, fmt.Errorf("clearing %s: %w", outDir, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", outDir, err)
	}
	// Keep the embed pattern valid even if the tree is later cleaned of
	// generated artifacts.
	if err := os.WriteFile(filepath.Join(outDir, ".gitkeep"), nil, 0o644); err != nil {
		return nil, err
	}

	index := &data.Index{
		SchemaVersion:  data.SchemaVersion,
		LockSHA256:     inputs.LockDigest,
		ElementsTokens: make(map[string]string),
	}
	tokensVersions := sortedKeys(inputs.Tokens)

	if err := generateElements(inputs, tokensVersions, outDir, index); err != nil {
		return nil, err
	}
	if err := generateTokens(inputs, tokensVersions, outDir, index); err != nil {
		return nil, err
	}
	if err := generateDocs(inputs.Docs, outDir, index); err != nil {
		return nil, err
	}

	sortArtifacts(index.Artifacts)
	encoded, err := marshalJSON(index)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, data.IndexPath), encoded, 0o644); err != nil {
		return nil, err
	}
	return index, nil
}

func generateElements(inputs *Inputs, tokensVersions []string, outDir string, index *data.Index) error {
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
			outDir,
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

func generateTokens(inputs *Inputs, versions []string, outDir string, index *data.Index) error {
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
			outDir,
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
			outDir,
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

func generateDocs(snapshots []DocsInput, outDir string, index *data.Index) error {
	sorted := append([]DocsInput(nil), snapshots...)
	sort.Slice(sorted, func(i, j int) bool {
		return compareVersions(sorted[i].Tag, sorted[j].Tag) < 0
	})

	var changelog DocsInput
	for _, snapshot := range sorted {
		if len(snapshot.Changelog) > 0 {
			changelog = snapshot
		}
		if err := writeDocsSnapshot(snapshot, outDir, index); err != nil {
			return err
		}
	}
	if len(changelog.Changelog) > 0 {
		source := "RedHat-UX/red-hat-design-system@" + changelog.Tag + " CHANGELOG.md"
		processed := normalize.ProcessMarkdown(changelog.Changelog, source)
		artifact, err := writeGzipArtifact(
			outDir,
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

func writeDocsSnapshot(snapshot DocsInput, outDir string, index *data.Index) error {
	if snapshot.Tag == "" {
		return errors.New("docs snapshot has no tag")
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
			return fmt.Errorf("docs snapshot %s has duplicate path %s", snapshot.Tag, cleaned)
		}
		seen[cleaned] = true
		source := file.Source
		if source == "" {
			source = sourceRepo + "@" + snapshot.Tag + " " + cleaned
		}
		processed := normalize.ProcessMarkdown(file.Content, source)
		kind := "doc:" + cleaned
		artifactPath := path.Join(PackageDocs, snapshot.Tag, cleaned+".gz")
		artifact, err := writeGzipArtifact(
			outDir, artifactPath, PackageDocs, snapshot.Tag, kind, processed.Content)
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
		outDir,
		path.Join(PackageDocs, snapshot.Tag, "index.json.gz"),
		PackageDocs, snapshot.Tag, KindDocs, manifest)
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
				Source: sourceRepo + "@" + snapshot.Tag + " " + cleaned,
				HTML:   content,
			})
		}
		sort.Slice(patterns, func(i, j int) bool { return patterns[i].Path < patterns[j].Path })
		encoded, err := marshalJSON(patterns)
		if err != nil {
			return err
		}
		artifact, err := writeGzipArtifact(
			outDir,
			path.Join(PackageDocs, snapshot.Tag, "patterns.json.gz"),
			PackageDocs, snapshot.Tag, KindPatterns, encoded)
		if err != nil {
			return err
		}
		index.Artifacts = append(index.Artifacts, artifact)
	}
	return nil
}

func writeGzipArtifact(outDir, artifactPath, pkg, version, kind string, payload []byte) (data.Artifact, error) {
	compressed, err := gzipBytes(payload)
	if err != nil {
		return data.Artifact{}, err
	}
	fullPath := filepath.Join(outDir, filepath.FromSlash(artifactPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return data.Artifact{}, err
	}
	if err := os.WriteFile(fullPath, compressed, 0o644); err != nil {
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
// artifact it names exists with the recorded size. It reads no artifact.
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
	if index.SchemaVersion != data.SchemaVersion || index.LockSHA256 != lockDigest {
		return false, nil
	}
	for _, artifact := range index.Artifacts {
		info, err := os.Stat(filepath.Join(outDir, filepath.FromSlash(artifact.Path)))
		if err != nil || info.IsDir() {
			return false, nil
		}
		if artifact.Bytes > 0 && info.Size() != artifact.Bytes {
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

func cleanRelative(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	cleaned := path.Clean(strings.TrimPrefix(value, "/"))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", fmt.Errorf("invalid relative path %q", value)
	}
	return cleaned, nil
}
