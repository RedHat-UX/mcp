// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Package lock reads, writes, and validates the pinned input lock for
// tools/gen-data.
package lock

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// SchemaVersion is the lock schema this build understands.
const SchemaVersion = 1

// Lock pins every input used to generate the embedded data tree.
type Lock struct {
	SchemaVersion int       `json:"schemaVersion"`
	Packages      []Package `json:"packages"`
	Docs          Docs      `json:"docs"`
}

// Package pins one npm package's stable releases.
type Package struct {
	// Name is the npm package name, for example "@rhds/elements".
	Name string `json:"name"`
	// ID is the short identifier used in the generated tree, for example
	// "elements".
	ID string `json:"id"`
	// Versions lists stable releases in ascending order.
	Versions []PackageVersion `json:"versions"`
}

// PackageVersion pins one npm tarball.
type PackageVersion struct {
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
	Tarball   string `json:"tarball"`
	Shasum    string `json:"shasum,omitempty"`
}

// Docs pins the documentation snapshots.
type Docs struct {
	Repo         string        `json:"repo"`
	AIGuidelines AIGuidelines  `json:"aiGuidelines"`
	Snapshots    []DocSnapshot `json:"snapshots"`
}

// AIGuidelines describes the docs/ai-guidelines submodule.
type AIGuidelines struct {
	Repo string `json:"repo"`
	URL  string `json:"url"`
}

// DocSnapshot pins one documentation tag and its submodule state.
type DocSnapshot struct {
	Tag                string `json:"tag"`
	Commit             string `json:"commit"`
	AIGuidelinesCommit string `json:"aiGuidelinesCommit,omitempty"`
}

// Load reads and validates a lock file.
func Load(path string) (*Lock, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lock
	if err := json.Unmarshal(encoded, &l); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if l.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf(
			"%s: unsupported lock schema version %d; this build understands %d",
			path, l.SchemaVersion, SchemaVersion)
	}
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &l, nil
}

// Save writes the lock as canonical JSON.
func Save(path string, l *Lock) error {
	encoded, err := l.Canonical()
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}

