// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
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
	if key == "@rhds/elements" ||
		strings.HasSuffix(key, "/@rhds/elements") ||
		strings.HasPrefix(key, "@rhds/elements@") ||
		strings.Contains(key, "@rhds/elements@") {
		return true
	}
	// pnpm supports parent>child selectors, including >>.
	if index := strings.LastIndex(key, ">"); index >= 0 {
		segment := strings.TrimSpace(key[index+1:])
		return segment == "@rhds/elements" || strings.HasPrefix(segment, "@rhds/elements@")
	}
	return false
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
		if reference.Name == "" || reference.Name == "default" {
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

// scanBudget bounds directory enumeration across every workspace pattern.
// Entry limits are shared so many broad patterns cannot multiply the work.
type scanBudget struct {
	limits    Limits
	visited   int
	exhausted bool
}

func newScanBudget(limits Limits) *scanBudget {
	return &scanBudget{limits: limits}
}

func (b *scanBudget) remaining() int {
	if b.exhausted {
		return 0
	}
	remaining := b.limits.MaxScanEntries - b.visited
	if remaining <= 0 {
		b.exhausted = true
		return 0
	}
	return remaining
}

func (b *scanBudget) consume(entries int) {
	b.visited += entries
	if b.visited >= b.limits.MaxScanEntries {
		b.exhausted = true
	}
}

// workspaceExpansion reports one bounded workspace pattern expansion and which
// configured limit stopped it.
type workspaceExpansion struct {
	directories    []string
	invalid        bool
	hitEntryLimit  bool
	hitDepthLimit  bool
	hitResultLimit bool
}

// expandWorkspacePattern walks a workspace glob one segment at a time. It never
// materializes a whole directory tree: each readDir is capped by the remaining
// entry budget, and traversal stops at the package result limit or the depth
// limit.
func expandWorkspacePattern(
	reader *workspaceReader,
	pattern string,
	budget *scanBudget,
	resultLimit int,
) workspaceExpansion {
	var result workspaceExpansion
	pattern = strings.TrimSpace(pattern)
	pattern = filepath.ToSlash(pattern)
	pattern = strings.TrimPrefix(pattern, "./")
	if pattern == "" || pattern == "." {
		return result
	}
	cleaned := path.Clean(pattern)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		result.invalid = true
		return result
	}
	segments := strings.Split(cleaned, "/")
	for _, segment := range segments {
		if isGlobSegment(segment) {
			if _, err := path.Match(segment, ""); err != nil {
				result.invalid = true
				return result
			}
		}
	}

	current := []string{""}
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		if budget.exhausted {
			result.hitEntryLimit = true
			return result
		}
		var next []string
		switch {
		case segment == "**":
			for _, base := range current {
				expanded, entryHit, depthHit, resultHit := expandWorkspaceRecursive(
					reader, base, budget, resultLimit)
				result.hitEntryLimit = result.hitEntryLimit || entryHit
				result.hitDepthLimit = result.hitDepthLimit || depthHit
				result.hitResultLimit = result.hitResultLimit || resultHit
				next = appendBounded(next, expanded, resultLimit, &result.hitResultLimit)
				if budget.exhausted {
					result.hitEntryLimit = true
					break
				}
			}
		case isGlobSegment(segment):
			for _, base := range current {
				readPath := base
				if readPath == "" {
					readPath = "."
				}
				entries, truncated, err := reader.readDir(readPath, budget.remaining())
				if truncated {
					result.hitEntryLimit = true
				}
				if err != nil {
					continue
				}
				budget.consume(len(entries))
				for _, entry := range entries {
					if !entry.IsDir() || entry.Name() == "node_modules" {
						continue
					}
					matched, err := path.Match(segment, entry.Name())
					if err != nil || !matched {
						continue
					}
					child := joinWorkspacePath(base, entry.Name())
					if workspaceDepth(child) > budget.limits.MaxScanDepth {
						result.hitDepthLimit = true
						continue
					}
					if resultLimit > 0 && len(next) >= resultLimit {
						result.hitResultLimit = true
						break
					}
					next = append(next, child)
				}
				if budget.exhausted {
					result.hitEntryLimit = true
					break
				}
			}
		default:
			for _, base := range current {
				child := joinWorkspacePath(base, segment)
				switch {
				case workspaceDepth(child) > budget.limits.MaxScanDepth:
					result.hitDepthLimit = true
				case reader.isDirectory(child):
					if resultLimit > 0 && len(next) >= resultLimit {
						result.hitResultLimit = true
						break
					}
					next = append(next, child)
				case reader.isRegularFile(child) && path.Base(child) == "package.json":
					if base != "" {
						next = append(next, base)
					}
				}
			}
		}
		current = uniqueSorted(next)
		if len(current) == 0 {
			return result
		}
		if resultLimit > 0 && len(current) >= resultLimit {
			// Keep walking so a later segment can narrow the matches, but
			// report that the intermediate set hit the bound.
			if len(current) > resultLimit {
				current = current[:resultLimit]
				result.hitResultLimit = true
			}
		}
	}
	for _, directory := range current {
		if !reader.isDirectory(directory) {
			continue
		}
		if resultLimit > 0 && len(result.directories) >= resultLimit {
			result.hitResultLimit = true
			break
		}
		result.directories = append(result.directories, directory)
	}
	return result
}

