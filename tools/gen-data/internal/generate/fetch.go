// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package generate

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/redhat-ux/mcp/tools/gen-data/internal/fetch"
	"github.com/redhat-ux/mcp/tools/gen-data/internal/lock"
)

const (
	elementsPackage = "elements"
	tokensPackage   = "tokens"
)

var docRoots = []string{
	"docs/foundations/",
	"docs/patterns/",
	"docs/accessibility/",
	"docs/get-started/",
	"docs/tokens/",
	"docs/theming/",
	"docs/support/",
	"docs/personalization/",
}

// Fetch downloads every pinned input. It is the only network path in
// generation; tests use Generate directly with fixture Inputs.
func Fetch(ctx context.Context, l *lock.Lock, client *fetch.Client) (*Inputs, error) {
	digest, err := l.Digest()
	if err != nil {
		return nil, err
	}
	inputs := &Inputs{
		LockDigest: digest,
		Elements:   make(map[string]ElementInput),
		Tokens:     make(map[string]TokenInput),
	}

	elementsPkg, ok := l.PackageByID(elementsPackage)
	if !ok {
		return nil, fmt.Errorf("lock has no %s package", elementsPackage)
	}
	tokensPkg, ok := l.PackageByID(tokensPackage)
	if !ok {
		return nil, fmt.Errorf("lock has no %s package", tokensPackage)
	}

	fetchedElements, err := parallelVersions(elementsPkg.Versions, 8, func(version lock.PackageVersion) (ElementInput, error) {
		return fetchElements(ctx, client, version)
	})
	if err != nil {
		return nil, err
	}
	inputs.Elements = fetchedElements

	fetchedTokens, err := parallelVersions(tokensPkg.Versions, 6, func(version lock.PackageVersion) (TokenInput, error) {
		return fetchTokens(ctx, client, version)
	})
	if err != nil {
		return nil, err
	}
	inputs.Tokens = fetchedTokens

	aiCache := make(map[string][]DocFile)
	for _, snapshot := range l.Docs.Snapshots {
		if snapshot.AIGuidelinesCommit != "" {
			if _, ok := aiCache[snapshot.AIGuidelinesCommit]; !ok {
				files, err := fetchAIGuidelines(ctx, client, l, snapshot.AIGuidelinesCommit)
				if err != nil {
					return nil, err
				}
				aiCache[snapshot.AIGuidelinesCommit] = files
			}
		}
		docsInput, err := fetchDocsSnapshot(ctx, client, l, snapshot, aiCache[snapshot.AIGuidelinesCommit])
		if err != nil {
			return nil, err
		}
		inputs.Docs = append(inputs.Docs, docsInput)
	}
	return inputs, nil
}

func fetchElements(ctx context.Context, client *fetch.Client, version lock.PackageVersion) (ElementInput, error) {
	data, err := client.Download(ctx, version.Tarball, version.Integrity)
	if err != nil {
		return ElementInput{}, fmt.Errorf("elements@%s: %w", version.Version, err)
	}
	entries, err := fetch.ExtractTarGz(bytes.NewReader(data), "package/", func(name string) bool {
		return name == "custom-elements.json" || name == "package.json"
	})
	if err != nil {
		return ElementInput{}, fmt.Errorf("elements@%s: %w", version.Version, err)
	}
	manifest, ok := entries["custom-elements.json"]
	if !ok {
		return ElementInput{}, fmt.Errorf("elements@%s tarball has no custom-elements.json", version.Version)
	}
	packageJSON, ok := entries["package.json"]
	if !ok {
		return ElementInput{}, fmt.Errorf("elements@%s tarball has no package.json", version.Version)
	}
	return ElementInput{Manifest: manifest, PackageJSON: packageJSON}, nil
}

func fetchTokens(ctx context.Context, client *fetch.Client, version lock.PackageVersion) (TokenInput, error) {
	data, err := client.Download(ctx, version.Tarball, version.Integrity)
	if err != nil {
		return TokenInput{}, fmt.Errorf("tokens@%s: %w", version.Version, err)
	}
	entries, err := fetch.ExtractTarGz(bytes.NewReader(data), "package/", func(name string) bool {
		return name == "json/rhds.tokens.json" || name == "css/default-theme.css"
	})
	if err != nil {
		return TokenInput{}, fmt.Errorf("tokens@%s: %w", version.Version, err)
	}
	tokensJSON, ok := entries["json/rhds.tokens.json"]
	if !ok {
		return TokenInput{}, fmt.Errorf("tokens@%s tarball has no json/rhds.tokens.json", version.Version)
	}
	themeCSS, hasThemeCSS := entries["css/default-theme.css"]
	return TokenInput{JSON: tokensJSON, ThemeCSS: themeCSS, HasThemeCSS: hasThemeCSS}, nil
}

