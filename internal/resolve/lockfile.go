// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tidwall/jsonc"
	"gopkg.in/yaml.v3"
)

// maxDepth bounds generic document walks. It protects the resolver from
// deeply nested or alias-expanded YAML and JSON.
const maxDepth = 100

// maxCandidates bounds candidate collection from one workspace. A hostile or
// corrupt lockfile must not grow memory without limit.
const maxCandidates = 4096

// maxCandidateRunes bounds a candidate's version and spec strings. Lockfiles
// are untrusted input and can contain multi-megabyte values.
const maxCandidateRunes = 256

// candidateSet deduplicates candidates from one source while preserving a
// deterministic order.
type candidateSet struct {
	seen      map[string]int
	items     []Candidate
	truncated bool
}

func newCandidateSet() *candidateSet {
	return &candidateSet{seen: make(map[string]int)}
}

func (s *candidateSet) add(version, spec, source string, precedence int) {
	version = truncateRunes(strings.TrimSpace(version), maxCandidateRunes)
	spec = truncateRunes(strings.TrimSpace(spec), maxCandidateRunes)
	if version == "" {
		return
	}
	key := source + "\x00" + version
	if index, ok := s.seen[key]; ok {
		// A map traversal can reach the same version through different
		// specifiers. Keep the lexicographically smallest so output does not
		// depend on map iteration order.
		if spec != "" && (s.items[index].Spec == "" || spec < s.items[index].Spec) {
			s.items[index].Spec = spec
		}
		return
	}
	candidate := Candidate{
		Version:    version,
		Spec:       spec,
		Source:     source,
		Precedence: precedence,
		Level:      LevelForPrecedence(precedence),
	}
	if len(s.items) < maxCandidates {
		s.seen[key] = len(s.items)
		s.items = append(s.items, candidate)
		return
	}
	s.truncated = true
	worst := s.worstIndex()
	if worst >= 0 && candidateBetter(candidate, s.items[worst]) {
		delete(s.seen, s.items[worst].Source+"\x00"+s.items[worst].Version)
		s.items[worst] = candidate
		s.seen[key] = worst
	}
}

func (s *candidateSet) worstIndex() int {
	if len(s.items) == 0 {
		return -1
	}
	worst := 0
	for i := 1; i < len(s.items); i++ {
		if candidateBetter(s.items[worst], s.items[i]) {
			worst = i
		}
	}
	return worst
}

// candidateBetter orders candidates for deterministic retention under the
// cap: higher precedence first (lower number), then higher version, then
// lexical source and spec.
func candidateBetter(a, b Candidate) bool {
	if a.Precedence != b.Precedence {
		return a.Precedence < b.Precedence
	}
	left := ParseVersionSpec(a.Version)
	right := ParseVersionSpec(b.Version)
	if left.OK && right.OK {
		if order := CompareParsed(left, right); order != 0 {
			return order > 0
		}
	} else if left.OK != right.OK {
		return left.OK
	}
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	return a.Spec < b.Spec
}

func (s *candidateSet) sorted() []Candidate {
	sort.SliceStable(s.items, func(i, j int) bool {
		if s.items[i].Precedence != s.items[j].Precedence {
			return s.items[i].Precedence < s.items[j].Precedence
		}
		left := ParseVersionSpec(s.items[i].Version)
		right := ParseVersionSpec(s.items[j].Version)
		if left.OK && right.OK {
			if order := CompareParsed(left, right); order != 0 {
				return order > 0
			}
		} else if left.OK != right.OK {
			return left.OK
		}
		if s.items[i].Source != s.items[j].Source {
			return s.items[i].Source < s.items[j].Source
		}
		return s.items[i].Version < s.items[j].Version
	})
	return s.items
}

// parseLockfileData parses one supported lockfile. The name selects the
// parser; callers pass a base name such as "package-lock.json". The bool
// reports that the candidate cap was reached.
func parseLockfileData(name string, data []byte, limits Limits) ([]Candidate, bool, error) {
	limits = limits.withDefaults()
	if int64(len(data)) > limits.MaxFileBytes {
		return nil, false, fmt.Errorf("%w: %s is %d bytes", errTooLarge, name, len(data))
	}
	set := newCandidateSet()
	switch filepath.Base(name) {
	case "package-lock.json":
		if err := parsePackageLock(data, name, set); err != nil {
			return nil, false, err
		}
	case "pnpm-lock.yaml":
		if err := parsePNPMLock(data, name, set); err != nil {
			return nil, false, err
		}
	case "yarn.lock":
		parseYarnLock(data, name, set)
	case "bun.lock":
		if err := parseBunLock(data, name, set); err != nil {
			return nil, false, err
		}
	default:
		return nil, false, fmt.Errorf("unsupported lockfile %q", name)
	}
	return set.sorted(), set.truncated, nil
}

