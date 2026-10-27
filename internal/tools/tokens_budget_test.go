// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/redhat-ux/mcp/internal/data"
)

func TestQueryTokensThemeOnlySourceIsCSS(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-accent-base",
		Theme: "dark",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v", out.Tokens)
	}
	tok := out.Tokens[0]
	if tok.Source != "css/default-theme.css" {
		t.Errorf("source = %q, want css/default-theme.css", tok.Source)
	}
	if tok.Value != "#0066cc" {
		t.Errorf("light value = %q, want #0066cc", tok.Value)
	}
	if tok.ThemeValue != "#92c5f9" {
		t.Errorf("dark theme value = %q, want #92c5f9", tok.ThemeValue)
	}
}

func TestQueryTokensJSONSource(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-brand",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if out.Tokens[0].Source != "json" {
		t.Errorf("source = %q, want json", out.Tokens[0].Source)
	}
}

func TestQueryTokensChainFormat(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token:  "color-interactive",
		Format: "chain",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	want := "--rh-color-interactive -> --rh-color-brand -> --rh-color-red-50"
	if got := out.Tokens[0].Value; got != want {
		t.Errorf("chain value = %q, want %q", got, want)
	}
}

func TestQueryTokensAutoThemeUsesLightAndWarns(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token: "color-brand",
		Theme: "auto",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if got, want := out.Tokens[0].ThemeValue, "#ff0000"; got != want {
		t.Errorf("auto theme value = %q, want %q", got, want)
	}
	if !hasWarning(out.Resolved.Warnings, "auto") {
		t.Errorf("warnings = %v, want an auto theme warning", out.Resolved.Warnings)
	}
}

func TestQueryTokensPaginatesWithCursor(t *testing.T) {
	svc := fixtureService(t)
	first := svc.QueryTokens(context.Background(), QueryTokensInput{PageSize: 3})
	if first.Error != nil {
		t.Fatalf("first page error = %+v", first.Error)
	}
	if len(first.Tokens) != 3 {
		t.Fatalf("first page = %d tokens, want 3", len(first.Tokens))
	}
	if first.NextCursor == "" {
		t.Fatal("first page has no nextCursor")
	}
	if first.Total != 10 {
		t.Errorf("total = %d, want 10", first.Total)
	}

	second := svc.QueryTokens(context.Background(), QueryTokensInput{
		PageSize: 3,
		Cursor:   first.NextCursor,
	})
	if second.Error != nil {
		t.Fatalf("second page error = %+v", second.Error)
	}
	if len(second.Tokens) != 3 {
		t.Fatalf("second page = %d tokens, want 3", len(second.Tokens))
	}
	for _, a := range first.Tokens {
		for _, b := range second.Tokens {
			if a.Name == b.Name {
				t.Errorf("token %q appears on both pages", a.Name)
			}
		}
	}
}

func TestQueryTokensRejectsCursorFromAnotherVersion(t *testing.T) {
	svc := fixtureService(t)
	cursor := encodeCursor("1.0.0", 3, false)
	out := svc.QueryTokens(context.Background(), QueryTokensInput{Cursor: cursor})
	if out.Error == nil || out.Error.Code != "cursor_version_mismatch" {
		t.Fatalf("error = %+v, want cursor_version_mismatch", out.Error)
	}
}

func TestQueryTokensAcceptsCoupledElementsVersion(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Token:   "color-brand",
		Version: "5.0.0",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if out.Resolved.Tokens != "3.2.0" {
		t.Errorf("resolved tokens = %q, want the coupled 3.2.0", out.Resolved.Tokens)
	}
	if !hasWarning(out.Resolved.Warnings, "elements") {
		t.Errorf("warnings = %v, want an elements-to-tokens resolution warning", out.Resolved.Warnings)
	}
}

func TestResponseBudgetConstants(t *testing.T) {
	if defaultResponseBudget != 8*1024 {
		t.Errorf("default budget = %d, want 8192", defaultResponseBudget)
	}
	if hardResponseBudget != 32*1024 {
		t.Errorf("hard budget = %d, want 32768", hardResponseBudget)
	}
}

