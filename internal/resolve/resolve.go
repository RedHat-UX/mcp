// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Package resolve implements the version cascade described in docs/PLAN.md
// section 6. Every workspace read is bounded and workspace content is treated
// as evidence, never as instructions. Symlinks are resolved before reading so
// a path can never escape the workspace root; symlinks that resolve inside the
// root are followed when reading package metadata, while template scanning
// skips symlink entries deliberately.
package resolve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Candidate is one piece of workspace evidence for the @rhds/elements
// version. Version is the exact version extracted from Spec when possible;
// otherwise it is the raw specifier and cannot be selected.
type Candidate struct {
	Version    string `json:"version"`
	Spec       string `json:"spec,omitempty"`
	Source     string `json:"source"`
	Precedence int    `json:"precedence"`
	Level      string `json:"level"`
	// Truncated reports that a field was shortened by a fixed cap. Callers
	// warn when this happens in a response payload.
	Truncated bool `json:"truncated,omitempty"`
}

// Ambiguity reports candidates at the winning precedence level that disagree
// on the @rhds/elements major.
type Ambiguity struct {
	Level      string      `json:"level"`
	Candidates []Candidate `json:"candidates"`
	Majors     []int       `json:"majors"`
}

// Result is the outcome of the cascade. Elements is empty when Ambiguity is
// non-nil and needs a decision.
type Result struct {
	Elements   string      `json:"elements,omitempty"`
	Source     string      `json:"source,omitempty"`
	Candidates []Candidate `json:"candidates"`
	Ambiguity  *Ambiguity  `json:"ambiguity,omitempty"`
	Warnings   []string    `json:"warnings,omitempty"`
}

// Options configures Resolve.
type Options struct {
	// Root is the workspace root. When empty, the process working directory is
	// used.
	Root string
	// Explicit is the version argument. When set, workspace reads are skipped.
	Explicit string
	// BundledElements are the stable @rhds/elements versions available in the
	// binary, in any order.
	BundledElements []string
	// Limits bounds workspace reads. Zero values use DefaultLimits.
	Limits Limits
}

// ErrInvalidRoot reports a workspace root that is not a readable directory.
var ErrInvalidRoot = errors.New("invalid workspace root")

// ErrNoBundledElements reports a binary with no bundled elements data.
var ErrNoBundledElements = errors.New("no bundled elements versions")

// lockfileNames are read in this order. They share one precedence level, so
// the order only affects reporting, never the first-hit rule.
var lockfileNames = []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock"}

// Resolve runs the version cascade.
func Resolve(options Options) (Result, error) {
	limits := options.Limits.withDefaults()
	var result Result

	if options.Explicit != "" {
		version := options.Explicit
		parsed := ParseVersionSpec(options.Explicit)
		switch {
		case !parsed.OK:
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("the version argument %q is not a semantic version", truncateRunes(options.Explicit, maxCandidateRunes)))
		case parsed.Partial:
			if resolved, ok := highestBundledFor(parsed, options.BundledElements); ok {
				version = resolved
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("version argument %q resolved to the highest bundled matching version %s",
						truncateRunes(options.Explicit, maxCandidateRunes), resolved))
			} else {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("no bundled elements version matches the version argument %q",
						truncateRunes(options.Explicit, maxCandidateRunes)))
			}
		default:
			version = parsed.Text
		}
		candidate := Candidate{
			Version:    truncateRunes(version, maxCandidateRunes),
			Spec:       truncateRunes(options.Explicit, maxCandidateRunes),
			Source:     "version argument",
			Precedence: PrecedenceExplicit,
			Level:      LevelExplicit,
		}
		result.Elements = candidate.Version
		result.Source = "version argument"
		result.Candidates = []Candidate{candidate}
		return result, nil
	}

	root := options.Root
	if root == "" {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return result, fmt.Errorf("%w: %v", ErrInvalidRoot, err)
		}
		root = workingDirectory
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidRoot, err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidRoot, err)
	}
	if !info.IsDir() {
		return result, fmt.Errorf("%w: %s is not a directory", ErrInvalidRoot, absoluteRoot)
	}
	root = absoluteRoot

	reader := newWorkspaceReader(root, limits)
	set := newCandidateSet()

	for _, name := range lockfileNames {
		payload, err := reader.read(name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("could not read %s: %v", name, err))
			continue
		}
		candidates, truncated, err := parseLockfileData(name, payload, limits)
		if err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("could not parse %s: %v", name, err))
			continue
		}
		if truncated {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("candidate collection for %s stopped at %d entries", name, maxCandidates))
		}
		mergeCandidates(set, candidates)
	}

	workspaceDirectories := collectPackageJSON(root, reader, set, &result.Warnings)
	collectInstalledPackages(root, reader, workspaceDirectories, set, &result.Warnings)
	collectTemplateCandidates(root, reader, set, &result.Warnings)
	if set.truncated {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("candidate collection stopped at %d entries", maxCandidates))
	}

	result.Candidates = set.sorted()
	sortCandidates(result.Candidates)

	if len(result.Candidates) == 0 {
		return defaultResult(result, options.BundledElements)
	}
	return selectCandidate(result, options.BundledElements)
}