// expandWorkspaceRecursive expands a ** segment from base, visiting each
// directory at most once within the shared entry and depth budgets.
func expandWorkspaceRecursive(
	reader *workspaceReader,
	base string,
	budget *scanBudget,
	resultLimit int,
) (directories []string, entryHit, depthHit, resultHit bool) {
	queue := []string{base}
	for len(queue) > 0 {
		directory := queue[0]
		queue = queue[1:]
		if directory != "" {
			if resultLimit > 0 && len(directories) >= resultLimit {
				resultHit = true
				return directories, entryHit, depthHit, resultHit
			}
			directories = append(directories, directory)
		}
		if budget.exhausted {
			return directories, true, depthHit, resultHit
		}
		if directory != "" && workspaceDepth(directory) >= budget.limits.MaxScanDepth {
			depthHit = true
			continue
		}
		readPath := directory
		if readPath == "" {
			readPath = "."
		}
		entries, truncated, err := reader.readDir(readPath, budget.remaining())
		if truncated {
			entryHit = true
		}
		if err != nil {
			continue
		}
		budget.consume(len(entries))
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == "node_modules" {
				continue
			}
			child := joinWorkspacePath(directory, entry.Name())
			if workspaceDepth(child) > budget.limits.MaxScanDepth {
				depthHit = true
				continue
			}
			queue = append(queue, child)
		}
	}
	return directories, entryHit, depthHit, resultHit
}

func appendBounded(destination, values []string, limit int, hitLimit *bool) []string {
	for _, value := range values {
		if limit > 0 && len(destination) >= limit {
			*hitLimit = true
			return destination
		}
		destination = append(destination, value)
	}
	return destination
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	unique := values[:0]
	for _, value := range values {
		if len(unique) > 0 && unique[len(unique)-1] == value {
			continue
		}
		unique = append(unique, value)
	}
	return unique
}

func isGlobSegment(segment string) bool {
	return strings.ContainsAny(segment, "*?[")
}

func joinWorkspacePath(base, name string) string {
	if base == "" {
		return name
	}
	return base + "/" + name
}

