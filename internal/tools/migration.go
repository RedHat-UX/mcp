// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"bennypowers.dev/cem/breaking"
	"bennypowers.dev/cem/manifest"
	"golang.org/x/mod/semver"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/data"
	"github.com/redhat-ux/mcp/internal/markdown"
)

// MigrationGuideInput is the input for rhds_get_migration_guide.
type MigrationGuideInput struct {
	FromVersion   string   `json:"fromVersion" jsonschema:"Starting @rhds/elements version"`
	ToVersion     string   `json:"toVersion,omitempty" jsonschema:"Target @rhds/elements version; defaults to the latest bundled stable version"`
	Element       string   `json:"element,omitempty" jsonschema:"Optional custom element tag name to limit the guide to"`
	IncludeTokens *bool    `json:"includeTokens,omitempty" jsonschema:"Include cross-major token renames and deprecations; default true"`
	Detail        string   `json:"detail,omitempty" jsonschema:"Detail level: summary (default, 8k budget) or full (32k budget)"`
	Sections      []string `json:"sections,omitempty" jsonschema:"Categories to include: changes, releaseNotes, tokens; defaults to all"`
	Limit         int      `json:"limit,omitempty" jsonschema:"Maximum entries per category; zero uses the response budget, maximum 200"`
}

// MigrationChange is one CEM breaking change.
type MigrationChange struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Element  string `json:"element"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}

// ReleaseNoteExample is one Before/After pair from a release note.
type ReleaseNoteExample struct {
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
	Element string `json:"element,omitempty"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Text    string `json:"text,omitempty"`
	Source  string `json:"source"`
}