func TestQueryTokensRespectsDefaultBudget(t *testing.T) {
	svc := New(largeTokenStore(t, 300), nil)
	out := svc.QueryTokens(context.Background(), QueryTokensInput{Detail: "summary"})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	size := wireResponseBytes(out)
	t.Logf("summary wire response = %d bytes (default budget %d)", size, defaultResponseBudget)
	if size > defaultResponseBudget {
		t.Errorf("summary response = %d bytes, over the %d byte default", size, defaultResponseBudget)
	}
	if out.NextCursor == "" {
		t.Error("budget-limited response has no nextCursor")
	}
	if !hasWarning(out.Resolved.Warnings, "budget") {
		t.Errorf("warnings = %v, want a budget warning", out.Resolved.Warnings)
	}
}

func TestQueryTokensFullDetailUsesHardCap(t *testing.T) {
	svc := New(largeTokenStore(t, 300), nil)
	out := svc.QueryTokens(context.Background(), QueryTokensInput{Detail: "full", PageSize: 100})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	size := wireResponseBytes(out)
	t.Logf("full wire response = %d bytes (hard cap %d)", size, hardResponseBudget)
	if size > hardResponseBudget {
		t.Errorf("full response = %d bytes, over the %d byte hard cap", size, hardResponseBudget)
	}
}

func TestQueryTokensUsesServiceOverrideBudget(t *testing.T) {
	svc := New(largeTokenStore(t, 300), nil)
	svc.maxResponseBytes = 2000
	out := svc.QueryTokens(context.Background(), QueryTokensInput{Detail: "full", PageSize: 100})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	size := wireResponseBytes(out)
	t.Logf("override wire response = %d bytes (budget 2000)", size)
	if size > 2000 {
		t.Errorf("response = %d bytes, over the service override of 2000", size)
	}
	if out.NextCursor == "" {
		t.Error("override-limited response has no nextCursor")
	}
}

func TestQueryTokensSingleOversizeResultIsAnError(t *testing.T) {
	svc := New(largeTokenStore(t, 1), nil)
	svc.maxResponseBytes = 100
	out := svc.QueryTokens(context.Background(), QueryTokensInput{Token: "color-token-000"})
	if out.Error == nil || out.Error.Code != "response_too_large" {
		t.Fatalf("error = %+v, want response_too_large", out.Error)
	}
}

