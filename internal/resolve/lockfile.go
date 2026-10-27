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
const maxCandidates = 512

// candidateSet deduplicates candidates from one source while preserving a
// deterministic order.
type candidateSet struct {
	seen      map[string]bool
	items     []Candidate
	truncated bool
}

func newCandidateSet() *candidateSet {
	return &candidateSet{seen: make(map[string]bool)}
}

func (s *candidateSet) add(version, spec, source string, precedence int) {
	version = strings.TrimSpace(version)
	if version == "" {
		return
	}
	key := source + "\x00" + version
	if s.seen[key] {
		return
	}
	if len(s.items) >= maxCandidates {
		s.truncated = true
		return
	}
	s.seen[key] = true
	s.items = append(s.items, Candidate{
		Version:    version,
		Spec:       spec,
		Source:     source,
		Precedence: precedence,
		Level:      LevelForPrecedence(precedence),
	})
}

func (s *candidateSet) sorted() []Candidate {
	sort.SliceStable(s.items, func(i, j int) bool {
		if s.items[i].Version != s.items[j].Version {
			return s.items[i].Version < s.items[j].Version
		}
		return s.items[i].Source < s.items[j].Source
	})
	return s.items
}

// parseLockfileData parses one supported lockfile. The name selects the
// parser; callers pass a base name such as "package-lock.json".
func parseLockfileData(name string, data []byte, limits Limits) ([]Candidate, error) {
	limits = limits.withDefaults()
	if int64(len(data)) > limits.MaxFileBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes", errTooLarge, name, len(data))
	}
	set := newCandidateSet()
	switch filepath.Base(name) {
	case "package-lock.json":
		if err := parsePackageLock(data, name, set); err != nil {
			return nil, err
		}
	case "pnpm-lock.yaml":
		if err := parsePNPMLock(data, name, set); err != nil {
			return nil, err
		}
	case "yarn.lock":
		parseYarnLock(data, name, set)
	case "bun.lock":
		if err := parseBunLock(data, name, set); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported lockfile %q", name)
	}
	return set.sorted(), nil
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
	for key, entry := range lock.Packages {
		if !packageLockKeyIsElements(key) {
			continue
		}
		version, parsed := exactVersion(entry.Version)
		if !parsed.OK && entry.Version == "" {
			continue
		}
		set.add(version, entry.Version, source, PrecedenceLockfile)
	}
	var walk func(map[string]packageLockDependency, int)
	walk = func(dependencies map[string]packageLockDependency, depth int) {
		if depth > maxDepth {
			return
		}
		for name, dependency := range dependencies {
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

func parsePNPMLock(data []byte, source string, set *candidateSet) error {
	var document any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	walkDocument(document, func(key string, value any) {
		switch {
		case key == "@rhds/elements":
			addSpecValue(value, source, set)
		case strings.HasPrefix(key, "@rhds/elements@"):
			spec := strings.TrimPrefix(key, "@rhds/elements@")
			if index := strings.IndexByte(spec, '('); index >= 0 {
				spec = spec[:index]
			}
			spec = strings.TrimPrefix(spec, "npm:")
			version, parsed := exactVersion(spec)
			if parsed.OK {
				set.add(version, spec, source, PrecedenceLockfile)
			}
		}
	})
	return nil
}

func addSpecValue(value any, source string, set *candidateSet) {
	switch typed := value.(type) {
	case string:
		version, parsed := exactVersion(typed)
		if parsed.OK {
			set.add(version, typed, source, PrecedenceLockfile)
		}
	case map[string]any:
		addSpecFromMap(typed, source, set)
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			if text, ok := key.(string); ok {
				converted[text] = item
			}
		}
		addSpecFromMap(converted, source, set)
	case []any:
		if len(typed) > 0 {
			addSpecValue(typed[0], source, set)
		}
	}
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

func parseBunLock(data []byte, source string, set *candidateSet) error {
	converted := jsonc.ToJSON(data)
	var document any
	if err := json.Unmarshal(converted, &document); err != nil {
		return err
	}
	walkDocument(document, func(key string, value any) {
		switch {
		case key == "@rhds/elements":
			switch typed := value.(type) {
			case string:
				version, parsed := exactVersion(typed)
				if parsed.OK {
					set.add(version, typed, source, PrecedenceLockfile)
				}
			case []any:
				for _, entry := range typed {
					spec, ok := entry.(string)
					if !ok {
						continue
					}
					version, parsed := exactVersion(spec)
					if parsed.OK {
						set.add(version, spec, source, PrecedenceLockfile)
						break
					}
				}
			case map[string]any:
				addSpecValue(typed, source, set)
			}
		case strings.HasPrefix(key, "@rhds/elements@"):
			spec := strings.TrimPrefix(key, "@rhds/elements@")
			spec = strings.TrimPrefix(spec, "npm:")
			version, parsed := exactVersion(spec)
			if parsed.OK {
				set.add(version, spec, source, PrecedenceLockfile)
			}
		}
	})
	return nil
}

// walkDocument visits every key/value pair in a decoded JSON or YAML
// document with a bounded, iterative walk.
func walkDocument(document any, visit func(key string, value any)) {
	type item struct {
		value any
		depth int
	}
	stack := []item{{value: document}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.depth > maxDepth {
			continue
		}
		switch typed := current.value.(type) {
		case map[string]any:
			for key, value := range typed {
				visit(key, value)
				stack = append(stack, item{value: value, depth: current.depth + 1})
			}
		case map[any]any:
			for key, value := range typed {
				if text, ok := key.(string); ok {
					visit(text, value)
				}
				stack = append(stack, item{value: value, depth: current.depth + 1})
			}
		case []any:
			for _, value := range typed {
				stack = append(stack, item{value: value, depth: current.depth + 1})
			}
		}
	}
}