func workspaceDepth(relative string) int {
	if relative == "" {
		return 0
	}
	return strings.Count(relative, "/") + 1
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
	budget := newScanBudget(reader.limits)
	var positive []string
	var negative []string
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		if strings.HasPrefix(pattern, "!") {
			pattern = strings.TrimSpace(strings.TrimPrefix(pattern, "!"))
			if pattern != "" {
				negative = append(negative, pattern)
			}
			continue
		}
		positive = append(positive, pattern)
	}
	if len(positive) == 0 {
		return nil
	}

	warned := make(map[string]bool)
	warnOnce := func(key, message string) {
		if warned[key] {
			return
		}
		warned[key] = true
		*warnings = append(*warnings, message)
	}
	recordExpansion := func(pattern string, expansion workspaceExpansion) {
		if expansion.invalid {
			*warnings = append(*warnings, fmt.Sprintf("invalid workspace pattern %q",
				truncateRunes(pattern, maxCandidateRunes)))
		}
		if expansion.hitEntryLimit {
			warnOnce("entries", fmt.Sprintf(
				"workspace discovery stopped after visiting %d directory entries",
				reader.limits.MaxScanEntries))
		}
		if expansion.hitDepthLimit {
			warnOnce("depth", fmt.Sprintf(
				"workspace discovery stopped at the maximum scan depth of %d",
				reader.limits.MaxScanDepth))
		}
		if expansion.hitResultLimit {
			warnOnce("packages", fmt.Sprintf(
				"workspace discovery stopped after %d packages", maxWorkspacePackages))
		}
	}

	seen := make(map[string]bool)
	var directories []string
	capped := false

	// Collect positive matches first, bounded by the package cap.
	for _, pattern := range positive {
		remaining := maxWorkspacePackages - len(directories)
		if remaining <= 0 {
			warnOnce("packages", fmt.Sprintf(
				"workspace discovery stopped after %d packages", maxWorkspacePackages))
			break
		}
		expansion := expandWorkspacePattern(reader, pattern, budget, remaining)
		recordExpansion(pattern, expansion)
		for _, match := range expansion.directories {
			realMatch, err := reader.resolveWithin(filepath.Join(root, filepath.FromSlash(match)))
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
			relative, err := filepath.Rel(reader.realRoot, directory)
			if err != nil || !withinRoot(reader.realRoot, directory) {
				continue
			}
			relative = filepath.ToSlash(relative)
			if relative == "." || seen[relative] {
				continue
			}
			seen[relative] = true
			if len(directories) >= maxWorkspacePackages {
				warnOnce("packages", fmt.Sprintf(
					"workspace discovery stopped after %d packages", maxWorkspacePackages))
				capped = true
				break
			}
			directories = append(directories, relative)
		}
		if capped || budget.exhausted {
			break
		}
	}

	// Negated patterns remove directories from the positive matches before any
	// package manifest is read, so an excluded package cannot add evidence.
	if len(negative) > 0 {
		excluded := make(map[string]bool)
		for _, pattern := range negative {
			expansion := expandWorkspacePattern(reader, pattern, budget, 0)
			recordExpansion(pattern, expansion)
			for _, directory := range expansion.directories {
				excluded[directory] = true
			}
		}
		if len(excluded) > 0 {
			filtered := directories[:0]
			for _, directory := range directories {
				if !excluded[directory] {
					filtered = append(filtered, directory)
				}
			}
			directories = filtered
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

// directoryReader lists at most limit entries from a workspace-relative
// directory. The bool reports that more entries exist than the limit.
type directoryReader interface {
	readDir(relative string, limit int) ([]os.DirEntry, bool, error)
}

// pnpmStorePackages lists the @rhds/elements package manifests inside a pnpm
// content-addressed store. Directory reads are bounded: the reader is asked for
// at most maxWorkspacePackages entries and the listing stops there instead of
// materializing a large store.
func pnpmStorePackages(reader directoryReader, warnings *[]string) []string {
	entries, truncated, err := reader.readDir("node_modules/.pnpm", maxWorkspacePackages)
	if err != nil {
		return nil
	}
	if truncated {
		*warnings = append(*warnings, fmt.Sprintf(
			"pnpm store scan stopped after %d directory entries; some installed @rhds/elements packages may be omitted",
			maxWorkspacePackages))
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