type packageLockDependency struct {
	Version      string                           `json:"version"`
	Dependencies map[string]packageLockDependency `json:"dependencies"`
}

func parsePackageLock(data []byte, source string, set *candidateSet) error {
	var lock struct {
		Packages     map[string]struct{ Version string } `json:"packages"`
		Dependencies map[string]packageLockDependency    `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return err
	}
	for _, key := range sortedStringKeys(lock.Packages) {
		entry := lock.Packages[key]
		if !packageLockKeyIsElements(key) {
			continue
		}
		if entry.Version == "" {
			continue
		}
		version, _ := exactVersion(entry.Version)
		set.add(version, entry.Version, source, PrecedenceLockfile)
	}
	var walk func(map[string]packageLockDependency, int)
	walk = func(dependencies map[string]packageLockDependency, depth int) {
		if depth > maxDepth {
			return
		}
		for _, name := range sortedStringKeys(dependencies) {
			dependency := dependencies[name]
			if name == "@rhds/elements" {
				version, _ := exactVersion(dependency.Version)
				set.add(version, dependency.Version, source, PrecedenceLockfile)
			}
			if len(dependency.Dependencies) > 0 {
				walk(dependency.Dependencies, depth+1)
			}
		}
	}
	walk(lock.Dependencies, 0)
	return nil
}

func packageLockKeyIsElements(key string) bool {
	return key == "@rhds/elements" ||
		key == "node_modules/@rhds/elements" ||
		strings.HasSuffix(key, "/node_modules/@rhds/elements")
}

// parsePNPMLock reads only resolved versions. Importers contribute
// dependencies, devDependencies, and optionalDependencies version fields;
// packages and snapshots contribute package IDs. peerDependencies ranges are
// deliberately ignored: they are constraints, not pins.
func parsePNPMLock(data []byte, source string, set *candidateSet) error {
	var document any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	top := stringMap(document)
	if top == nil {
		return nil
	}
	importers := stringMap(top["importers"])
	for _, name := range sortedAnyMapKeys(importers) {
		importer := stringMap(importers[name])
		for _, section := range []string{"dependencies", "devDependencies", "optionalDependencies"} {
			dependencies := stringMap(importer[section])
			if dependency, ok := dependencies["@rhds/elements"]; ok {
				addPNPMResolved(dependency, source, set)
			}
		}
	}
	for _, section := range []string{"packages", "snapshots"} {
		packages := stringMap(top[section])
		for _, key := range sortedAnyMapKeys(packages) {
			if version, ok := versionFromPNPMKey(key); ok {
				set.add(version, key, source, PrecedenceLockfile)
			}
		}
	}
	return nil
}

func addPNPMResolved(value any, source string, set *candidateSet) {
	values := stringMap(value)
	if values == nil {
		// A string here is a range, not a resolved version.
		return
	}
	spec, ok := values["version"].(string)
	if !ok {
		return
	}
	version, parsed := exactVersion(spec)
	if parsed.OK {
		set.add(version, spec, source, PrecedenceLockfile)
	}
}

// versionFromPNPMKey understands the package ID forms used by pnpm majors:
//
//	/@rhds/elements/4.2.2        (v5)
//	/@rhds/elements@4.2.2        (v6+)
//	@rhds/elements@4.2.2(peer@1) (v7+)
func versionFromPNPMKey(key string) (string, bool) {
	trimmed := strings.TrimPrefix(key, "/")
	var rest string
	switch {
	case strings.HasPrefix(trimmed, "@rhds/elements@"):
		rest = trimmed[len("@rhds/elements@"):]
	case strings.HasPrefix(trimmed, "@rhds/elements/"):
		rest = trimmed[len("@rhds/elements/"):]
	default:
		return "", false
	}
	rest = strings.TrimPrefix(rest, "npm:")
	if index := strings.IndexAny(rest, "(_"); index >= 0 {
		rest = rest[:index]
	}
	parsed := ParseVersionSpecLeading(rest)
	if !parsed.OK || parsed.Partial {
		return "", false
	}
	return parsed.Text, true
}

func parseYarnLock(data []byte, source string, set *candidateSet) {
	var (
		currentSpec     string
		currentResolved string
	)
	flush := func() {
		if currentSpec == "" {
			return
		}
		spec := currentSpec
		if currentResolved != "" {
			spec = currentResolved
		}
		version, parsed := exactVersion(spec)
		if parsed.OK {
			set.add(version, currentSpec, source, PrecedenceLockfile)
		}
		currentSpec = ""
		currentResolved = ""
	}

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' && line[0] != '#' && strings.HasSuffix(line, ":") {
			flush()
			if spec, ok := yarnElementSpec(line); ok {
				currentSpec = spec
			}
			continue
		}
		if currentSpec == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "version"):
			if value, ok := yarnValue(trimmed, "version"); ok {
				currentResolved = value
			}
		case strings.HasPrefix(trimmed, "resolution"):
			if value, ok := yarnValue(trimmed, "resolution"); ok && currentResolved == "" {
				if spec, ok := yarnElementSpec(value); ok {
					currentResolved = spec
				}
			}
		}
	}
	flush()
}

func yarnElementSpec(header string) (string, bool) {
	header = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(header), ":"))
	for _, selector := range strings.Split(header, ",") {
		selector = strings.Trim(strings.TrimSpace(selector), `"`)
		index := strings.Index(selector, "@rhds/elements@")
		if index < 0 {
			continue
		}
		spec := selector[index+len("@rhds/elements@"):]
		spec = strings.Trim(spec, `"`)
		return spec, true
	}
	return "", false
}

