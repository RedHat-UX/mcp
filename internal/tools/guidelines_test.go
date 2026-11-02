// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestDesignGuidelinesChunksByHeading(t *testing.T) {
	out := fixtureService(t).GetDesignGuidelines(context.Background(), DesignGuidelinesInput{
		Element: "rh-button",
		Version: "5.0.0",
	})
	if out.Error != nil {
		t.Fatalf("GetDesignGuidelines() error = %+v", out.Error)
	}
	if out.Resolved.Docs != "v5.0.0" {
		t.Errorf("docs version = %q, want v5.0.0", out.Resolved.Docs)
	}
	var headings []string
	for _, chunk := range out.Chunks {
		headings = append(headings, chunk.Heading)
		if chunk.ReleaseTag != "v5.0.0" {
			t.Errorf("release tag = %q, want v5.0.0", chunk.ReleaseTag)
		}
		if chunk.Path == "" || chunk.Source == "" {
			t.Errorf("chunk has no path or source: %+v", chunk)
		}
		if slices.Contains([]string{"Variants", "Accessibility", "Size", "Color"}, chunk.Heading) {
			if chunk.Text == "" {
				t.Errorf("chunk %q has no body", chunk.Heading)
			}
		}
	}
	for _, want := range []string{"Button", "Variants", "Accessibility", "Style", "Size", "Color"} {
		if !slices.Contains(headings, want) {
			t.Errorf("headings = %v, missing %q", headings, want)
		}
	}
}

func TestDesignGuidelinesTopicMatchesHeadingAndBody(t *testing.T) {
	out := fixtureService(t).GetDesignGuidelines(context.Background(), DesignGuidelinesInput{
		Topic:   "accessibility",
		Version: "5.0.0",
	})
	if out.Error != nil {
		t.Fatalf("GetDesignGuidelines() error = %+v", out.Error)
	}
	if len(out.Chunks) == 0 {
		t.Fatal("no chunks matched topic accessibility")
	}
	for _, chunk := range out.Chunks {
		haystack := chunk.Heading + "\n" + chunk.Text
		if !containsFold(haystack, "accessibility") {
			t.Errorf("chunk does not match topic: %+v", chunk)
		}
	}
}

func TestDesignGuidelinesLimitTruncatesExplicitly(t *testing.T) {
	out := fixtureService(t).GetDesignGuidelines(context.Background(), DesignGuidelinesInput{
		Element: "rh-button",
		Version: "5.0.0",
		Limit:   2,
	})
	if out.Error != nil {
		t.Fatalf("GetDesignGuidelines() error = %+v", out.Error)
	}
	if len(out.Chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(out.Chunks))
	}
	if !out.Truncated {
		t.Error("truncated = false, want true")
	}
	if out.Total <= 2 {
		t.Errorf("total = %d, want more than the limit", out.Total)
	}
	if !hasWarning(out.Resolved.Warnings, "limit") {
		t.Errorf("warnings = %v, want a limit warning", out.Resolved.Warnings)
	}
}

func TestDesignGuidelinesMapsElementsVersionToDocsSnapshot(t *testing.T) {
	out := fixtureService(t).GetDesignGuidelines(context.Background(), DesignGuidelinesInput{
		Topic:   "card",
		Version: "4.0.0",
	})
	if out.Error != nil {
		t.Fatalf("GetDesignGuidelines() error = %+v", out.Error)
	}
	if out.Resolved.Docs != "v4.2.2" {
		t.Errorf("docs version = %q, want v4.2.2", out.Resolved.Docs)
	}
	if len(out.Chunks) == 0 || !containsFold(out.Chunks[0].Text, "version 4 card") {
		t.Errorf("chunks = %+v, want the v4.2.2 docs", out.Chunks)
	}
}

func TestDesignGuidelinesRespectsBudget(t *testing.T) {
	svc := fixtureService(t)
	svc.maxResponseBytes = 3000
	out := svc.GetDesignGuidelines(context.Background(), DesignGuidelinesInput{
		Element: "rh-button",
		Version: "5.0.0",
		Limit:   100,
	})
	if out.Error != nil {
		t.Fatalf("GetDesignGuidelines() error = %+v", out.Error)
	}
	size := wireResponseBytes(out)
	t.Logf("guidelines wire response = %d bytes (test budget 3000)", size)
	if size > 3000 {
		t.Errorf("response = %d bytes, over the 3000 byte budget", size)
	}
	if !out.Truncated {
		t.Error("truncated = false, want true")
	}
	if !hasWarning(out.Resolved.Warnings, "budget") {
		t.Errorf("warnings = %v, want a budget warning", out.Resolved.Warnings)
	}
}

func containsFold(haystack, needle string) bool {
	return hasWarning([]string{haystack}, needle)
}

func TestDesignGuidelinesDocsTagReturnsFullResolvedBlock(t *testing.T) {
	out := fixtureService(t).GetDesignGuidelines(context.Background(), DesignGuidelinesInput{
		Topic:   "button",
		Version: "v5.0.0",
	})
	if out.Error != nil {
		t.Fatalf("GetDesignGuidelines() error = %+v", out.Error)
	}
	if out.Resolved.Docs != "v5.0.0" {
		t.Errorf("docs = %q", out.Resolved.Docs)
	}
	if out.Resolved.Elements != "5.0.0" {
		t.Errorf("elements = %q, want 5.0.0", out.Resolved.Elements)
	}
	if out.Resolved.Tokens != "3.2.0" {
		t.Errorf("tokens = %q, want 3.2.0", out.Resolved.Tokens)
	}
	if out.Resolved.Source != "bundled" {
		t.Errorf("source = %q", out.Resolved.Source)
	}
}

func TestDesignGuidelinesElementWithUnmatchedTopicIsEmpty(t *testing.T) {
	out := fixtureService(t).GetDesignGuidelines(context.Background(), DesignGuidelinesInput{
		Element: "rh-button",
		Topic:   "spaceship",
		Version: "5.0.0",
	})
	if out.Error != nil {
		t.Fatalf("GetDesignGuidelines() error = %+v, want an empty result", out.Error)
	}
	if len(out.Chunks) != 0 {
		t.Errorf("chunks = %+v, want none", out.Chunks)
	}
	if !hasWarning(out.Resolved.Warnings, "spaceship") {
		t.Errorf("warnings = %v, want a topic-specific warning", out.Resolved.Warnings)
	}
}

func TestStripSourceComment(t *testing.T) {
	input := "<!-- source: RedHat-UX/red-hat-design-system@v5.0.0 docs/x.md -->\n\n# Heading\n\nBody."
	got := stripSourceComment(input)
	if strings.Contains(got, "<!-- source:") {
		t.Errorf("stripSourceComment() = %q, leaked the source comment", got)
	}
	if !strings.HasPrefix(got, "# Heading") {
		t.Errorf("stripSourceComment() = %q, want the heading first", got)
	}
}
