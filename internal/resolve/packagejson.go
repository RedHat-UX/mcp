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

// catalogReference records a dependency that points at a pnpm catalog. Only
// referenced catalogs contribute candidates.
type catalogReference struct {
	Name   string
	Source string
}

type catalogDefinition struct {
	Source string
	Values map[string]string
}

type catalogDefinitions struct {
	Defaults []catalogDefinition
	Named    map[string][]catalogDefinition
}

func newCatalogDefinitions() *catalogDefinitions {
	return &catalogDefinitions{Named: make(map[string][]catalogDefinition)}
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
	catalogRefs := make([]catalogReference, 0)
	definitions := newCatalogDefinitions()

	payload, err := reader.read("package.json")
	switch {
	case err == nil:
		if unmarshalErr := json.Unmarshal(payload, &manifest); unmarshalErr != nil {
			*warnings = append(*warnings, fmt.Sprintf("could not parse package.json: %v", unmarshalErr))
		} else {
			addDependencyMaps(manifest, "package.json", set, &catalogRefs, warnings)
			addOverrideCandidates(manifest.Overrides, "package.json", set, warnings)
			addOverrideCandidates(manifest.Resolutions, "package.json", set, warnings)
			addOverrideCandidates(manifest.Pnpm.Overrides, "package.json", set, warnings)
			addCatalogDefinitions(manifest.Pnpm.Catalog, "package.json", true, definitions)
			addCatalogDefinitions(manifest.Pnpm.Catalogs, "package.json", false, definitions)
		}
	case !errors.Is(err, fs.ErrNotExist):
		*warnings = append(*warnings, fmt.Sprintf("could not read package.json: %v", err))
	}

	patterns := workspacePatterns(manifest.Workspaces)
	pnpmPatterns, pnpmDefinitions := collectPNPMWorkspace(reader, warnings)
	patterns = append(patterns, pnpmPatterns...)
	definitions.Defaults = append(definitions.Defaults, pnpmDefinitions.Defaults...)
	for name, values := range pnpmDefinitions.Named {
		definitions.Named[name] = append(definitions.Named[name], values...)
	}

	directories := workspaceDirectories(root, reader, patterns, warnings)
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
		addDependencyMaps(workspace, relative, set, &catalogRefs, warnings)
		addOverrideCandidates(workspace.Overrides, relative, set, warnings)
		addOverrideCandidates(workspace.Resolutions, relative, set, warnings)
	}
	resolveCatalogReferences(catalogRefs, definitions, set, warnings)
	return directories
}

