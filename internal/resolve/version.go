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
	// Operator is the npm range operator preceding the version: ^, ~, >, >=,
	// <, <=, or =. An empty operator means an exact version or a bare partial.
	Operator string
	Partial  bool
	OK       bool
}

// maxVersionDigits bounds each numeric component so malformed or hostile
// version strings cannot overflow strconv.Atoi.
const maxVersionDigits = 9

// maxSpecScan bounds the bytes inspected for a version. Package specifiers are
// short; a multi-megabyte lockfile value must not cause a long scan.
const maxSpecScan = 4096

// ParseVersionSpec extracts the first x.y.z semantic version from a package
// specifier. It accepts plain semver, npm ranges, v-prefixed tags, and npm:
// aliases. Specifiers that name a local path or a remote artifact (file:,
// link:, workspace:, git, http, and https) are not version constraints and are
// rejected. When no exact version is present it accepts partial npm ranges such
// as "^4", "4", "4.x", and "~4.2". It rejects non-version specifiers such as
// "latest" and "*".
func ParseVersionSpec(spec string) ParsedVersion {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ParsedVersion{}
	}
	scan, ok := versionSpecText(spec)
	if !ok {
		return ParsedVersion{}
	}
	if len(scan) > maxSpecScan {
		scan = scan[:maxSpecScan]
	}
	for i := 0; i < len(scan); i++ {
		if !isDigit(scan[i]) {
			continue
		}
		if parsed, ok := parseVersionAt(scan, i); ok {
			parsed.Operator = operatorBefore(scan, i)
			if parsed.Operator != "" && parsed.Operator != "=" {
				// A range pins an operator, not an exact bundled version.
				parsed.Partial = true
			}
			return parsed
		}
	}
	return parsePartialSpec(scan)
}

// versionSpecText returns the portion of spec that may hold a version. It
// rejects protocol specifiers outright: a version-like substring in a local
// path or remote URL is not dependency evidence.
func versionSpecText(spec string) (string, bool) {
	if strings.HasPrefix(strings.ToLower(spec), "npm:") {
		rest := strings.TrimSpace(spec[len("npm:"):])
		if rest == "" {
			return "", false
		}
		// Yarn Berry records a resolved package as "npm:4.2.2", while an npm
		// alias is "npm:pkg@^4". A version-shaped remainder is already the
		// value to parse.
		if looksLikeVersionText(rest) {
			return rest, true
		}
		at := strings.LastIndex(rest, "@")
		if at <= 0 {
			return "", false
		}
		candidate := strings.TrimSpace(rest[at+1:])
		if candidate == "" || hasNonVersionProtocol(candidate) {
			return "", false
		}
		return candidate, true
	}
	if hasNonVersionProtocol(spec) {
		return "", false
	}
	return spec, true
}

func looksLikeVersionText(text string) bool {
	if text == "" {
		return false
	}
	switch text[0] {
	case 'v', 'V', '^', '~', '>', '<', '=', '*':
		return true
	default:
		return isDigit(text[0])
	}
}

