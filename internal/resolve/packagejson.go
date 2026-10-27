// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxWorkspacePackages bounds workspace manifest discovery.
const maxWorkspacePackages = 200

type packageJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	Overrides            any               `json:"overrides"`
	Resolutions          any               `json:"resolutions"`
	Workspaces           any               `json:"workspaces"`
	Pnpm                 struct {
		Overrides any `json:"overrides"`
		Catalog   any `json:"catalog"`
		Catalogs  any `json:"catalogs"`
	} `json:"pnpm"`
}

// collectPackageJSON reads the root package.json, its workspace manifests,
// and pnpm catalog definitions.
func collectPackageJSON(
	root string,
	reader *workspaceReader,
	set *candidateSet,
	warnings *[]string,
) []string {
	var manifest packageJSON
	payload, err := reader.read("package.json")
	switch {
	case err == nil:
		if unmarshalErr := json.Unmarshal(payload, &manifest); unmarshalErr != nil {
			*warnings = append(*warnings, fmt.Sprintf("could not parse package.json: %v", unmarshalErr))
		} else {
			addDependencyMaps(manifest, "package.json", set, warnings)
			addOverrideCandidates(manifest.Overrides, "package.json", set)
			addOverrideCandidates(manifest.Resolutions, "package.json", set)
			addOverrideCandidates(manifest.Pnpm.Overrides, "package.json", set)
			addCatalogCandidates(manifest.Pnpm.Catalog, "package.json", set)
			addCatalogCandidates(manifest.Pnpm.Catalogs, "package.json", set)
		}
	case !errors.Is(err, fs.ErrNotExist):
		*warnings = append(*warnings, fmt.Sprintf("could not read package.json: %v", err))
	}

	patterns := workspacePatterns(manifest.Workspaces)
	patterns = append(patterns, collectPNPMWorkspace(root, reader, set, warnings)...)

	directories := workspaceDirectories(root, patterns, warnings)
	for _, directory := range directories {
		relative := filepath.ToSlash(filepath.Join(directory, "package.json"))
		workspacePayload, err := reader.read(relative)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				*warnings = append(*warnings, fmt.Sprintf("could not read %s: %v", relative, err))
			}
			continue
		}
		var workspace packageJSON
		if err := json.Unmarshal(workspacePayload, &workspace); err != nil {
			*warnings = append(*warnings, fmt.Sprintf("could not parse %s: %v", relative, err))
			continue
		}
		addDependencyMaps(workspace, relative, set, warnings)
		addOverrideCandidates(workspace.Overrides, relative, set)
		addOverrideCandidates(workspace.Resolutions, relative, set)
	}
	return directories
}

func addDependencyMaps(manifest packageJSON, source string, set *candidateSet, warnings *[]string) {
	for _, dependencies := range []map[string]string{
		manifest.Dependencies,
		manifest.DevDependencies,
		manifest.OptionalDependencies,
	} {
		for name, spec := range dependencies {
			if name != "@rhds/elements" {
				continue
			}
			addPackageSpec(spec, source, set, warnings)
		}
	}
}

func addPackageSpec(spec, source string, set *candidateSet, warnings *[]string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return
	}
	// Catalogs contribute their own candidates, so an indirection here is not
	// itself evidence of a version.
	if strings.HasPrefix(spec, "catalog:") {
		return
	}
	spec = strings.TrimPrefix(spec, "workspace:")
	version, parsed := exactVersion(spec)
	if !parsed.OK {
		*warnings = append(*warnings,
			fmt.Sprintf("could not parse an @rhds/elements version from %q in %s", spec, source))
		return
	}
	set.add(version, spec, source, PrecedencePackageJSON)
}

func addOverrideCandidates(value any, source string, set *candidateSet) {
	addOverrideCandidatesAt(value, source, set, 0)
}

func addOverrideCandidatesAt(value any, source string, set *candidateSet, depth int) {
	if depth > maxDepth {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "@rhds/elements" {
				if spec, ok := item.(string); ok {
					version, parsed := exactVersion(spec)
					if parsed.OK {
						set.add(version, spec, source, PrecedencePackageJSON)
					}
				}
			}
			addOverrideCandidatesAt(item, source, set, depth+1)
		}
	case map[any]any:
		for key, item := range typed {
			if text, ok := key.(string); ok && text == "@rhds/elements" {
				if spec, ok := item.(string); ok {
					version, parsed := exactVersion(spec)
					if parsed.OK {
						set.add(version, spec, source, PrecedencePackageJSON)
					}
				}
			}
			addOverrideCandidatesAt(item, source, set, depth+1)
		}
	}
}

// addCatalogCandidates walks pnpm catalog structures, which may be a single
// dependency map or a map of named catalogs.
func addCatalogCandidates(value any, source string, set *candidateSet) {
	addOverrideCandidates(value, source, set)
}