// TokenChange is one cross-major token rename, removal, or deprecation.
type TokenChange struct {
	Action      string   `json:"action"`
	From        string   `json:"from"`
	To          string   `json:"to,omitempty"`
	Variants    []string `json:"variants,omitempty"`
	Since       string   `json:"since,omitempty"`
	Replacement string   `json:"replacement,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// MigrationGuideOutput is the output for rhds_get_migration_guide.
type MigrationGuideOutput struct {
	Resolved          ResolvedVersion      `json:"resolved"`
	FromVersion       string               `json:"fromVersion"`
	ToVersion         string               `json:"toVersion"`
	Changes           []MigrationChange    `json:"changes"`
	ReleaseNotes      []ReleaseNoteExample `json:"releaseNotes"`
	TokenChanges      []TokenChange        `json:"tokenChanges"`
	Detail            string               `json:"detail"`
	Sections          []string             `json:"sections"`
	TotalChanges      int                  `json:"totalChanges"`
	TotalReleaseNotes int                  `json:"totalReleaseNotes"`
	TotalTokenChanges int                  `json:"totalTokenChanges"`
	Truncated         bool                 `json:"truncated"`
	Error             *ToolError           `json:"error,omitempty"`
}

// GetMigrationGuide implements rhds_get_migration_guide for direct service
// calls.
func (s *Service) GetMigrationGuide(_ context.Context, input MigrationGuideInput) MigrationGuideOutput {
	return s.migrationGuide(input)
}

func (s *Service) migrationGuideWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input MigrationGuideInput,
) (*mcp.CallToolResult, MigrationGuideOutput) {
	resolved, required, failure := s.prepareResolution(
		ctx, newCallEnv(request), input.FromVersion, "", true)
	if required != nil {
		return required, MigrationGuideOutput{}
	}
	if failure != nil {
		out := MigrationGuideOutput{
			Changes:      []MigrationChange{},
			ReleaseNotes: []ReleaseNoteExample{},
			TokenChanges: []TokenChange{},
			Error:        failure,
		}
		return nil, fitMigrationGuide(out, s.responseBudget(input.Detail == "full"))
	}
	if resolved.Resolved.Elements != "" {
		input.FromVersion = resolved.Resolved.Elements
	}
	out := s.migrationGuide(input)
	mergeResolution(&out.Resolved, resolved.Resolved)
	return nil, out
}

func (s *Service) migrationGuide(input MigrationGuideInput) (out MigrationGuideOutput) {
	detail := input.Detail
	if detail == "" {
		detail = "summary"
	}
	// Every return path, including errors that echo caller-supplied versions,
	// goes through the response bound.
	defer func() {
		out = fitMigrationGuide(out, s.responseBudget(detail == "full"))
	}()
	out = MigrationGuideOutput{
		Changes:      []MigrationChange{},
		ReleaseNotes: []ReleaseNoteExample{},
		TokenChanges: []TokenChange{},
	}
	if failure := s.dataFailure(); failure != nil {
		out.Error = failure
		return out
	}
	switch detail {
	case "summary", "full":
	default:
		out.Error = invalidArgument("detail must be summary or full")
		return out
	}
	out.Detail = detail

	fromVersion, failure := s.normalizeMigrationVersion(input.FromVersion, "fromVersion")
	if failure != nil {
		out.Error = failure
		return out
	}
	toVersion := strings.TrimSpace(input.ToVersion)
	if toVersion == "" {
		versions := s.bundledElements()
		if len(versions) == 0 {
			out.Error = &ToolError{
				Code:    "artifact_not_found",
				Message: "no bundled elements data",
				Hint:    "build with generated data: make data build",
			}
			return out
		}
		toVersion = versions[0]
	}
	toVersion, failure = s.normalizeMigrationVersion(toVersion, "toVersion")
	if failure != nil {
		out.Error = failure
		return out
	}
	fromCanonical := semver.Canonical("v" + strings.TrimPrefix(fromVersion, "v"))
	toCanonical := semver.Canonical("v" + strings.TrimPrefix(toVersion, "v"))
	if fromCanonical == "" || toCanonical == "" {
		out.Error = invalidArgument("fromVersion and toVersion must be semantic versions")
		return out
	}
	if semver.Compare(fromCanonical, toCanonical) > 0 {
		out.Error = invalidArgument("fromVersion must not be newer than toVersion")
		return out
	}

	sections, selectedSections, failure := normalizeMigrationSections(input.Sections)
	if failure != nil {
		out.Error = failure
		return out
	}
	out.Sections = selectedSections
	limit := input.Limit
	if limit < 0 {
		out.Error = invalidArgument("limit must be zero or positive")
		return out
	}
	if limit > maxMigrationLimit {
		limit = maxMigrationLimit
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("limit was capped at %d", maxMigrationLimit))
	}

	fromTokens, _ := s.store.Index().TokensForElements(fromVersion)
	toTokens, _ := s.store.Index().TokensForElements(toVersion)
	out.FromVersion = fromVersion
	out.ToVersion = toVersion
	// Keep warnings collected before the resolved block is replaced, notably
	// the limit cap notice.
	resolutionWarnings := out.Resolved.Warnings
	out.Resolved = ResolvedVersion{
		Elements: toVersion,
		Tokens:   toTokens,
		Docs:     s.docsVersionFor(toVersion),
		Source:   "bundled",
		Warnings: resolutionWarnings,
	}
	if fromCanonical == toCanonical {
		out.Resolved.Warnings = append(out.Resolved.Warnings,
			fmt.Sprintf("elements@%s and elements@%s are the same version", fromVersion, toVersion))
		return out
	}

	element := normalizeElementTag(input.Element)
	base, failure := s.loadManifest(fromVersion)
	if failure != nil {
		out.Error = failure
		return out
	}
	head, failure := s.loadManifest(toVersion)
	if failure != nil {
		out.Error = failure
		return out
	}
	if element != "" {
		fromFound, _ := s.componentIn(element, fromVersion)
		toFound, _ := s.componentIn(element, toVersion)
		if !fromFound && !toFound {
			out.Resolved.Warnings = append(out.Resolved.Warnings,
				fmt.Sprintf("%s is not declared in elements@%s or elements@%s", element, fromVersion, toVersion))
		}
	}

	knownTags := unionTags(declarationsByTag(base), declarationsByTag(head))
	if sections["changes"] {
		comparison := s.compareManifests(fromVersion, toVersion, base, head)
		for _, change := range comparison.Changes {
			if element != "" && change.Element != element {
				continue
			}
			out.Changes = append(out.Changes, MigrationChange{
				Rule:     change.Rule,
				Severity: change.Severity.String(),
				Element:  change.Element,
				Subject:  change.Subject,
				Message:  change.Message,
			})
		}
		out.TotalChanges = len(out.Changes)
		out.Changes = limitSlice(out.Changes, limit)
	}

	if sections["releaseNotes"] {
		examples := s.releaseExamples(fromVersion, toVersion, element, knownTags)
		out.TotalReleaseNotes = len(examples)
		out.ReleaseNotes = limitSlice(examples, limit)
		if limit == 0 {
			out.ReleaseNotes = limitSlice(examples, maxMigrationReleaseExamples)
			if len(examples) > maxMigrationReleaseExamples {
				out.Truncated = true
				out.Resolved.Warnings = append(out.Resolved.Warnings,
					fmt.Sprintf("release-note examples were capped at %d of %d; use sections or limit to narrow",
						maxMigrationReleaseExamples, len(examples)))
			}
		}
	}

	includeTokens := true
	if input.IncludeTokens != nil {
		includeTokens = *input.IncludeTokens
	}
	if includeTokens && sections["tokens"] {
		changes, failure := s.tokenChangesBetween(fromTokens, toTokens, detail == "full")
		if failure != nil {
			out.Resolved.Warnings = append(out.Resolved.Warnings, failure.Message)
		} else {
			out.TokenChanges = changes
			out.TotalTokenChanges = len(changes)
			out.TokenChanges = limitSlice(out.TokenChanges, limit)
		}
	}

	return out
}

const maxMigrationLimit = 200

func normalizeMigrationSections(requested []string) (map[string]bool, []string, *ToolError) {
	valid := []string{"changes", "releaseNotes", "tokens"}
	selected := make(map[string]bool, len(valid))
	if len(requested) == 0 {
		for _, section := range valid {
			selected[section] = true
		}
		return selected, valid, nil
	}
	validSet := make(map[string]bool, len(valid))
	for _, section := range valid {
		validSet[section] = true
	}
	for _, section := range requested {
		section = strings.TrimSpace(section)
		if !validSet[section] {
			return nil, nil, invalidArgument(fmt.Sprintf(
				"unknown section %q; valid sections: %s",
				truncateResponseText(section, maxCandidateRunes), strings.Join(valid, ", ")))
		}
		selected[section] = true
	}
	ordered := make([]string, 0, len(selected))
	for _, section := range valid {
		if selected[section] {
			ordered = append(ordered, section)
		}
	}
	return selected, ordered, nil
}

func limitSlice[T any](items []T, limit int) []T {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

// normalizeMigrationVersion accepts bundled versions with or without a leading
// "v" and maps a docs snapshot tag to the newest bundled elements release for
// its major.
func (s *Service) normalizeMigrationVersion(value, field string) (string, *ToolError) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", invalidArgument(field + " is required")
	}
	if s.store.Has(packageElements, trimmed, kindManifest) {
		return trimmed, nil
	}
	stripped := strings.TrimPrefix(trimmed, "v")
	if s.store.Has(packageElements, stripped, kindManifest) {
		return stripped, nil
	}
	if s.store.Has(packageDocs, trimmed, kindDocs) {
		if version, ok := s.elementsVersionForDocsTag(trimmed); ok {
			return version, nil
		}
	}
	return "", &ToolError{
		Code:    "artifact_not_found",
		Message: fmt.Sprintf("elements@%s is not bundled", truncateResponseText(trimmed, maxCandidateRunes)),
		Hint:    "call rhds_resolve_version to list bundled versions",
	}
}

func unionTags(sets ...map[string]*manifest.CustomElementDeclaration) map[string]bool {
	tags := make(map[string]bool)
	for _, set := range sets {
		for tag := range set {
			tags[tag] = true
		}
	}
	return tags
}

func tagFingerprint(tags map[string]bool) string {
	if len(tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tags))
	for tag := range tags {
		keys = append(keys, tag)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func (s *Service) compareManifests(fromVersion, toVersion string, base, head *manifest.Package) *breaking.Result {
	key := fromVersion + "\x00" + toVersion
	if cached, ok := s.comparisons.Load(key); ok {
		return cached.(*breaking.Result)
	}
	result := breaking.Compare(base, head, breaking.Options{})
	actual, _ := s.comparisons.LoadOrStore(key, result)
	return actual.(*breaking.Result)
}

func (s *Service) componentIn(tag, version string) (bool, bool) {
	model, failure := s.loadModel(version)
	if failure != nil {
		return false, false
	}
	_, ok := model.Component(tag)
	return ok, true
}

const maxMigrationReleaseExamples = 100

// releaseExamples reads the per-version release notes in the range and
// extracts Before/After pairs. Older data trees only have the combined
// changelog; it is used as a fallback. Parsed pages are cached per version so
// a second guide call does not decompress or re-parse them.
func (s *Service) releaseExamples(
	fromVersion, toVersion, element string,
	knownTags map[string]bool,
) []ReleaseNoteExample {
	versions := versionsBetween(s.bundledElements(), fromVersion, toVersion)
	cacheSuffix := tagFingerprint(knownTags)
	var examples []ReleaseNoteExample
	for _, version := range versions {
		cacheKey := version + "\x00" + cacheSuffix
		var parsed []ReleaseNoteExample
		if cached, ok := s.releaseNotes.Load(cacheKey); ok {
			parsed = cached.([]ReleaseNoteExample)
		} else {
			content, err := s.store.Read(packageReleaseNotes, version, kindReleaseNotes)
			if err != nil {
				content, err = s.combinedReleaseNotes(version)
				if err != nil {
					continue
				}
			}
			parsed = parseReleaseExamples(version, content, knownTags)
			s.releaseNotes.Store(cacheKey, parsed)
		}
		for _, example := range parsed {
			if element != "" && example.Element != element {
				continue
			}
			examples = append(examples, example)
		}
	}
	return examples
}

func (s *Service) combinedReleaseNotes(version string) ([]byte, error) {
	content, err := s.store.Read(packageReleaseNotes, "latest", kindChangelog)
	if err != nil {
		return nil, err
	}
	sections := markdown.Sections(content, 2)
	for _, section := range sections {
		if strings.TrimSpace(section.Heading) == version {
			return []byte("## " + section.Heading + "\n\n" + section.Body + "\n"), nil
		}
	}
	return nil, fmt.Errorf("no release notes for %s", version)
}

func versionsBetween(versions []string, fromVersion, toVersion string) []string {
	from := semver.Canonical("v" + strings.TrimPrefix(fromVersion, "v"))
	to := semver.Canonical("v" + strings.TrimPrefix(toVersion, "v"))
	if from == "" || to == "" {
		return nil
	}
	var result []string
	for _, version := range versions {
		canonical := semver.Canonical("v" + strings.TrimPrefix(version, "v"))
		if canonical == "" {
			continue
		}
		if semver.Compare(canonical, from) > 0 && semver.Compare(canonical, to) <= 0 {
			result = append(result, version)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a := semver.Canonical("v" + strings.TrimPrefix(result[i], "v"))
		b := semver.Canonical("v" + strings.TrimPrefix(result[j], "v"))
		return semver.Compare(a, b) < 0
	})
	return result
}

// parseReleaseExamples extracts bullet-scoped Before/After code pairs.
func parseReleaseExamples(version string, content []byte, knownTags map[string]bool) []ReleaseNoteExample {
	sections := markdown.Sections(content, 2)
	if len(sections) == 0 {
		sections = []markdown.Chunk{{Body: string(content)}}
	}
	var examples []ReleaseNoteExample
	seen := make(map[string]bool)
	for _, section := range sections {
		if section.Heading != "" && section.Heading != version {
			continue
		}
		for _, bullet := range releaseNoteBullets(section.Body) {
			title := releaseNoteTitle(bullet)
			tag := findTagName(bullet, knownTags)
			for _, pair := range beforeAfterPairs(bullet) {
				key := version + "\x00" + pair.Before + "\x00" + pair.After
				if seen[key] {
					continue
				}
				seen[key] = true
				examples = append(examples, ReleaseNoteExample{
					Version: version,
					Title:   title,
					Element: tag,
					Before:  pair.Before,
					After:   pair.After,
					Text:    strings.TrimSpace(collapseWhitespace(title)),
					Source:  "release-notes@" + version,
				})
			}
		}
	}
	return examples
}

// releaseNoteBullets splits a section body into top-level "- " bullets.
func releaseNoteBullets(body string) []string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var (
		bullets  []string
		current  []string
		fence    string
		inBullet bool
	)
	flush := func() {
		if inBullet && len(current) > 0 {
			bullets = append(bullets, strings.Join(current, "\n"))
		}
		current = nil
		inBullet = false
	}
	for _, line := range lines {
		if fence != "" {
			current = append(current, line)
			if closesFenceLine(line, fence) {
				fence = ""
			}
			continue
		}
		if marker, ok := opensFenceLine(line); ok {
			fence = marker
			if inBullet {
				current = append(current, line)
			}
			continue
		}
		if strings.HasPrefix(line, "- ") {
			flush()
			inBullet = true
			current = []string{line}
			continue
		}
		if inBullet {
			current = append(current, line)
		}
	}
	flush()
	return bullets
}

func opensFenceLine(line string) (string, bool) {
	return markdown.OpensFence(line)
}

func closesFenceLine(line, marker string) bool {
	return markdown.ClosesFence(line, marker)
}

func releaseNoteTitle(bullet string) string {
	line, _, _ := strings.Cut(bullet, "\n")
	line = strings.TrimPrefix(strings.TrimSpace(line), "- ")
	if hash, rest, ok := strings.Cut(line, ": "); ok && isChangesetHash(hash) {
		line = rest
	}
	return strings.TrimSpace(line)
}

func isChangesetHash(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 7 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

type beforeAfterPair struct {
	Before string
	After  string
}

// beforeAfterPairs returns every Before/After pair in a bullet, in order.
func beforeAfterPairs(bullet string) []beforeAfterPair {
	lines := strings.Split(strings.ReplaceAll(bullet, "\r\n", "\n"), "\n")
	blocks := markdown.CodeBlocks(bullet)
	var (
		pairs   []beforeAfterPair
		pending string
	)
	for _, block := range blocks {
		switch nearestLabel(lines, block.StartLine-1) {
		case "before":
			pending = block.Content
		case "after":
			if pending != "" {
				pairs = append(pairs, beforeAfterPair{Before: pending, After: block.Content})
				pending = ""
			}
		}
	}
	return pairs
}

// nearestLabel walks upward from the line before a code fence and returns
// "before" or "after" when it finds that label, stopping at a bullet boundary.
// Release notes write labels as plain text ("Before:"), ATX headings
// ("##### Before"), and annotated headings ("After, for the new API:").
func nearestLabel(lines []string, start int) string {
	for index := start; index >= 0 && index < len(lines); index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		if heading := strings.TrimLeft(line, "#"); heading != line {
			heading = strings.TrimSpace(heading)
			if heading != "" {
				line = heading
			}
		}
		line = strings.Trim(line, "*_` ")
		switch {
		case hasLabelPrefix(line, "before"):
			return "before"
		case hasLabelPrefix(line, "after"):
			return "after"
		case strings.HasPrefix(line, "- "):
			return ""
		}
	}
	return ""
}