func addDependencyMaps(
	manifest packageJSON,
	source string,
	set *candidateSet,
	catalogRefs *[]catalogReference,
	warnings *[]string,
) {
	for _, dependencies := range []map[string]string{
		manifest.Dependencies,
		manifest.DevDependencies,
		manifest.OptionalDependencies,
	} {
		for _, name := range sortedStringKeys(dependencies) {
			if name != "@rhds/elements" {
				continue
			}
			spec := dependencies[name]
			if strings.HasPrefix(spec, "catalog:") {
				*catalogRefs = append(*catalogRefs, catalogReference{
					Name:   strings.TrimPrefix(spec, "catalog:"),
					Source: source,
				})
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
	spec = strings.TrimPrefix(spec, "workspace:")
	version, parsed := exactVersion(spec)
	if !parsed.OK {
		*warnings = append(*warnings,
			fmt.Sprintf("could not parse an @rhds/elements version from %q in %s", spec, source))
		return
	}
	set.add(version, spec, source, PrecedencePackageJSON)
}

// isElementsSelector matches npm, yarn, and pnpm override keys such as
// "@rhds/elements", "**/@rhds/elements", and "@rhds/elements@^5.0.0".
func isElementsSelector(key string) bool {
	return key == "@rhds/elements" ||
		strings.HasSuffix(key, "/@rhds/elements") ||
		strings.HasPrefix(key, "@rhds/elements@") ||
		strings.Contains(key, "@rhds/elements@")
}

func addOverrideCandidates(value any, source string, set *candidateSet, warnings *[]string) {
	addOverrideCandidatesAt(value, source, set, warnings, 0)
}

func addOverrideCandidatesAt(value any, source string, set *candidateSet, warnings *[]string, depth int) {
	if depth > maxDepth {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range sortedAnyMapKeys(typed) {
			item := typed[key]
			if isElementsSelector(key) {
				addOverrideValue(item, source, set, warnings)
			}
			addOverrideCandidatesAt(item, source, set, warnings, depth+1)
		}
	case map[any]any:
		for _, key := range sortedAnyKeys(typed) {
			item := typed[key]
			if text, ok := key.(string); ok && isElementsSelector(text) {
				addOverrideValue(item, source, set, warnings)
			}
			addOverrideCandidatesAt(item, source, set, warnings, depth+1)
		}
	}
}

// addOverrideValue handles a string version and npm's nested override form,
// {"@rhds/elements": {".": "4.2.2"}}.
func addOverrideValue(value any, source string, set *candidateSet, warnings *[]string) {
	switch typed := value.(type) {
	case string:
		addPackageSpec(typed, source, set, warnings)
	case map[string]any:
		if spec, ok := typed["."].(string); ok {
			addPackageSpec(spec, source, set, warnings)
		}
	case map[any]any:
		if spec, ok := typed["."].(string); ok {
			addPackageSpec(spec, source, set, warnings)
		}
	}
}

func addCatalogDefinitions(value any, source string, defaultCatalog bool, definitions *catalogDefinitions) {
	if value == nil {
		return
	}
	if defaultCatalog {
		definitions.Defaults = append(definitions.Defaults, catalogDefinition{
			Source: source,
			Values: catalogValues(value),
		})
		return
	}
	named := stringMap(value)
	for _, name := range sortedAnyMapKeys(named) {
		definitions.Named[name] = append(definitions.Named[name], catalogDefinition{
			Source: source,
			Values: catalogValues(named[name]),
		})
	}
}

func catalogValues(value any) map[string]string {
	values := stringMap(value)
	result := make(map[string]string)
	for _, name := range sortedAnyMapKeys(values) {
		if spec, ok := values[name].(string); ok {
			result[name] = spec
		}
	}
	return result
}

func resolveCatalogReferences(
	references []catalogReference,
	definitions *catalogDefinitions,
	set *candidateSet,
	warnings *[]string,
) {
	for _, reference := range references {
		var matched bool
		if reference.Name == "" {
			for _, definition := range definitions.Defaults {
				if spec, ok := definition.Values["@rhds/elements"]; ok {
					addPackageSpec(spec, definition.Source, set, warnings)
					matched = true
				}
			}
		} else {
			for _, definition := range definitions.Named[reference.Name] {
				if spec, ok := definition.Values["@rhds/elements"]; ok {
					addPackageSpec(spec, definition.Source, set, warnings)
					matched = true
				}
			}
		}
		if !matched {
			*warnings = append(*warnings, fmt.Sprintf(
				"@rhds/elements in %s uses catalog:%s but no matching catalog entry was found",
				reference.Source, reference.Name))
		}
	}
}

func workspaceDirectories(
	root string,
	reader *workspaceReader,
	patterns []string,
	warnings *[]string,
) []string {
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
			realMatch, err := reader.resolveWithin(match)
			if err != nil {
				continue
			}
			info, err := os.Lstat(realMatch)
			if err != nil {
				continue
			}
			directory := realMatch
			if info.Mode().IsRegular() && filepath.Base(realMatch) == "package.json" {
				directory = filepath.Dir(realMatch)
			} else if !info.IsDir() {
				continue
			}
			relative, err := filepath.Rel(root, directory)
			if err != nil || !withinRoot(root, directory) {
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
	reader *workspaceReader,
	warnings *[]string,
) ([]string, *catalogDefinitions) {
	definitions := newCatalogDefinitions()
	payload, err := reader.read("pnpm-workspace.yaml")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			*warnings = append(*warnings, fmt.Sprintf("could not read pnpm-workspace.yaml: %v", err))
		}
		return nil, definitions
	}
	var document any
	if err := yaml.Unmarshal(payload, &document); err != nil {
		*warnings = append(*warnings, fmt.Sprintf("could not parse pnpm-workspace.yaml: %v", err))
		return nil, definitions
	}
	values := stringMap(document)
	if values == nil {
		return nil, definitions
	}
	addCatalogDefinitions(values["catalog"], "pnpm-workspace.yaml", true, definitions)
	addCatalogDefinitions(values["catalogs"], "pnpm-workspace.yaml", false, definitions)
	return workspacePatterns(values["packages"]), definitions
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
	candidates = append(candidates, pnpmStorePackages(reader, warnings)...)

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
// content-addressed store. Directory reads go through the containment-checked
// workspace reader.
func pnpmStorePackages(reader *workspaceReader, warnings *[]string) []string {
	entries, err := reader.readDir("node_modules/.pnpm")
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

func sortedStringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedAnyMapKeys(values map[string]any) []string {
	return sortedStringKeys(values)
}

func sortedAnyKeys(values map[any]any) []any {
	keys := make([]any, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j])
	})
	return keys
}