func mergeCandidates(set *candidateSet, candidates []Candidate) {
	for _, candidate := range candidates {
		set.add(candidate.Version, candidate.Spec, candidate.Source, candidate.Precedence)
	}
}

func defaultResult(result Result, bundled []string) (Result, error) {
	latest, ok := latestBundled(bundled)
	if !ok {
		return result, ErrNoBundledElements
	}
	candidate := Candidate{
		Version:    latest,
		Source:     "bundled",
		Precedence: PrecedenceBundled,
		Level:      LevelBundled,
	}
	result.Elements = latest
	result.Source = "bundled"
	result.Candidates = append(result.Candidates, candidate)
	result.Warnings = append(result.Warnings,
		fmt.Sprintf("no workspace version evidence found; using latest bundled elements@%s", latest))
	return result, nil
}

func latestBundled(bundled []string) (string, bool) {
	type parsed struct {
		text   string
		parsed ParsedVersion
	}
	versions := make([]parsed, 0, len(bundled))
	for _, version := range bundled {
		value := ParseVersionSpec(version)
		if !value.OK {
			continue
		}
		versions = append(versions, parsed{text: value.Text, parsed: value})
	}
	if len(versions) == 0 {
		return "", false
	}
	sort.Slice(versions, func(i, j int) bool {
		return CompareParsed(versions[i].parsed, versions[j].parsed) > 0
	})
	return versions[0].text, true
}

func selectCandidate(result Result, bundled []string) (Result, error) {
	levels := make([]int, 0)
	seen := make(map[int]bool, len(result.Candidates))
	for _, candidate := range result.Candidates {
		if !seen[candidate.Precedence] {
			seen[candidate.Precedence] = true
			levels = append(levels, candidate.Precedence)
		}
	}
	sort.Ints(levels)
	for _, level := range levels {
		top := make([]Candidate, 0, len(result.Candidates))
		for _, candidate := range result.Candidates {
			if candidate.Precedence == level {
				top = append(top, candidate)
			}
		}
		if selected, ok := selectFromLevel(result, top, level, bundled); ok {
			return selected, nil
		}
	}
	return defaultResult(result, bundled)
}

// selectFromLevel chooses a version from the candidates at one precedence
// level. It reports false when the level has no parseable candidate, so the
// caller can fall back to lower-precedence evidence instead of dropping it.
func selectFromLevel(result Result, top []Candidate, winning int, bundled []string) (Result, bool) {
	majors := make(map[int]bool)
	type parsedCandidate struct {
		candidate    Candidate
		parsed       ParsedVersion
		resolvedText string
	}
	parsedTop := make([]parsedCandidate, 0, len(top))
	for _, candidate := range top {
		parsed := ParseVersionSpec(candidate.Version)
		if !parsed.OK {
			parsed = ParseVersionSpec(candidate.Spec)
		}
		// A package.json spec can carry a range operator while the candidate
		// version is the bare version extracted from it. Honor the operator so
		// ^4.2.0 resolves to the highest 4.x rather than pinning 4.2.0. A
		// lockfile or installed package records an exact resolved version, so
		// its version wins over the range key.
		if candidate.Level != LevelLockfile && candidate.Level != LevelInstalled {
			if spec := ParseVersionSpec(candidate.Spec); spec.OK && spec.Operator != "" {
				parsed = spec
			}
		}
		if !parsed.OK {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("could not parse a version from %q in %s",
					truncateRunes(candidate.Version, maxCandidateRunes), candidate.Source))
			continue
		}
		majors[parsed.Major] = true
		resolvedText := parsed.Text
		if parsed.Partial {
			var ok bool
			resolvedText, ok = highestBundledFor(parsed, bundled)
			if !ok {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("no bundled elements version matches %q in %s",
						truncateRunes(candidate.Spec, maxCandidateRunes), candidate.Source))
				continue
			}
		}
		parsedTop = append(parsedTop, parsedCandidate{
			candidate:    candidate,
			parsed:       parsed,
			resolvedText: resolvedText,
		})
	}

	if len(majors) > 1 {
		result.Ambiguity = &Ambiguity{
			Level:      LevelForPrecedence(winning),
			Candidates: top,
			Majors:     sortedMajors(majors),
		}
		result.Elements = ""
		result.Source = LevelForPrecedence(winning)
		return result, true
	}
	if len(parsedTop) == 0 {
		return result, false
	}

	sort.Slice(parsedTop, func(i, j int) bool {
		left := ParseVersionSpec(parsedTop[i].resolvedText)
		right := ParseVersionSpec(parsedTop[j].resolvedText)
		if order := CompareParsed(left, right); order != 0 {
			return order > 0
		}
		if parsedTop[i].candidate.Source != parsedTop[j].candidate.Source {
			return parsedTop[i].candidate.Source < parsedTop[j].candidate.Source
		}
		return parsedTop[i].candidate.Spec < parsedTop[j].candidate.Spec
	})
	chosen := parsedTop[0]
	result.Elements = chosen.resolvedText
	result.Source = chosen.candidate.Source

	distinct := make(map[string]bool)
	for _, item := range parsedTop {
		distinct[item.resolvedText] = true
	}
	if len(distinct) > 1 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("workspace sources disagree on a minor or patch version; using highest %s from %s",
				chosen.resolvedText, chosen.candidate.Source))
	}
	addLowerPrecedenceWarning(&result, chosen.parsed, chosen.resolvedText, winning)
	return result, true
}