func hasNonVersionProtocol(spec string) bool {
	// A plain version, range, or tag never contains a path separator or a
	// fragment. This also rejects GitHub shorthand such as user/repo#v4.2.2.
	if strings.ContainsAny(spec, "/#") {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(spec))
	for _, prefix := range []string{
		"file:", "link:", "workspace:", "portal:", "patch:",
		"git:", "git+", "git@", "github:", "gitlab:", "bitbucket:",
		"http:", "https:", "ssh:",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if index := strings.IndexByte(spec, ':'); index > 0 {
		return isSchemeToken(spec[:index])
	}
	return false
}

func isSchemeToken(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		value := text[i]
		switch {
		case value >= 'a' && value <= 'z',
			value >= 'A' && value <= 'Z',
			value >= '0' && value <= '9',
			value == '+', value == '-', value == '.':
		default:
			return false
		}
	}
	first := text[0]
	return (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')
}

// operatorBefore returns the range operator immediately preceding the version
// at start, ignoring spaces.
func operatorBefore(spec string, start int) string {
	i := start - 1
	for i >= 0 && (spec[i] == ' ' || spec[i] == '\t') {
		i--
	}
	if i < 0 {
		return ""
	}
	switch spec[i] {
	case '^', '~', '>', '<':
		return string(spec[i])
	case '=':
		if i > 0 && (spec[i-1] == '>' || spec[i-1] == '<') {
			return string(spec[i-1]) + "="
		}
		return "="
	default:
		return ""
	}
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

	prerelease := ""
	if index < len(spec) && spec[index] == '-' {
		preEnd := index + 1
		for preEnd < len(spec) && isVersionIdentifierChar(spec[preEnd]) {
			preEnd++
		}
		if preEnd == index+1 {
			return ParsedVersion{}, false
		}
		prerelease = spec[index+1 : preEnd]
		if !validPrerelease(prerelease) {
			return ParsedVersion{}, false
		}
		index = preEnd
	}
	if index < len(spec) && spec[index] == '+' {
		buildEnd := index + 1
		for buildEnd < len(spec) && isVersionIdentifierChar(spec[buildEnd]) {
			buildEnd++
		}
		if buildEnd == index+1 {
			return ParsedVersion{}, false
		}
		if !validBuildMetadata(spec[index+1 : buildEnd]) {
			return ParsedVersion{}, false
		}
		index = buildEnd
		// SemVer has exactly one +; a second one starts a malformed value.
		if index < len(spec) && spec[index] == '+' {
			return ParsedVersion{}, false
		}
	}

	return ParsedVersion{
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		HasMinor:   true,
		Prerelease: prerelease,
		Text:       spec[start:index],
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
	if index := strings.LastIndex(text, "@rhds/elements@"); index >= 0 {
		text = text[index+len("@rhds/elements@"):]
	}
	text = strings.TrimLeft(text, " ")
	operator := ""
	if text != "" {
		j := 0
		switch text[0] {
		case '^', '~', '=':
			j = 1
		case '>', '<':
			j = 1
			if j < len(text) && text[j] == '=' {
				j++
			}
		}
		if j > 0 {
			operator = text[:j]
			text = strings.TrimLeft(text[j:], " ")
		}
	}
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
		return partialVersion(major, 0, false, strconv.Itoa(major), operator)
	}
	if rest[0] != '.' {
		return ParsedVersion{}
	}
	rest = rest[1:]
	if rest == "x" || rest == "X" || rest == "*" {
		return partialVersion(major, 0, false, strconv.Itoa(major), operator)
	}
	minor, minorEnd, ok := parseNumber(rest, 0)
	if !ok {
		return ParsedVersion{}
	}
	rest = rest[minorEnd:]
	if rest == "" || rest == ".x" || rest == ".X" || rest == ".*" {
		return partialVersion(major, minor, true,
			strconv.Itoa(major)+"."+strconv.Itoa(minor), operator)
	}
	return ParsedVersion{}
}

func partialVersion(major, minor int, hasMinor bool, text, operator string) ParsedVersion {
	return ParsedVersion{
		Major:    major,
		Minor:    minor,
		HasMinor: hasMinor,
		Text:     text,
		Operator: operator,
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

func isVersionIdentifierChar(value byte) bool {
	return isDigit(value) ||
		(value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		value == '.' || value == '-'
}

// validPrerelease enforces SemVer 2.0.0 prerelease rules: dot-separated,
// non-empty identifiers of alphanumerics and hyphens. A purely numeric
// identifier must not have a leading zero.
func validPrerelease(text string) bool {
	return validIdentifiers(text, true)
}

// validBuildMetadata enforces SemVer 2.0.0 build metadata rules. Numeric
// identifiers may have leading zeros, and a value never participates in
// comparison ordering.
func validBuildMetadata(text string) bool {
	return validIdentifiers(text, false)
}

func validIdentifiers(text string, prerelease bool) bool {
	for _, identifier := range strings.Split(text, ".") {
		if identifier == "" {
			return false
		}
		allDigits := true
		for i := 0; i < len(identifier); i++ {
			value := identifier[i]
			if !isDigit(value) {
				allDigits = false
			}
			if !isVersionIdentifierChar(value) {
				return false
			}
		}
		if prerelease && allDigits && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
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
