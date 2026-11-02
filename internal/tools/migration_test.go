// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestMigrationGuideBreakingChanges(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
		Element:     "rh-card",
		Sections:    []string{"changes"},
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	if out.FromVersion != "1.0.0" || out.ToVersion != "5.0.0" {
		t.Errorf("versions = %s -> %s", out.FromVersion, out.ToVersion)
	}
	var rules []string
	for _, change := range out.Changes {
		if change.Element != "rh-card" {
			t.Errorf("change is for %q, want rh-card: %+v", change.Element, change)
		}
		if change.Severity == "breaking" {
			rules = append(rules, change.Rule)
		}
	}
	if !slices.Contains(rules, "attribute-removed") && !slices.Contains(rules, "attributeRemoved") {
		t.Errorf("breaking rules = %v, want an attribute removal", rules)
	}
	if !slices.Contains(rules, "slot-removed") && !slices.Contains(rules, "slotRemoved") {
		t.Errorf("breaking rules = %v, want a slot removal", rules)
	}
}

func TestMigrationGuideReleaseNoteExamples(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "4.0.0",
		ToVersion:   "5.0.0",
		Element:     "rh-card",
		Sections:    []string{"releaseNotes"},
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	if len(out.ReleaseNotes) == 0 {
		t.Fatal("no release-note examples")
	}
	found := false
	for _, note := range out.ReleaseNotes {
		if note.Version != "5.0.0" {
			t.Errorf("note version = %q, want 5.0.0", note.Version)
		}
		if note.Element != "rh-card" {
			t.Errorf("note element = %q, want rh-card", note.Element)
		}
		if containsFold(note.Before, "legacy") && containsFold(note.After, "rh-card") {
			found = true
		}
	}
	if !found {
		t.Errorf("release notes = %+v, want the rh-card Before/After example", out.ReleaseNotes)
	}
}

func TestMigrationGuideTokenChanges(t *testing.T) {
	svc := fixtureService(t)
	out := svc.GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
		Detail:      "full",
		Sections:    []string{"tokens"},
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	var rename, deprecation bool
	for _, change := range out.TokenChanges {
		rename = rename || change.Action == "rename"
		deprecation = deprecation || change.Action == "deprecate"
	}
	if !rename {
		t.Errorf("token changes = %+v, want at least one rename", out.TokenChanges)
	}
	if !deprecation {
		t.Errorf("token changes = %+v, want at least one deprecation", out.TokenChanges)
	}

	// The curated entries themselves are asserted without the budget, because
	// a hard-capped page may not reach a specific entry deep in the list.
	all, failure := svc.tokenChangesBetween("1.1.2", "3.2.0", true)
	if failure != nil {
		t.Fatalf("tokenChangesBetween() = %+v", failure)
	}
	rename, deprecation = false, false
	for _, change := range all {
		if change.Action == "rename" && change.From == "--rh-color-red-50" && change.To == "--rh-color-red-10" {
			rename = true
		}
		if change.Action == "deprecate" && change.From == "--rh-color-surface-dark-alt" {
			deprecation = true
		}
	}
	if !rename {
		t.Error("curated token changes have no red-50 rename")
	}
	if !deprecation {
		t.Error("curated token changes have no surface-dark-alt deprecation")
	}
}

func TestMigrationGuideIncludeTokensFalse(t *testing.T) {
	includeTokens := false
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion:   "1.0.0",
		ToVersion:     "5.0.0",
		IncludeTokens: &includeTokens,
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	if len(out.TokenChanges) != 0 {
		t.Errorf("token changes = %+v, want none", out.TokenChanges)
	}
}

func TestMigrationGuideDefaultsToLatest(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "4.0.0",
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	if out.ToVersion != "5.0.0" {
		t.Errorf("to version = %q, want 5.0.0", out.ToVersion)
	}
}

func TestMigrationGuideRejectsUnknownVersion(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "9.9.9",
	})
	if out.Error == nil || out.Error.Code != "artifact_not_found" {
		t.Fatalf("error = %+v, want artifact_not_found", out.Error)
	}
}

func TestMigrationGuideRejectsBackwardsRange(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "5.0.0",
		ToVersion:   "1.0.0",
	})
	if out.Error == nil || out.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v, want invalid_argument", out.Error)
	}
}