func hasWarning(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(strings.ToLower(warning), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// largeTokenStore builds an in-memory tokens artifact big enough to exercise
// the NFR-03 budgets without committing a large fixture.
func largeTokenStore(t *testing.T, count int) *data.Store {
	t.Helper()
	color := map[string]any{"$type": "color"}
	for i := 0; i < count; i++ {
		color[fmt.Sprintf("token-%03d", i)] = map[string]any{
			"$value":       "#ff0000",
			"$type":        "color",
			"$description": strings.Repeat("description text ", 16),
		}
	}
	document, err := json.Marshal(map[string]any{"color": color})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(document)
	index := []byte(fmt.Sprintf(`{
		"schemaVersion": 1,
		"artifacts": [
			{"package": "tokens", "version": "9.0.0", "kind": "tokens", "path": "tokens/rhds.tokens.json", "sha256": "%x", "bytes": %d}
		]
	}`, sum, len(document)))
	store, err := data.Open(fstest.MapFS{
		"index.json":              {Data: index},
		"tokens/rhds.tokens.json": {Data: document},
	})
	if err != nil {
		t.Fatalf("opening large fixture store: %v", err)
	}
	return store
}

func TestFitQueryTokensPageMarksFieldTruncation(t *testing.T) {
	chain := make([]string, 25)
	for i := range chain {
		chain[i] = strings.Repeat("a", 10)
	}
	chain[0] = strings.Repeat("a", 250)
	out := QueryTokensOutput{
		Resolved: ResolvedVersion{Tokens: "3.2.0"},
		Tokens: []TokenResult{{
			Name:        "token-x",
			CSSVariable: "--rh-token-x",
			Value:       "v",
			Description: strings.Repeat("d", 600),
			AliasChain:  chain,
			Source:      tokenSourceJSON,
		}},
		Total: 1,
	}
	fitted := fitQueryTokensPage(out, defaultResponseBudget, 0)
	if fitted.Error != nil {
		t.Fatalf("fitQueryTokensPage() error = %+v", fitted.Error)
	}
	if len(fitted.Tokens) != 1 {
		t.Fatalf("tokens = %d, want one", len(fitted.Tokens))
	}
	if !fitted.Tokens[0].Truncated {
		t.Error("token Truncated = false, want true")
	}
	if !hasWarning(fitted.Resolved.Warnings, "description") {
		t.Errorf("warnings = %v, want a description truncation warning", fitted.Resolved.Warnings)
	}
	if !hasWarning(fitted.Resolved.Warnings, "aliasChain truncated from 25 to 20 entries") {
		t.Errorf("warnings = %v, want an aliasChain truncation warning", fitted.Resolved.Warnings)
	}
	if !hasWarning(fitted.Resolved.Warnings, "aliasChain[0] truncated") {
		t.Errorf("warnings = %v, want an aliasChain entry truncation warning", fitted.Resolved.Warnings)
	}
	if size := wireResponseBytes(fitted); size > defaultResponseBudget {
		t.Errorf("response = %d bytes, over the %d byte default", size, defaultResponseBudget)
	}
}

// TestFitQueryTokensPageNoticesCountReturnedEntries checks that a notice
// describes the page actually returned, not entries the budget trimmed away.
func TestFitQueryTokensPageNoticesCountReturnedEntries(t *testing.T) {
	tokens := make([]TokenResult, 40)
	for i := range tokens {
		tokens[i] = TokenResult{
			Name:        fmt.Sprintf("token-%03d", i),
			CSSVariable: fmt.Sprintf("--rh-token-%03d", i),
			Value:       "v",
			Description: strings.Repeat("d", 600),
			Source:      tokenSourceJSON,
		}
	}
	out := QueryTokensOutput{
		Resolved: ResolvedVersion{Tokens: "3.2.0"},
		Tokens:   tokens,
		Total:    len(tokens),
	}
	fitted := fitQueryTokensPage(out, 2500, 0)
	if fitted.Error != nil {
		t.Fatalf("fitQueryTokensPage() error = %+v", fitted.Error)
	}
	if len(fitted.Tokens) == 0 || len(fitted.Tokens) == len(tokens) {
		t.Fatalf("tokens = %d, want a trimmed non-empty page", len(fitted.Tokens))
	}
	want := fmt.Sprintf("token %s description truncated", fitted.Tokens[0].Name)
	if !hasWarning(fitted.Resolved.Warnings, want) {
		t.Errorf("warnings = %v, want %q", fitted.Resolved.Warnings, want)
	}
	last := tokens[len(tokens)-1].Name
	lastReturned := false
	for _, tok := range fitted.Tokens {
		if tok.Name == last {
			lastReturned = true
			break
		}
	}
	if !lastReturned && hasWarning(fitted.Resolved.Warnings, "token "+last+" description truncated") {
		t.Errorf("warnings = %v, still describe a trimmed entry (%s)", fitted.Resolved.Warnings, last)
	}
	if size := wireResponseBytes(fitted); size > 2500 {
		t.Errorf("response = %d bytes, over 2500", size)
	}
}

// TestFitQueryTokensPageOversizeTokenReturnsError follows the cursor when a
// single token cannot fit. The page must fail structurally instead of
// returning an empty page whose cursor points at the same offset forever.
func TestFitQueryTokensPageOversizeTokenReturnsError(t *testing.T) {
	chain := make([]string, 20)
	for i := range chain {
		chain[i] = strings.Repeat("a", 190)
	}
	base := QueryTokensOutput{
		Resolved: ResolvedVersion{Tokens: "3.2.0"},
		Tokens: []TokenResult{{
			Name:        "token-x",
			CSSVariable: "--rh-token-x",
			Value:       "v",
			AliasChain:  chain,
			Source:      tokenSourceJSON,
		}},
		Total: 1,
	}
	cursor := ""
	for hop := 0; hop < 10; hop++ {
		out := base
		out.Tokens = append([]TokenResult(nil), base.Tokens...)
		out.NextCursor = cursor
		fitted := fitQueryTokensPage(out, defaultResponseBudget, 0)
		if fitted.Error != nil {
			if fitted.Error.Code != "response_too_large" {
				t.Fatalf("hop %d error = %+v, want response_too_large", hop, fitted.Error)
			}
			if fitted.NextCursor != "" {
				t.Errorf("hop %d returned a cursor alongside the error: %q", hop, fitted.NextCursor)
			}
			if len(fitted.Tokens) != 0 {
				t.Errorf("hop %d returned %d tokens alongside the error", hop, len(fitted.Tokens))
			}
			return
		}
		if fitted.NextCursor == "" {
			t.Fatalf("hop %d returned no tokens, no cursor, and no error", hop)
		}
		if fitted.NextCursor == cursor {
			t.Fatalf("hop %d returned a self-referential cursor %q", hop, cursor)
		}
		cursor = fitted.NextCursor
	}
	t.Fatal("cursor loop did not terminate")
}

func TestFitQueryTokensPageTrimWarningMatchesCount(t *testing.T) {
	tokens := make([]TokenResult, 300)
	for i := range tokens {
		tokens[i] = TokenResult{
			Name:        fmt.Sprintf("token-%03d", i),
			CSSVariable: fmt.Sprintf("--rh-token-%03d", i),
			Value:       "v",
			Description: strings.Repeat("d", 200),
			Source:      tokenSourceJSON,
		}
	}
	// Choose a budget where the page trims to exactly 50 tokens without a
	// warning, so adding the warning and cursor forces a further trim. The
	// warning must then report the final count, not the pre-loop count.
	trimmed := QueryTokensOutput{
		Resolved: ResolvedVersion{Tokens: "3.2.0"},
		Tokens:   append([]TokenResult(nil), tokens[:50]...),
		Total:    len(tokens),
	}
	budget := estimatedResponseBytes(trimmed) + 10
	out := QueryTokensOutput{
		Resolved: ResolvedVersion{Tokens: "3.2.0"},
		Tokens:   tokens,
		Total:    len(tokens),
	}
	fitted := fitQueryTokensPage(out, budget, 0)
	if fitted.Error != nil {
		t.Fatalf("fitQueryTokensPage() error = %+v", fitted.Error)
	}
	if len(fitted.Tokens) >= 50 {
		t.Fatalf("tokens = %d, want the warning and cursor to force a further trim", len(fitted.Tokens))
	}
	want := fmt.Sprintf("to %d to fit", len(fitted.Tokens))
	if !hasWarning(fitted.Resolved.Warnings, want) {
		t.Errorf("warnings = %v, want %q", fitted.Resolved.Warnings, want)
	}
}

// TestFitQueryTokensPageCountsCursorBytes sweeps budgets where a single token
// plus its cursor fits. A larger page must trim to fit rather than fail
// because the cursor was added after the last budget check.
func TestFitQueryTokensPageCountsCursorBytes(t *testing.T) {
	tokens := make([]TokenResult, 40)
	for i := range tokens {
		tokens[i] = TokenResult{
			Name:        fmt.Sprintf("token-%03d", i),
			CSSVariable: fmt.Sprintf("--rh-token-%03d", i),
			Value:       "v",
			Description: strings.Repeat("d", 80),
			Source:      tokenSourceJSON,
		}
	}
	tested := 0
	for budget := 500; budget <= 4000; budget += 13 {
		// The minimum feasible page is one token plus the trim warning and
		// the cursor. Only sweep budgets where that page fits.
		single := QueryTokensOutput{
			Resolved: ResolvedVersion{
				Tokens: "3.2.0",
				Warnings: []string{fmt.Sprintf(
					"results trimmed from %d to %d to fit the response budget",
					len(tokens), 1)},
			},
			Total:      len(tokens),
			Tokens:     append([]TokenResult(nil), tokens[:1]...),
			NextCursor: encodeCursor("3.2.0", 1, false),
			Truncated:  true,
		}
		if estimatedResponseBytes(single) > budget {
			continue
		}
		tested++
		out := QueryTokensOutput{
			Resolved: ResolvedVersion{Tokens: "3.2.0"},
			Total:    len(tokens),
			Tokens:   append([]TokenResult(nil), tokens...),
		}
		fitted := fitQueryTokensPage(out, budget, 0)
		if fitted.Error != nil {
			t.Fatalf("budget %d: %+v, but one token with its cursor fits", budget, fitted.Error)
		}
		if got := wireResponseBytes(fitted); got > budget {
			t.Errorf("budget %d: response = %d bytes", budget, got)
		}
		if fitted.NextCursor == "" {
			t.Errorf("budget %d: partial page has no cursor", budget)
		}
	}
	if tested == 0 {
		t.Fatal("no budget exercised the cursor path")
	}
}

func TestQueryTokensWarnsWhenQueryAndTokenAreBothSet(t *testing.T) {
	out := fixtureService(t).QueryTokens(context.Background(), QueryTokensInput{
		Query: "brand",
		Token: "color-brand",
	})
	if out.Error != nil {
		t.Fatalf("QueryTokens() error = %+v", out.Error)
	}
	if len(out.Tokens) != 1 || out.Tokens[0].Name != "color-brand" {
		t.Errorf("tokens = %+v, want the exact token", out.Tokens)
	}
	if !hasWarning(out.Resolved.Warnings, "precedence") {
		t.Errorf("warnings = %v, want an explicit precedence warning", out.Resolved.Warnings)
	}
}
