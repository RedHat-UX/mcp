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

// ParsedVersion is the first semantic version found in a specifier. Partial
// is true for ranges such as "^4", "4", "4.x", and "~4.2"; those identify a
// major (and optionally a minor) but not an exact bundled version.
type ParsedVersion struct {
	Major      int
	Minor      int
	Patch      int
	HasMinor   bool
	Prerelease string
	Text       string
	Partial    bool
	OK         bool
}

// maxVersionDigits bounds each numeric component so malformed or hostile
// version strings cannot overflow strconv.Atoi.
const maxVersionDigits = 9

// maxSpecScan bounds the bytes inspected for a version. Package specifiers are
// short; a multi-megabyte lockfile value must not cause a long scan.
const maxSpecScan = 4096

// ParseVersionSpec extracts the first x.y.z semantic version from a package
// specifier. It accepts ranges, npm aliases, workspace protocol prefixes, and
// CDN URLs. When no exact version is present it accepts partial npm ranges
// such as "^4", "4", "4.x", and "~4.2". It rejects non-version specifiers such
// as "latest" and "*".
func ParseVersionSpec(spec string) ParsedVersion {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ParsedVersion{}
	}
	scan := spec
	if len(scan) > maxSpecScan {
		scan = scan[:maxSpecScan]
	}
	for i := 0; i < len(scan); i++ {
		if !isDigit(scan[i]) {
			continue
		}
		if parsed, ok := parseVersionAt(scan, i); ok {
			return parsed
		}
	}
	return parsePartialSpec(spec)
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
		HasMinor:   true,
		Prerelease: prerelease,
		Text:       text,
		OK:         true,
	}, true
}

// parsePartialSpec accepts major-only and major.minor npm ranges. It requires
// the whole (trimmed) specifier to be version-like, so a stray digit in prose
// or a URL path is not mistaken for a version.
func parsePartialSpec(spec string) ParsedVersion {
	text := strings.TrimSpace(spec)
	if len(text) > maxSpecScan {
		text = text[:maxSpecScan]
	}
	text = strings.TrimPrefix(text, "workspace:")
	text = strings.TrimPrefix(text, "npm:")
	if index := strings.LastIndex(text, "@rhds/elements@"); index >= 0 {
		text = text[index+len("@rhds/elements@"):]
	}
	text = strings.TrimLeft(text, "^~><=| ")
	if text == "" {
		return ParsedVersion{}
	}
	if text[0] == 'v' || text[0] == 'V' {
		text = text[1:]
	}
	major, index, ok := parseNumber(text, 0)
	if !ok {
		return ParsedVersion{}
	}
	rest := text[index:]
	if rest == "" {
		return partialVersion(major, 0, false, strconv.Itoa(major))
	}
	if rest[0] != '.' {
		return ParsedVersion{}
	}
	rest = rest[1:]
	if rest == "x" || rest == "X" || rest == "*" {
		return partialVersion(major, 0, false, strconv.Itoa(major))
	}
	minor, minorEnd, ok := parseNumber(rest, 0)
	if !ok {
		return ParsedVersion{}
	}
	rest = rest[minorEnd:]
	if rest == "" || rest == ".x" || rest == ".X" || rest == ".*" {
		return partialVersion(major, minor, true,
			strconv.Itoa(major)+"."+strconv.Itoa(minor))
	}
	return ParsedVersion{}
}

func partialVersion(major, minor int, hasMinor bool, text string) ParsedVersion {
	return ParsedVersion{
		Major:    major,
		Minor:    minor,
		HasMinor: hasMinor,
		Text:     text,
		Partial:  true,
		OK:       true,
	}
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

// CompareParsed orders two parsed versions. Partial ranges compare by their
// numeric components; full versions with equal components use semver so
// prerelease ordering is preserved. Unparseable values sort last.
func CompareParsed(a, b ParsedVersion) int {
	if a.OK && b.OK {
		if order := compareInt(a.Major, b.Major); order != 0 {
			return order
		}
		if order := compareInt(a.Minor, b.Minor); order != 0 {
			return order
		}
		if order := compareInt(a.Patch, b.Patch); order != 0 {
			return order
		}
		if a.Partial || b.Partial {
			return 0
		}
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

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// exactVersion returns the canonical version text for a specifier, or the
// original specifier when no version can be extracted. For a partial range the
// text is the major or major.minor; selection maps it to a bundled version.
func exactVersion(spec string) (string, ParsedVersion) {
	parsed := ParseVersionSpec(spec)
	if !parsed.OK {
		return spec, parsed
	}
	return parsed.Text, parsed
}

// truncateRunes shortens untrusted strings before they reach a candidate or a
// tool response.
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}