func workspaceDirectories(root string, patterns []string, warnings *[]string) []string {
	if len(patterns) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var directories []string
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "!") {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("invalid workspace pattern %q: %v", pattern, err))
			continue
		}
		sort.Strings(matches)
		for _, match := range matches {
			if !withinRoot(root, match) {
				continue
			}
			info, err := os.Lstat(match)
			if err != nil {
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			directory := match
			if info.Mode().IsRegular() && filepath.Base(match) == "package.json" {
				directory = filepath.Dir(match)
			} else if !info.IsDir() {
				continue
			}
			relative, err := filepath.Rel(root, directory)
			if err != nil {
				continue
			}
			relative = filepath.ToSlash(relative)
			if relative == "." || seen[relative] {
				continue
			}
			seen[relative] = true
			directories = append(directories, relative)
			if len(directories) >= maxWorkspacePackages {
				*warnings = append(*warnings,
					fmt.Sprintf("workspace discovery stopped after %d packages", maxWorkspacePackages))
				return directories
			}
		}
	}
	return directories
}

func workspacePatterns(value any) []string {
	switch typed := value.(type) {
	case []any:
		patterns := make([]string, 0, len(typed))
		for _, item := range typed {
			if pattern, ok := item.(string); ok {
				patterns = append(patterns, pattern)
			}
		}
		return patterns
	case map[string]any:
		if packages, ok := typed["packages"]; ok {
			return workspacePatterns(packages)
		}
	case map[any]any:
		if packages, ok := typed["packages"]; ok {
			return workspacePatterns(packages)
		}
	}
	return nil
}

// collectPNPMWorkspace reads pnpm workspace packages and catalog definitions.
// pnpm keeps both in pnpm-workspace.yaml rather than package.json.
func collectPNPMWorkspace(
	root string,
	reader *workspaceReader,
	set *candidateSet,
	warnings *[]string,
) []string {
	payload, err := reader.read("pnpm-workspace.yaml")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			*warnings = append(*warnings, fmt.Sprintf("could not read pnpm-workspace.yaml: %v", err))
		}
		return nil
	}
	var document any
	if err := yaml.Unmarshal(payload, &document); err != nil {
		*warnings = append(*warnings, fmt.Sprintf("could not parse pnpm-workspace.yaml: %v", err))
		return nil
	}
	values := stringMap(document)
	if values == nil {
		return nil
	}
	addOverrideCandidates(values["catalog"], "pnpm-workspace.yaml", set)
	addOverrideCandidates(values["catalogs"], "pnpm-workspace.yaml", set)
	return workspacePatterns(values["packages"])
}

func stringMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			if text, ok := key.(string); ok {
				converted[text] = item
			}
		}
		return converted
	default:
		return nil
	}
}

// collectInstalledPackages finds installed @rhds/elements package.json files
// in hoisted, pnpm, and workspace locations.
func collectInstalledPackages(
	root string,
	reader *workspaceReader,
	workspaceDirectories []string,
	set *candidateSet,
	warnings *[]string,
) {
	candidates := []string{"node_modules/@rhds/elements/package.json"}
	for _, directory := range workspaceDirectories {
		candidates = append(candidates,
			filepath.ToSlash(filepath.Join(directory, "node_modules/@rhds/elements/package.json")))
	}
	candidates = append(candidates, pnpmStorePackages(root, warnings)...)

	for _, relative := range candidates {
		payload, err := reader.read(relative)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errTooLarge) {
				*warnings = append(*warnings, fmt.Sprintf("could not read %s: %v", relative, err))
			}
			continue
		}
		var manifest packageJSON
		if err := json.Unmarshal(payload, &manifest); err != nil {
			*warnings = append(*warnings,
				fmt.Sprintf("could not parse %s: %v", relative, err))
			continue
		}
		if manifest.Name != "" && manifest.Name != "@rhds/elements" {
			continue
		}
		version, parsed := exactVersion(manifest.Version)
		if !parsed.OK {
			continue
		}
		set.add(version, manifest.Version, relative, PrecedenceInstalled)
	}
}

// pnpmStorePackages lists the @rhds/elements package manifests inside a pnpm
// content-addressed store. The directory names embed the resolved version.
func pnpmStorePackages(root string, warnings *[]string) []string {
	storeDirectory := filepath.Join(root, "node_modules", ".pnpm")
	entries, err := os.ReadDir(storeDirectory)
	if err != nil {
		return nil
	}
	var packages []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "@rhds+elements@") {
			continue
		}
		packages = append(packages, filepath.ToSlash(filepath.Join(
			"node_modules", ".pnpm", entry.Name(),
			"node_modules/@rhds/elements/package.json")))
		if len(packages) >= maxWorkspacePackages {
			*warnings = append(*warnings, "pnpm store scan stopped at the package limit")
			break
		}
	}
	sort.Strings(packages)
	return packages
}
