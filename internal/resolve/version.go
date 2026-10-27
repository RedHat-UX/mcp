// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// Version precedence levels, lowest number wins.
const (
	PrecedenceExplicit = iota + 1
	PrecedenceLockfile
	PrecedenceInstalled
	PrecedencePackageJSON
	PrecedenceTemplate
	PrecedenceBundled
)

// Human-readable level names reported with candidates.
const (
	LevelExplicit    = "explicit"
	LevelLockfile    = "lockfile"
	LevelInstalled   = "installed"
	LevelPackageJSON = "package.json"
	LevelTemplate    = "template"
	LevelBundled     = "bundled"
)

// LevelForPrecedence returns the stable level name for a precedence value.
func LevelForPrecedence(precedence int) string {
	switch precedence {
	case PrecedenceExplicit:
		return LevelExplicit
	case PrecedenceLockfile:
		return LevelLockfile
	case PrecedenceInstalled:
		return LevelInstalled
	case PrecedencePackageJSON:
		return LevelPackageJSON
	case PrecedenceTemplate:
		return LevelTemplate
	case PrecedenceBundled:
		return LevelBundled
	default:
		return "unknown"
	}
}

// ParsedVersion is the first semantic version found in a specifier.
type ParsedVersion struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease string
	Text       string
	OK         bool
}

// maxVersionDigits bounds each numeric component so malformed or hostile
// version strings cannot overflow strconv.Atoi.
const maxVersionDigits = 9

// ParseVersionSpec extracts the first x.y.z semantic version from a package
// specifier. It accepts ranges, npm aliases, workspace protocol prefixes, and
// CDN URLs. It intentionally rejects partial versions such as "5" and
// non-version specifiers such as "latest", "*", and "file:".
func ParseVersionSpec(spec string) ParsedVersion {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ParsedVersion{}
	}
	for i := 0; i < len(spec); i++ {
		if !isDigit(spec[i]) {
			continue
		}
		if parsed, ok := parseVersionAt(spec, i); ok {
			return parsed
		}
	}
	return ParsedVersion{}
}

func parseVersionAt(spec string, start int) (ParsedVersion, bool) {
	index := start
	major, next, ok := parseNumber(spec, index)
	if !ok {
		return ParsedVersion{}, false
	}
	index = next
	if index >= len(spec) || spec[index] != '.' {
		return ParsedVersion{}, false
	}
	minor, next, ok := parseNumber(spec, index+1)
	if !ok {
		return ParsedVersion{}, false
	}
	index = next
	if index >= len(spec) || spec[index] != '.' {
		return ParsedVersion{}, false
	}
	patch, next, ok := parseNumber(spec, index+1)
	if !ok {
		return ParsedVersion{}, false
	}
	index = next

	end := index
	if end < len(spec) && spec[end] == '-' {
		preEnd := end + 1
		for preEnd < len(spec) && isPrereleaseChar(spec[preEnd]) {
			preEnd++
		}
		if preEnd == end+1 {
			return ParsedVersion{}, false
		}
		end = preEnd
	}
	if end < len(spec) && spec[end] == '+' {
		buildEnd := end + 1
		for buildEnd < len(spec) && isPrereleaseChar(spec[buildEnd]) {
			buildEnd++
		}
		if buildEnd == end+1 {
			return ParsedVersion{}, false
		}
		end = buildEnd
	}

	text := spec[start:end]
	prerelease := ""
	if dash := strings.IndexByte(text, '-'); dash >= 0 {
		prerelease = text[dash+1:]
		if plus := strings.IndexByte(prerelease, '+'); plus >= 0 {
			prerelease = prerelease[:plus]
		}
	}
	return ParsedVersion{
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		Prerelease: prerelease,
		Text:       text,
		OK:         true,
	}, true
}

func parseNumber(spec string, start int) (int, int, bool) {
	index := start
	for index < len(spec) && isDigit(spec[index]) {
		index++
	}
	if index == start || index-start > maxVersionDigits {
		return 0, start, false
	}
	value, err := strconv.Atoi(spec[start:index])
	if err != nil {
		return 0, start, false
	}
	return value, index, true
}

func isDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isPrereleaseChar(value byte) bool {
	return isDigit(value) ||
		(value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		value == '.' || value == '-' || value == '+'
}

// CompareParsed orders two parsed versions. Unparseable values sort last.
func CompareParsed(a, b ParsedVersion) int {
	if a.OK && b.OK {
		return semver.Compare("v"+a.Text, "v"+b.Text)
	}
	switch {
	case a.OK:
		return 1
	case b.OK:
		return -1
	default:
		return strings.Compare(a.Text, b.Text)
	}
}

// exactVersion returns the canonical version text for a specifier, or the
// original specifier when no version can be extracted.
func exactVersion(spec string) (string, ParsedVersion) {
	parsed := ParseVersionSpec(spec)
	if !parsed.OK {
		return spec, parsed
	}
	return parsed.Text, parsed
}