// highestBundledFor maps a partial range to the highest bundled version that
// satisfies it. Ranges honor their operator; a bare partial ("4" or "4.2")
// means the highest bundled version in that major or major.minor line.
func highestBundledFor(parsed ParsedVersion, bundled []string) (string, bool) {
	best := ""
	var bestParsed ParsedVersion
	for _, version := range bundled {
		candidate := ParseVersionSpec(version)
		if !candidate.OK || candidate.Partial || !satisfiesOperator(candidate, parsed) {
			continue
		}
		if best == "" || CompareParsed(candidate, bestParsed) > 0 {
			best = candidate.Text
			bestParsed = candidate
		}
	}
	return best, best != ""
}

func satisfiesOperator(available, target ParsedVersion) bool {
	order := CompareParsed(available, target)
	switch target.Operator {
	case "", "=":
		if target.Partial {
			if available.Major != target.Major {
				return false
			}
			return !target.HasMinor || available.Minor == target.Minor
		}
		return order == 0
	case "^":
		return available.Major == target.Major && order >= 0
	case "~":
		if available.Major != target.Major || order < 0 {
			return false
		}
		// ~4 means the whole 4.x line; ~4.2 means only 4.2.x.
		return !target.HasMinor || available.Minor == target.Minor
	case ">=":
		return order >= 0
	case ">":
		return order > 0
	case "<=":
		return order <= 0
	case "<":
		return order < 0
	default:
		return false
	}
}

func addLowerPrecedenceWarning(result *Result, chosen ParsedVersion, chosenText string, winning int) {
	seen := make(map[string]bool)
	for _, candidate := range result.Candidates {
		if candidate.Precedence <= winning || seen[candidate.Source] {
			continue
		}
		parsed := ParseVersionSpec(candidate.Version)
		if !parsed.OK {
			parsed = ParseVersionSpec(candidate.Spec)
		}
		if !parsed.OK || parsed.Major == chosen.Major {
			continue
		}
		seen[candidate.Source] = true
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("ignored %s in %s because a higher-precedence source selected elements@%s",
				truncateRunes(candidate.Version, maxCandidateRunes), candidate.Source, chosenText))
	}
}

func sortedMajors(majors map[int]bool) []int {
	result := make([]int, 0, len(majors))
	for major := range majors {
		result = append(result, major)
	}
	sort.Ints(result)
	return result
}

func sortCandidates(candidates []Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Precedence != candidates[j].Precedence {
			return candidates[i].Precedence < candidates[j].Precedence
		}
		left := ParseVersionSpec(candidates[i].Version)
		right := ParseVersionSpec(candidates[j].Version)
		if left.OK && right.OK {
			if order := CompareParsed(left, right); order != 0 {
				return order > 0
			}
		} else if left.OK != right.OK {
			return left.OK
		}
		if candidates[i].Source != candidates[j].Source {
			return candidates[i].Source < candidates[j].Source
		}
		return strings.Compare(candidates[i].Version, candidates[j].Version) < 0
	})
}