func TestMigrationGuideFullDetailUsesHardCap(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
		Detail:      "full",
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	size := wireResponseBytes(out)
	t.Logf("migration full wire response = %d bytes (hard cap %d)", size, hardResponseBudget)
	if size > hardResponseBudget {
		t.Errorf("response = %d bytes, over the %d byte hard cap", size, hardResponseBudget)
	}
}

func TestMigrationGuideRespectsBudget(t *testing.T) {
	svc := fixtureService(t)
	svc.maxResponseBytes = 4000
	out := svc.GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	size := wireResponseBytes(out)
	t.Logf("migration wire response = %d bytes (test budget 4000)", size)
	if size > 4000 {
		t.Errorf("response = %d bytes, over the 4000 byte budget", size)
	}
	if !out.Truncated {
		t.Error("truncated = false, want true")
	}
	if !hasWarning(out.Resolved.Warnings, "budget") {
		t.Errorf("warnings = %v, want a budget warning", out.Resolved.Warnings)
	}
	if !hasWarning(out.Resolved.Warnings, "omitted") {
		t.Errorf("warnings = %v, want an omitted-count warning", out.Resolved.Warnings)
	}
}

// migrationCategories builds category slices large enough that the old
// whole-category eviction logic drops token changes entirely. This makes the
// test below fail when fitMigrationGuide regresses to that behavior.
func migrationCategories(changeCount, noteCount, tokenCount int) (
	[]MigrationChange, []ReleaseNoteExample, []TokenChange,
) {
	changes := make([]MigrationChange, 0, changeCount)
	for i := 0; i < changeCount; i++ {
		changes = append(changes, MigrationChange{
			Rule:     "attribute-removed",
			Severity: "breaking",
			Element:  "rh-card",
			Message:  strings.Repeat("change message ", 12),
		})
	}
	notes := make([]ReleaseNoteExample, 0, noteCount)
	for i := 0; i < noteCount; i++ {
		notes = append(notes, ReleaseNoteExample{
			Version: "5.0.0",
			Element: "rh-card",
			Before:  strings.Repeat("before ", 20),
			After:   strings.Repeat("after ", 20),
			Source:  "release-notes@5.0.0",
		})
	}
	tokens := make([]TokenChange, 0, tokenCount)
	for i := 0; i < tokenCount; i++ {
		tokens = append(tokens, TokenChange{
			Action: "rename",
			From:   fmt.Sprintf("--rh-old-%03d", i),
			To:     fmt.Sprintf("--rh-new-%03d", i),
			Since:  "2.0.0",
		})
	}
	return changes, notes, tokens
}

func TestMigrationGuideDefaultKeepsEveryCategory(t *testing.T) {
	changes, notes, tokens := migrationCategories(10, 10, 150)
	out := MigrationGuideOutput{
		Resolved:          ResolvedVersion{Elements: "5.0.0", Tokens: "3.2.0"},
		FromVersion:       "1.0.0",
		ToVersion:         "5.0.0",
		Changes:           changes,
		ReleaseNotes:      notes,
		TokenChanges:      tokens,
		TotalChanges:      len(changes),
		TotalReleaseNotes: len(notes),
		TotalTokenChanges: len(tokens),
	}
	fitted := fitMigrationGuide(out, defaultResponseBudget)
	if fitted.Error != nil {
		t.Fatalf("fitMigrationGuide() error = %+v", fitted.Error)
	}
	if len(fitted.Changes) == 0 {
		t.Error("changes = 0, want at least one")
	}
	if len(fitted.ReleaseNotes) == 0 {
		t.Error("releaseNotes = 0, want at least one")
	}
	if len(fitted.TokenChanges) == 0 {
		t.Error("tokenChanges = 0, want at least one")
	}
	if !fitted.Truncated {
		t.Error("truncated = false, want true")
	}
	if size := wireResponseBytes(fitted); size > defaultResponseBudget {
		t.Errorf("response = %d bytes, over the %d byte default", size, defaultResponseBudget)
	}
}