func yarnValue(line, field string) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, field))
	rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
	rest = strings.Trim(rest, `"`)
	return rest, rest != ""
}

// parseBunLock reads only resolved package entries. Workspace dependency
// ranges are not pins and are ignored.
func parseBunLock(data []byte, source string, set *candidateSet) error {
	converted := jsonc.ToJSON(data)
	var document any
	if err := json.Unmarshal(converted, &document); err != nil {
		return err
	}
	top := stringMap(document)
	if top == nil {
		return nil
	}
	packages := stringMap(top["packages"])
	for _, key := range sortedAnyMapKeys(packages) {
		value := packages[key]
		if key == "@rhds/elements" {
			switch typed := value.(type) {
			case string:
				if version, ok := versionFromBunSpec(typed); ok {
					set.add(version, typed, source, PrecedenceLockfile)
				}
			case []any:
				for _, entry := range typed {
					spec, ok := entry.(string)
					if !ok {
						continue
					}
					if version, ok := versionFromBunSpec(spec); ok {
						set.add(version, spec, source, PrecedenceLockfile)
						break
					}
				}
			case map[string]any:
				addSpecFromMap(typed, source, set)
			}
			continue
		}
		if version, ok := versionFromBunKey(key); ok {
			set.add(version, key, source, PrecedenceLockfile)
		}
	}
	return nil
}

func versionFromBunSpec(spec string) (string, bool) {
	index := strings.LastIndex(spec, "@")
	if index < 0 {
		return "", false
	}
	parsed := ParseVersionSpecLeading(spec[index+1:])
	if !parsed.OK || parsed.Partial {
		return "", false
	}
	return parsed.Text, true
}

func versionFromBunKey(key string) (string, bool) {
	trimmed := strings.TrimPrefix(key, "/")
	if !strings.HasPrefix(trimmed, "@rhds/elements@") {
		return "", false
	}
	parsed := ParseVersionSpecLeading(strings.TrimPrefix(trimmed, "@rhds/elements@"))
	if !parsed.OK || parsed.Partial {
		return "", false
	}
	return parsed.Text, true
}

func addSpecFromMap(values map[string]any, source string, set *candidateSet) {
	for _, field := range []string{"version", "specifier"} {
		if spec, ok := values[field].(string); ok {
			version, parsed := exactVersion(spec)
			if parsed.OK {
				set.add(version, spec, source, PrecedenceLockfile)
				return
			}
		}
	}
}