// hasLabelPrefix reports whether line is a bare label or a label followed by
// punctuation or an annotation, so "aftermath" does not match "after".
func hasLabelPrefix(line, label string) bool {
	if !strings.HasPrefix(strings.ToLower(line), label) {
		return false
	}
	rest := line[len(label):]
	if rest == "" {
		return true
	}
	switch rest[0] {
	case ':', ',', '(', '[', '-', ' ':
		return true
	default:
		return false
	}
}

// findTagName returns the first real custom-element tag in text. It rejects
// CSS custom properties (--rh-*), file names (rh-*.css), and paths, and
// prefers tags that are declared in the compared manifests.
func findTagName(text string, known map[string]bool) string {
	lower := strings.ToLower(text)
	for index := 0; index+3 <= len(lower); index++ {
		if !strings.HasPrefix(lower[index:], "rh-") {
			continue
		}
		if index > 0 && lower[index-1] == '-' {
			continue
		}
		if index > 0 && !tagBoundaryBefore(lower[index-1]) {
			continue
		}
		end := index + 3
		for end < len(lower) && isTagNameByte(lower[end]) {
			end++
		}
		if end == index+3 {
			continue
		}
		if end < len(lower) && (lower[end] == '.' || lower[end] == '/' || lower[end] == '_') {
			continue
		}
		candidate := lower[index:end]
		if len(known) > 0 && !known[candidate] {
			continue
		}
		return candidate
	}
	return ""
}

