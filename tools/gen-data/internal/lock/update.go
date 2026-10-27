// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package lock

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

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

// DocsTags are the latest release tag per elements major, per docs/PLAN.md
// section 5.1.
var DocsTags = []string{"v1.4.5", "v2.1.2", "v3.2.0", "v4.2.2", "v5.0.0"}

// AIGuidelinesPath is the submodule path inside DocsRepo.
const AIGuidelinesPath = "docs/ai-guidelines"

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

	for _, tag := range DocsTags {
		commit, err := client.GitHubCommit(ctx, DocsRepo, tag)
		if err != nil {
			return nil, fmt.Errorf("resolving %s@%s: %w", DocsRepo, tag, err)
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