// smallMigrationOutput builds short category entries so a 2000-byte budget can
// still keep one entry per category.
func smallMigrationOutput(changeCount, noteCount, tokenCount int) MigrationGuideOutput {
	changes := make([]MigrationChange, 0, changeCount)
	for i := 0; i < changeCount; i++ {
		changes = append(changes, MigrationChange{
			Rule: "attribute-removed", Severity: "breaking",
			Element: "rh-x", Message: "m",
		})
	}
	notes := make([]ReleaseNoteExample, 0, noteCount)
	for i := 0; i < noteCount; i++ {
		notes = append(notes, ReleaseNoteExample{
			Version: "5.0.0", Element: "rh-x",
			Before: "<a>", After: "<b>", Source: "release-notes@5.0.0",
		})
	}
	tokens := make([]TokenChange, 0, tokenCount)
	for i := 0; i < tokenCount; i++ {
		tokens = append(tokens, TokenChange{
			Action: "rename",
			From:   fmt.Sprintf("--rh-a-%02d", i),
			To:     fmt.Sprintf("--rh-b-%02d", i),
		})
	}
	return MigrationGuideOutput{
		Resolved:          ResolvedVersion{Elements: "5.0.0", Tokens: "3.2.0"},
		FromVersion:       "1.0.0",
		ToVersion:         "5.0.0",
		Changes:           changes,
		ReleaseNotes:      notes,
		TokenChanges:      tokens,
		TotalChanges:      len(changes),
		TotalReleaseNotes: len(notes),
		TotalTokenChanges: len(tokens),
	}
}

func TestMigrationGuideKeepsCategoryMinimumAtSmallBudget(t *testing.T) {
	out := smallMigrationOutput(10, 5, 20)
	fitted := fitMigrationGuide(out, 2000)
	if fitted.Error != nil {
		t.Fatalf("fitMigrationGuide() error = %+v", fitted.Error)
	}
	if len(fitted.Changes) == 0 {
		t.Error("changes = 0, want at least one at the 2000 byte budget")
	}
	if len(fitted.ReleaseNotes) == 0 {
		t.Error("releaseNotes = 0, want at least one at the 2000 byte budget")
	}
	if len(fitted.TokenChanges) == 0 {
		t.Error("tokenChanges = 0, want at least one at the 2000 byte budget")
	}
	if size := wireResponseBytes(fitted); size > 2000 {
		t.Errorf("response = %d bytes, over the 2000 byte budget", size)
	}
}

// TestMigrationGuideOmittedCountsUseSelectedTotal checks that an explicit
// limit is not reported as a budget omission. The warning must describe what
// the budget dropped from the post-limit selection.
func TestMigrationGuideOmittedCountsUseSelectedTotal(t *testing.T) {
	out := smallMigrationOutput(40, 10, 40)
	out.TotalChanges = 400
	out.TotalReleaseNotes = 100
	out.TotalTokenChanges = 400
	fitted := fitMigrationGuide(out, 2000)
	if fitted.Error != nil {
		t.Fatalf("fitMigrationGuide() error = %+v", fitted.Error)
	}
	if !hasWarning(fitted.Resolved.Warnings, "of 40 changes") {
		t.Errorf("warnings = %v, want an omitted count against the 40 selected changes", fitted.Resolved.Warnings)
	}
	if hasWarning(fitted.Resolved.Warnings, "of 400 changes") {
		t.Errorf("warnings = %v, want the post-limit count, not the pre-limit total", fitted.Resolved.Warnings)
	}
}

// TestMigrationGuideSmallBudgetErrorIsStructured covers the case where even a
// single item from each category cannot fit: the guide must fail loudly rather
// than silently drop a category.
func TestMigrationGuideSmallBudgetErrorIsStructured(t *testing.T) {
	svc := fixtureService(t)
	svc.maxResponseBytes = 2000
	out := svc.GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
	})
	if out.Error == nil || out.Error.Code != "response_too_large" {
		t.Fatalf("error = %+v, want response_too_large", out.Error)
	}
	if len(out.Changes)+len(out.ReleaseNotes)+len(out.TokenChanges) != 0 {
		t.Errorf("partial categories returned alongside the error: %d/%d/%d",
			len(out.Changes), len(out.ReleaseNotes), len(out.TokenChanges))
	}
}

