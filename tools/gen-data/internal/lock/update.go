// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package lock

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/redhat-ux/mcp/tools/gen-data/internal/fetch"
)

// packageSpecs are the npm packages the server embeds. Update resolves every
// stable release currently published.
var packageSpecs = []struct {
	Name string
	ID   string
}{
	{Name: "@rhds/elements", ID: "elements"},
	{Name: "@rhds/tokens", ID: "tokens"},
}

// DocsRepo is the source of documentation snapshots.
const DocsRepo = "RedHat-UX/red-hat-design-system"

// AIGuidelinesPath is the submodule path inside DocsRepo.
const AIGuidelinesPath = "docs/ai-guidelines"

// DocsTagsFromElements returns the latest release tag for each elements major
// in the given stable version list. Deriving the tags keeps a future elements
// major from being pinned in the lock without a docs snapshot.
func DocsTagsFromElements(versions []string) []string {
	latest := make(map[string]string)
	for _, version := range StableVersions(versions) {
		tag := "v" + strings.TrimPrefix(version, "v")
		major := semver.Major(tag)
		if major == "" {
			continue
		}
		latest[major] = tag
	}
	tags := make([]string, 0, len(latest))
	for _, tag := range latest {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool {
		return compareVersions(tags[i], tags[j]) < 0
	})
	return tags
}

// Update resolves the current published inputs and returns a fresh lock.
// It is the only function that writes new pins; `make data` never calls it.
func Update(ctx context.Context, client *fetch.Client) (*Lock, error) {
	result := &Lock{
		SchemaVersion: SchemaVersion,
		Docs: Docs{
			Repo: DocsRepo,
		},
	}

	for _, spec := range packageSpecs {
		metadata, err := client.RegistryMetadata(ctx, spec.Name)
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", spec.Name, err)
		}
		versions := make([]string, 0, len(metadata.Versions))
		for version := range metadata.Versions {
			versions = append(versions, version)
		}
		versions = StableVersions(versions)
		if len(versions) == 0 {
			return nil, fmt.Errorf("%s has no stable versions", spec.Name)
		}
		pinned := make([]PackageVersion, 0, len(versions))
		for _, version := range versions {
			dist := metadata.Versions[version].Dist
			if dist.Integrity == "" || dist.Tarball == "" {
				return nil, fmt.Errorf("%s@%s is missing dist integrity or tarball", spec.Name, version)
			}
			pinned = append(pinned, PackageVersion{
				Version:   version,
				Integrity: dist.Integrity,
				Tarball:   dist.Tarball,
				Shasum:    dist.Shasum,
			})
		}
		result.Packages = append(result.Packages, Package{
			Name:     spec.Name,
			ID:       spec.ID,
			Versions: pinned,
		})
	}

	elements, ok := result.PackageByID("elements")
	if !ok {
		return nil, fmt.Errorf("lock has no elements package")
	}
	elementVersions := make([]string, 0, len(elements.Versions))
	for _, version := range elements.Versions {
		elementVersions = append(elementVersions, version.Version)
	}
	docsTags := DocsTagsFromElements(elementVersions)
	if len(docsTags) == 0 {
		return nil, fmt.Errorf("elements package has no stable versions to derive docs tags from")
	}

	for _, tag := range docsTags {
		commit, err := client.GitHubCommit(ctx, DocsRepo, tag)
		if err != nil {
			return nil, fmt.Errorf("resolving docs tag %s for an elements major: %w", tag, err)
		}
		submodule, err := client.GitHubSubmoduleCommit(ctx, DocsRepo, commit, AIGuidelinesPath)
		if err != nil {
			return nil, fmt.Errorf("resolving %s submodule at %s: %w", AIGuidelinesPath, tag, err)
		}
		result.Docs.Snapshots = append(result.Docs.Snapshots, DocSnapshot{
			Tag:                tag,
			Commit:             commit,
			AIGuidelinesCommit: submodule,
		})
	}

	if err := resolveAIGuidelinesRepo(ctx, client, result); err != nil {
		return nil, err
	}
	sort.Slice(result.Docs.Snapshots, func(i, j int) bool {
		return compareVersions(result.Docs.Snapshots[i].Tag, result.Docs.Snapshots[j].Tag) < 0
	})
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

func resolveAIGuidelinesRepo(ctx context.Context, client *fetch.Client, result *Lock) error {
	for _, snapshot := range result.Docs.Snapshots {
		if snapshot.AIGuidelinesCommit == "" {
			continue
		}
		body, err := client.Open(ctx, client.GitModulesURL(DocsRepo, snapshot.Commit))
		if err != nil {
			return fmt.Errorf("reading .gitmodules at %s: %w", snapshot.Tag, err)
		}
		content, readErr := io.ReadAll(body)
		_ = body.Close()
		if readErr != nil {
			return readErr
		}
		modules := fetch.ParseGitModules(string(content))
		url := modules[AIGuidelinesPath]
		if url == "" {
			continue
		}
		result.Docs.AIGuidelines = AIGuidelines{
			Repo: repoSlug(url),
			URL:  url,
		}
		return nil
	}
	return nil
}

func repoSlug(url string) string {
	slug := strings.TrimSuffix(url, ".git")
	slug = strings.TrimPrefix(slug, "https://github.com/")
	slug = strings.TrimPrefix(slug, "http://github.com/")
	slug = strings.TrimPrefix(slug, "git@github.com:")
	return slug
}