func fetchDocsSnapshot(ctx context.Context, client *fetch.Client, l *lock.Lock, snapshot lock.DocSnapshot, aiFiles []DocFile) (DocsInput, error) {
	body, err := client.Open(ctx, client.DocsTarballURL(l.Docs.Repo, snapshot.Commit))
	if err != nil {
		return DocsInput{}, fmt.Errorf("docs %s: %w", snapshot.Tag, err)
	}
	defer func() { _ = body.Close() }()

	entries, err := fetch.ExtractTarGzTree(body, func(name string) bool {
		return includeDoc(name) || includePattern(name) || name == "CHANGELOG.md"
	})
	if err != nil {
		return DocsInput{}, fmt.Errorf("docs %s: %w", snapshot.Tag, err)
	}

	result := DocsInput{
		Tag:    snapshot.Tag,
		Commit: snapshot.Commit,
	}
	for name, content := range entries {
		switch {
		case name == "CHANGELOG.md":
			result.Changelog = content
		case includeDoc(name):
			result.Files = append(result.Files, DocFile{Path: name, Content: content})
		case includePattern(name):
			result.Patterns = append(result.Patterns, PatternFile{Path: name, Content: content})
		}
	}
	result.Files = append(result.Files, aiFiles...)
	return result, nil
}

func fetchAIGuidelines(ctx context.Context, client *fetch.Client, l *lock.Lock, commit string) ([]DocFile, error) {
	if l.Docs.AIGuidelines.Repo == "" {
		return nil, fmt.Errorf("lock has no ai-guidelines repo but %s pins a submodule commit", commit)
	}
	body, err := client.Open(ctx, client.AIGuidelinesTarballURL(l.Docs.AIGuidelines.Repo, commit))
	if err != nil {
		return nil, fmt.Errorf("ai-guidelines@%s: %w", commit, err)
	}
	defer func() { _ = body.Close() }()

	entries, err := fetch.ExtractTarGzTree(body, func(name string) bool {
		return (strings.HasPrefix(name, "content/") && strings.HasSuffix(name, ".md")) || name == "README.md"
	})
	if err != nil {
		return nil, fmt.Errorf("ai-guidelines@%s: %w", commit, err)
	}
	files := make([]DocFile, 0, len(entries))
	for name, content := range entries {
		logical := "docs/ai-guidelines/" + name
		files = append(files, DocFile{
			Path:    logical,
			Source:  l.Docs.AIGuidelines.Repo + "@" + commit + " " + name,
			Content: content,
		})
	}
	return files, nil
}

func includeDoc(name string) bool {
	if !strings.HasSuffix(name, ".md") {
		return false
	}
	if strings.HasPrefix(name, "elements/") && strings.Contains(name, "/docs/") {
		return true
	}
	for _, root := range docRoots {
		if strings.HasPrefix(name, root) {
			return true
		}
	}
	return false
}

func includePattern(name string) bool {
	if !strings.HasSuffix(name, ".html") {
		return false
	}
	if !strings.HasPrefix(name, "docs/patterns/") && !strings.HasPrefix(name, "docs/theming/") {
		return false
	}
	return strings.Contains(name, "/patterns/")
}

// parallelVersions runs fn concurrently with a worker limit and keys results
// by package version.
func parallelVersions[V any](
	items []lock.PackageVersion,
	workers int,
	fn func(lock.PackageVersion) (V, error),
) (map[string]V, error) {
	results := make(map[string]V, len(items))
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var (
		mu       sync.Mutex
		failures = make(map[string]error, len(items))
		wg       sync.WaitGroup
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				value, err := fn(items[index])
				if err != nil {
					mu.Lock()
					failures[items[index].Version] = err
					mu.Unlock()
					continue
				}
				mu.Lock()
				results[items[index].Version] = value
				mu.Unlock()
			}
		}()
	}
	for index := range items {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	if len(failures) > 0 {
		// Report the lowest failing version, not whichever worker lost the
		// race, so error output is deterministic.
		versions := sortedKeys(failures)
		return nil, failures[versions[0]]
	}
	return results, nil
}