func TestMigrationGuideSectionsSelector(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
		Sections:    []string{"tokens"},
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	if len(out.Changes) != 0 || len(out.ReleaseNotes) != 0 {
		t.Errorf("unselected categories are not empty: changes=%d notes=%d",
			len(out.Changes), len(out.ReleaseNotes))
	}
	if len(out.TokenChanges) == 0 {
		t.Error("tokenChanges = 0, want the selected category")
	}
	if !slices.Equal(out.Sections, []string{"tokens"}) {
		t.Errorf("sections = %v", out.Sections)
	}
}

func TestMigrationGuideRejectsUnknownSection(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		Sections:    []string{"nope"},
	})
	if out.Error == nil || out.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v, want invalid_argument", out.Error)
	}
}

func TestMigrationGuideAcceptsVPrefixAndDocsTag(t *testing.T) {
	svc := fixtureService(t)
	for _, input := range []MigrationGuideInput{
		{FromVersion: "v4.0.0", ToVersion: "v5.0.0"},
		{FromVersion: "v4.2.2", ToVersion: "v5.0.0"},
	} {
		out := svc.GetMigrationGuide(context.Background(), input)
		if out.Error != nil {
			t.Errorf("GetMigrationGuide(%+v) error = %+v", input, out.Error)
			continue
		}
		if out.FromVersion == "" || out.ToVersion != "5.0.0" {
			t.Errorf("versions = %q -> %q", out.FromVersion, out.ToVersion)
		}
	}
}

func TestMigrationGuideLimitCapsEachCategory(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
		Sections:    []string{"changes", "tokens"},
		Detail:      "full",
		Limit:       5,
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	if len(out.Changes) > 5 || len(out.TokenChanges) > 5 {
		t.Errorf("limit not applied: changes=%d tokens=%d", len(out.Changes), len(out.TokenChanges))
	}
}

func TestMigrationGuideLimitMarksTruncatedAndCountsOmitted(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
		Sections:    []string{"changes", "tokens"},
		Detail:      "full",
		Limit:       1,
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	if out.TotalChanges <= 1 {
		t.Fatalf("totalChanges = %d, want more than the limit of 1", out.TotalChanges)
	}
	if out.TotalTokenChanges <= 1 {
		t.Fatalf("totalTokenChanges = %d, want more than the limit of 1", out.TotalTokenChanges)
	}
	if len(out.Changes) != 1 || len(out.TokenChanges) != 1 {
		t.Errorf("limited counts = %d changes, %d token changes, want 1 each",
			len(out.Changes), len(out.TokenChanges))
	}
	if !out.Truncated {
		t.Error("truncated = false, want true")
	}
	if !hasWarning(out.Resolved.Warnings, "omitted") {
		t.Errorf("warnings = %v, want an omitted-count warning", out.Resolved.Warnings)
	}
	if !hasWarning(out.Resolved.Warnings, "changes") {
		t.Errorf("warnings = %v, want the omitted changes count", out.Resolved.Warnings)
	}
	if !hasWarning(out.Resolved.Warnings, "token") {
		t.Errorf("warnings = %v, want the omitted token changes count", out.Resolved.Warnings)
	}
}

func TestMigrationGuideLimitOmittedCountsMatchTotals(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "1.0.0",
		ToVersion:   "5.0.0",
		Sections:    []string{"changes", "tokens"},
		Detail:      "full",
		Limit:       2,
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	if got, want := len(out.Changes), min(2, out.TotalChanges); got != want {
		t.Errorf("changes = %d, want %d", got, want)
	}
	if got, want := len(out.TokenChanges), min(2, out.TotalTokenChanges); got != want {
		t.Errorf("token changes = %d, want %d", got, want)
	}
	if out.TotalChanges > len(out.Changes) || out.TotalTokenChanges > len(out.TokenChanges) {
		if !out.Truncated {
			t.Error("truncated = false, want true when the limit drops entries")
		}
	}
}