func tagBoundaryBefore(c byte) bool {
	switch c {
	case '<', '`', '"', '\'', '(', '[', ' ', '\t', '\n', '\r', '>', ',', ':':
		return true
	default:
		return false
	}
}

func isTagNameByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
}

func (s *Service) tokenChangesBetween(fromTokens, toTokens string, expandVariants bool) ([]TokenChange, *ToolError) {
	if fromTokens == "" || toTokens == "" {
		return nil, nil
	}
	fromMajor := versionMajor(fromTokens)
	toMajor := versionMajor(toTokens)
	if fromMajor == 0 || toMajor == 0 || fromMajor == toMajor {
		return nil, nil
	}
	key := fromTokens + "\x00" + toTokens + "\x00" + strconv.FormatBool(expandVariants)
	if cached, ok := s.tokenChanges.Load(key); ok {
		return cached.([]TokenChange), nil
	}
	migrations, err := data.LoadTokenMigrations()
	if err != nil {
		return nil, &ToolError{
			Code:    "invalid_token_migrations",
			Message: "could not load the curated token migration data",
			Hint:    err.Error(),
		}
	}
	var deprecations, removals, renames []TokenChange
	for _, migration := range migrations.Between(fromMajor, toMajor) {
		for _, deprecation := range migration.Deprecations {
			name := deprecation.Name
			if name == "" {
				name = "*" + deprecation.Suffix
			}
			deprecations = append(deprecations, TokenChange{
				Action:      "deprecate",
				From:        name,
				Since:       migration.ToVersion,
				Replacement: deprecation.Replacement,
				Note:        deprecation.Note,
			})
		}
		for _, removal := range migration.Removals {
			removals = append(removals, TokenChange{
				Action:      "remove",
				From:        removal.Name,
				Since:       migration.ToVersion,
				Replacement: removal.Replacement,
				Note:        removal.Reason,
			})
		}
		for _, rename := range migration.Renames {
			renames = append(renames, TokenChange{
				Action:   "rename",
				From:     rename.From,
				To:       rename.To,
				Variants: rename.Variants,
				Since:    migration.ToVersion,
			})
			if !expandVariants {
				continue
			}
			for _, suffix := range rename.Variants {
				renames = append(renames, TokenChange{
					Action: "rename",
					From:   rename.From + suffix,
					To:     rename.To + suffix,
					Since:  migration.ToVersion,
				})
			}
		}
	}
	changes := make([]TokenChange, 0, len(deprecations)+len(removals)+len(renames))
	changes = append(changes, deprecations...)
	changes = append(changes, removals...)
	changes = append(changes, renames...)
	s.tokenChanges.Store(key, changes)
	return changes, nil
}

func versionMajor(version string) int {
	canonical := semver.Canonical("v" + strings.TrimPrefix(version, "v"))
	if canonical == "" {
		return 0
	}
	major := strings.TrimPrefix(semver.Major(canonical), "v")
	parsed, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return parsed
}