// Canonical returns deterministic JSON for the lock. Packages and versions are
// sorted so `make data-update` does not churn the file.
func (l *Lock) Canonical() ([]byte, error) {
	copy := l.clone()
	sort.Slice(copy.Packages, func(i, j int) bool {
		return copy.Packages[i].ID < copy.Packages[j].ID
	})
	for i := range copy.Packages {
		sort.Slice(copy.Packages[i].Versions, func(a, b int) bool {
			return compareVersions(copy.Packages[i].Versions[a].Version, copy.Packages[i].Versions[b].Version) < 0
		})
	}
	sort.Slice(copy.Docs.Snapshots, func(i, j int) bool {
		return compareVersions(copy.Docs.Snapshots[i].Tag, copy.Docs.Snapshots[j].Tag) < 0
	})
	encoded, err := json.MarshalIndent(copy, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// Digest returns the sha256 of the canonical lock bytes. The generated
// index.json records this value; `make data` is a no-op when it matches.
func (l *Lock) Digest() (string, error) {
	encoded, err := l.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// Validate checks that every required field is populated.
func (l *Lock) Validate() error {
	if l.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema version %d", l.SchemaVersion)
	}
	if len(l.Packages) == 0 {
		return errors.New("lock has no packages")
	}
	seenIDs := make(map[string]bool, len(l.Packages))
	for i, pkg := range l.Packages {
		if pkg.ID == "" {
			return fmt.Errorf("packages[%d] has no id", i)
		}
		if pkg.Name == "" {
			return fmt.Errorf("packages[%d] has no name", i)
		}
		if seenIDs[pkg.ID] {
			return fmt.Errorf("duplicate package id %q", pkg.ID)
		}
		seenIDs[pkg.ID] = true
		seenVersions := make(map[string]bool, len(pkg.Versions))
		for _, version := range pkg.Versions {
			if !semver.IsValid(versionTag(version.Version)) {
				return fmt.Errorf("%s version %q is not valid semver", pkg.ID, version.Version)
			}
			if semver.Prerelease(versionTag(version.Version)) != "" {
				return fmt.Errorf("%s version %q is a prerelease", pkg.ID, version.Version)
			}
			if version.Integrity == "" {
				return fmt.Errorf("%s@%s has no integrity hash", pkg.ID, version.Version)
			}
			if err := validateIntegrity(version.Integrity); err != nil {
				return fmt.Errorf("%s@%s has an invalid integrity hash: %w", pkg.ID, version.Version, err)
			}
			if version.Tarball == "" {
				return fmt.Errorf("%s@%s has no tarball URL", pkg.ID, version.Version)
			}
			if seenVersions[version.Version] {
				return fmt.Errorf("%s@%s is duplicated", pkg.ID, version.Version)
			}
			seenVersions[version.Version] = true
		}
	}
	if l.Docs.Repo == "" {
		return errors.New("lock has no docs repo")
	}
	seenTags := make(map[string]bool, len(l.Docs.Snapshots))
	for i, snapshot := range l.Docs.Snapshots {
		if snapshot.Tag == "" {
			return fmt.Errorf("docs.snapshots[%d] has no tag", i)
		}
		if !ValidDocTag(snapshot.Tag) {
			return fmt.Errorf("docs.snapshots[%d] has an invalid tag %q", i, snapshot.Tag)
		}
		if seenTags[snapshot.Tag] {
			return fmt.Errorf("docs snapshot tag %q is duplicated", snapshot.Tag)
		}
		seenTags[snapshot.Tag] = true
		if !isLowerHexSHA(snapshot.Commit) {
			return fmt.Errorf("docs.snapshots[%d] has an invalid commit sha %q", i, snapshot.Commit)
		}
		if snapshot.AIGuidelinesCommit != "" && !isLowerHexSHA(snapshot.AIGuidelinesCommit) {
			return fmt.Errorf(
				"docs.snapshots[%d] has an invalid ai-guidelines commit sha %q",
				i, snapshot.AIGuidelinesCommit)
		}
	}
	return nil
}

// validateIntegrity accepts the sha512 form npm publishes: "sha512-" followed
// by the standard base64 encoding of exactly 64 digest bytes.
func validateIntegrity(value string) error {
	const prefix = "sha512-"
	if !strings.HasPrefix(value, prefix) {
		return errors.New(`want a "sha512-" value`)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return err
	}
	if len(decoded) != sha512.Size {
		return fmt.Errorf("want %d digest bytes, got %d", sha512.Size, len(decoded))
	}
	return nil
}

func isLowerHexSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ValidDocTag accepts only release tags of the form vX.Y.Z. Docs tags become
// directory names in the generated tree, so separators are rejected too.
func ValidDocTag(tag string) bool {
	if !strings.HasPrefix(tag, "v") {
		return false
	}
	if strings.ContainsAny(tag, `/\`) || strings.Contains(tag, "..") {
		return false
	}
	if !semver.IsValid(tag) {
		return false
	}
	return semver.Prerelease(tag) == "" && semver.Canonical(tag) == tag
}

// PackageByID returns a pinned package by its short ID.
func (l *Lock) PackageByID(id string) (Package, bool) {
	for _, pkg := range l.Packages {
		if pkg.ID == id {
			return pkg, true
		}
	}
	return Package{}, false
}

func (l *Lock) clone() *Lock {
	copy := &Lock{SchemaVersion: l.SchemaVersion, Docs: l.Docs}
	copy.Packages = make([]Package, len(l.Packages))
	for i, pkg := range l.Packages {
		copy.Packages[i] = pkg
		copy.Packages[i].Versions = append([]PackageVersion(nil), pkg.Versions...)
	}
	copy.Docs.Snapshots = append([]DocSnapshot(nil), l.Docs.Snapshots...)
	return copy
}

func versionTag(version string) string {
	return "v" + strings.TrimPrefix(version, "v")
}

func compareVersions(a, b string) int {
	ta, tb := versionTag(a), versionTag(b)
	if semver.IsValid(ta) && semver.IsValid(tb) {
		return semver.Compare(ta, tb)
	}
	return strings.Compare(a, b)
}

// StableVersions filters out prereleases and sorts ascending.
func StableVersions(versions []string) []string {
	stable := make([]string, 0, len(versions))
	for _, version := range versions {
		if semver.IsValid(versionTag(version)) && semver.Prerelease(versionTag(version)) == "" {
			stable = append(stable, version)
		}
	}
	sort.Slice(stable, func(i, j int) bool {
		return semver.Compare(versionTag(stable[i]), versionTag(stable[j])) < 0
	})
	return stable
}

// HighestSatisfying returns the highest version that satisfies an npm-style
// range. The RHDS packages currently use caret ranges; exact, tilde,
// comparator, wildcard, and hyphen ranges are supported so a future release
// range does not require a code change.
func HighestSatisfying(rangeSpec string, versions []string) (string, bool) {
	best := ""
	for _, version := range versions {
		if !satisfies(version, rangeSpec) {
			continue
		}
		if best == "" || compareVersions(version, best) > 0 {
			best = version
		}
	}
	return best, best != ""
}

func satisfies(version, rangeSpec string) bool {
	v := versionTag(strings.TrimSpace(version))
	if !semver.IsValid(v) {
		return false
	}
	for _, alternative := range strings.Split(rangeSpec, "||") {
		if satisfiesAlternative(v, strings.TrimSpace(alternative)) {
			return true
		}
	}
	return false
}

func satisfiesAlternative(v, spec string) bool {
	if spec == "" || spec == "*" || spec == "x" {
		return true
	}
	if strings.Contains(spec, " - ") {
		parts := strings.SplitN(spec, " - ", 2)
		low, lok := lowerBound(strings.TrimSpace(parts[0]))
		high, inclusive, hok := hyphenUpperBound(strings.TrimSpace(parts[1]))
		if !lok || !hok {
			return false
		}
		if inclusive {
			return semver.Compare(v, low) >= 0 && semver.Compare(v, high) <= 0
		}
		return semver.Compare(v, low) >= 0 && semver.Compare(v, high) < 0
	}
	fields := mergeComparatorOperators(strings.Fields(spec))
	if len(fields) == 0 {
		return true
	}
	for _, field := range fields {
		if !satisfiesComparator(v, field) {
			return false
		}
	}
	return true
}

// mergeComparatorOperators joins an operator split from its operand by
// whitespace, as npm permits `>= 1.0.0`. Without this, the operator becomes a
// field of its own and matches nothing.
func mergeComparatorOperators(fields []string) []string {
	operators := map[string]bool{
		"<": true, "<=": true, ">": true, ">=": true, "=": true,
		"^": true, "~": true,
	}
	merged := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if operators[field] && i+1 < len(fields) {
			merged = append(merged, field+fields[i+1])
			i++
			continue
		}
		merged = append(merged, field)
	}
	return merged
}

func satisfiesComparator(v, comparator string) bool {
	switch {
	case strings.HasPrefix(comparator, "^"):
		low, ok := lowerBound(strings.TrimPrefix(comparator, "^"))
		if !ok {
			return false
		}
		high, ok := caretUpper(strings.TrimPrefix(comparator, "^"))
		if !ok {
			return false
		}
		return semver.Compare(v, low) >= 0 && semver.Compare(v, high) < 0
	case strings.HasPrefix(comparator, "~"):
		spec := strings.TrimPrefix(comparator, "~")
		low, ok := lowerBound(spec)
		if !ok {
			return false
		}
		high, ok := tildeUpper(spec)
		if !ok {
			return false
		}
		return semver.Compare(v, low) >= 0 && semver.Compare(v, high) < 0
	case strings.HasPrefix(comparator, ">="):
		low, ok := lowerBound(comparator[2:])
		return ok && semver.Compare(v, low) >= 0
	case strings.HasPrefix(comparator, "<="):
		spec := comparator[2:]
		if isPartialVersion(spec) {
			next, ok := nextPartialBound(spec)
			return ok && semver.Compare(v, next) < 0
		}
		high, ok := lowerBound(spec)
		return ok && semver.Compare(v, high) <= 0
	case strings.HasPrefix(comparator, ">"):
		spec := comparator[1:]
		if isPartialVersion(spec) {
			next, ok := nextPartialBound(spec)
			return ok && semver.Compare(v, next) >= 0
		}
		low, ok := lowerBound(spec)
		return ok && semver.Compare(v, low) > 0
	case strings.HasPrefix(comparator, "<"):
		high, ok := lowerBound(comparator[1:])
		return ok && semver.Compare(v, high) < 0
	case strings.HasPrefix(comparator, "="):
		spec := comparator[1:]
		if isPartialVersion(spec) {
			low, ok := lowerBound(spec)
			if !ok {
				return false
			}
			high, ok := nextPartialBound(spec)
			return ok && semver.Compare(v, low) >= 0 && semver.Compare(v, high) < 0
		}
		low, ok := lowerBound(spec)
		return ok && semver.Compare(v, low) == 0
	default:
		return comparePartial(v, comparator)
	}
}

// comparePartial handles partial versions such as "1", "1.2", "1.x", and
// "1.2.x" that do not have a single lower bound.
func comparePartial(v, spec string) bool {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(spec), "v"), ".")
	versionParts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	for i, part := range parts {
		if part == "x" || part == "X" || part == "*" {
			return true
		}
		if i >= len(versionParts) || versionParts[i] != part {
			return false
		}
	}
	return len(versionParts) >= len(parts)
}

func lowerBound(spec string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(spec), "v"), ".")
	if len(parts) == 0 || parts[0] == "" {
		return "", false
	}
	numbers := make([]int, 3)
	for i := 0; i < 3; i++ {
		if i >= len(parts) || parts[i] == "x" || parts[i] == "X" || parts[i] == "*" {
			numbers[i] = 0
			continue
		}
		value, err := strconv.Atoi(parts[i])
		if err != nil {
			return "", false
		}
		numbers[i] = value
	}
	return fmt.Sprintf("v%d.%d.%d", numbers[0], numbers[1], numbers[2]), true
}

func caretUpper(spec string) (string, bool) {
	parts := splitVersion(spec)
	numbers, ok := numericParts(parts)
	if !ok {
		return "", false
	}
	switch {
	case numbers[0] > 0:
		return fmt.Sprintf("v%d.0.0", numbers[0]+1), true
	case len(parts) < 2 || isWildcard(parts[1]):
		// ^0 -> <1.0.0
		return "v1.0.0", true
	case numbers[1] > 0:
		return fmt.Sprintf("v0.%d.0", numbers[1]+1), true
	case len(parts) < 3 || isWildcard(parts[2]):
		// ^0.0 -> <0.1.0
		return "v0.1.0", true
	default:
		return fmt.Sprintf("v0.0.%d", numbers[2]+1), true
	}
}

func tildeUpper(spec string) (string, bool) {
	parts := splitVersion(spec)
	if len(parts) == 0 || parts[0] == "" {
		return "", false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", false
	}
	if len(parts) == 1 || isWildcard(parts[1]) {
		return fmt.Sprintf("v%d.0.0", major+1), true
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("v%d.%d.0", major, minor+1), true
}

// hyphenUpperBound returns the upper bound of a hyphen range. An exact upper
// version is inclusive; a partial upper version (for example "2.3") means the
// whole 2.3.x line, so the exclusive bound is 2.4.0.
func hyphenUpperBound(spec string) (string, bool, bool) {
	if !isPartialVersion(spec) {
		bound, ok := lowerBound(spec)
		return bound, true, ok
	}
	bound, ok := nextPartialBound(spec)
	return bound, false, ok
}

func isPartialVersion(spec string) bool {
	parts := splitVersion(spec)
	if len(parts) < 3 {
		return true
	}
	for _, part := range parts {
		if isWildcard(part) {
			return true
		}
	}
	return false
}

// nextPartialBound returns the first version after the partial range:
// 1 -> 2.0.0, 1.2 -> 1.3.0, 1.2.x -> 1.3.0.
func nextPartialBound(spec string) (string, bool) {
	parts := splitVersion(spec)
	numbers, ok := numericParts(parts)
	if !ok {
		return "", false
	}
	if len(parts) < 2 || isWildcard(parts[1]) {
		return fmt.Sprintf("v%d.0.0", numbers[0]+1), true
	}
	return fmt.Sprintf("v%d.%d.0", numbers[0], numbers[1]+1), true
}

func splitVersion(spec string) []string {
	return strings.Split(strings.TrimPrefix(strings.TrimSpace(spec), "v"), ".")
}

func numericParts(parts []string) ([3]int, bool) {
	var numbers [3]int
	for i := 0; i < 3; i++ {
		if i >= len(parts) || isWildcard(parts[i]) {
			numbers[i] = 0
			continue
		}
		value, err := strconv.Atoi(parts[i])
		if err != nil {
			return numbers, false
		}
		numbers[i] = value
	}
	return numbers, true
}

func isWildcard(part string) bool {
	return part == "x" || part == "X" || part == "*"
}