func TestFindTagNameRejectsCSSVariablesAndFiles(t *testing.T) {
	known := map[string]bool{"rh-card": true, "rh-accordion": true}
	cases := map[string]string{
		"use --rh-color-red-50":                       "",
		"`rh-accordion-lightdom-shim.css`":            "",
		"`rh-accordion/rh-accordion.js`":              "",
		"`<rh-card>`":                                 "rh-card",
		"the rh-card element":                         "rh-card",
		"`<rh-accordion>` and `<rh-accordion-panel>`": "rh-accordion",
	}
	for text, want := range cases {
		if got := findTagName(text, known); got != want {
			t.Errorf("findTagName(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestBeforeAfterPairsHandlesMultiplePairs(t *testing.T) {
	bullet := "- abc1234: `<rh-card>` changed.\n\nBefore:\n\n```html\nA\n```\n\nAfter:\n\n```html\nB\n```\n\nBefore:\n\n```html\nC\n```\n\nAfter:\n\n```html\nD\n```\n"
	pairs := beforeAfterPairs(bullet)
	if len(pairs) != 2 {
		t.Fatalf("pairs = %+v, want 2", pairs)
	}
	if pairs[0].Before != "A" || pairs[0].After != "B" {
		t.Errorf("first pair = %+v", pairs[0])
	}
	if pairs[1].Before != "C" || pairs[1].After != "D" {
		t.Errorf("second pair = %+v", pairs[1])
	}
}

func TestParseReleaseExamplesKeepsMultiplePairs(t *testing.T) {
	content := []byte("## 5.0.0\n\n- abc1234: `<rh-card>` changed.\n\nBefore:\n\n```html\nA\n```\n\nAfter:\n\n```html\nB\n```\n\nBefore:\n\n```html\nC\n```\n\nAfter:\n\n```html\nD\n```\n")
	examples := parseReleaseExamples("5.0.0", content, map[string]bool{"rh-card": true})
	if len(examples) != 2 {
		t.Fatalf("examples = %+v, want 2", examples)
	}
	for _, example := range examples {
		if example.Element != "rh-card" {
			t.Errorf("element = %q, want rh-card", example.Element)
		}
	}
}

func TestMigrationGuideWithEnvResolvesFromVersion(t *testing.T) {
	_, out := fixtureService(t).migrationGuideWithEnv(context.Background(), nil, MigrationGuideInput{
		FromVersion: "v1.0.0",
		ToVersion:   "v5.0.0",
	})
	if out.Error != nil {
		t.Fatalf("migrationGuideWithEnv() error = %+v", out.Error)
	}
	if out.FromVersion != "1.0.0" || out.ToVersion != "5.0.0" {
		t.Errorf("versions = %q -> %q, want 1.0.0 -> 5.0.0",
			out.FromVersion, out.ToVersion)
	}
}

func TestBeforeAfterPairsAcceptsHeadingAndAnnotatedLabels(t *testing.T) {
	bullet := strings.Join([]string{
		"- abc1234: `<rh-card>` changed.",
		"",
		"##### Before",
		"",
		"```html",
		"A",
		"```",
		"",
		"**After, for the new API:**",
		"",
		"```html",
		"B",
		"```",
		"",
		"#### Aftermath is not a label",
		"",
		"```html",
		"C",
		"```",
	}, "\n")
	pairs := beforeAfterPairs(bullet)
	if len(pairs) != 1 {
		t.Fatalf("pairs = %+v, want 1", pairs)
	}
	if pairs[0].Before != "A" || pairs[0].After != "B" {
		t.Errorf("pair = %+v, want A before and B after", pairs[0])
	}
}

func TestMigrationGuideTotalReleaseNotesSurvivesLimit(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "4.0.0",
		ToVersion:   "5.0.0",
		Sections:    []string{"releaseNotes"},
		Limit:       1,
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	if out.TotalReleaseNotes < 2 {
		t.Errorf("totalReleaseNotes = %d, want at least 2", out.TotalReleaseNotes)
	}
	if len(out.ReleaseNotes) != 1 {
		t.Errorf("releaseNotes = %d, want the explicit limit of 1", len(out.ReleaseNotes))
	}
}

func TestMigrationGuideKeepsLimitCapWarning(t *testing.T) {
	out := fixtureService(t).GetMigrationGuide(context.Background(), MigrationGuideInput{
		FromVersion: "4.0.0",
		ToVersion:   "5.0.0",
		Limit:       500,
	})
	if out.Error != nil {
		t.Fatalf("GetMigrationGuide() error = %+v", out.Error)
	}
	found := false
	for _, warning := range out.Resolved.Warnings {
		if strings.Contains(warning, fmt.Sprintf("limit was capped at %d", maxMigrationLimit)) {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want the limit cap warning", out.Resolved.Warnings)
	}
}
