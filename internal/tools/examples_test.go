// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"strings"
	"testing"
)

func TestUsageExamplesDemo(t *testing.T) {
	out := fixtureService(t).GetUsageExamples(context.Background(), UsageExamplesInput{
		TagName: "rh-button",
		Version: "5.0.0",
		Kind:    "demo",
	})
	if out.Error != nil {
		t.Fatalf("GetUsageExamples() error = %+v", out.Error)
	}
	if len(out.Examples) != 1 {
		t.Fatalf("examples = %+v, want one demo", out.Examples)
	}
	demo := out.Examples[0]
	if demo.Kind != "demo" {
		t.Errorf("kind = %q", demo.Kind)
	}
	if demo.URL == "" || demo.Source == "" {
		t.Errorf("demo has no URL or source: %+v", demo)
	}
	if demo.Content != "" {
		t.Errorf("demo content = %q, want empty", demo.Content)
	}
}

func TestUsageExamplesDocsSnippet(t *testing.T) {
	out := fixtureService(t).GetUsageExamples(context.Background(), UsageExamplesInput{
		TagName: "rh-button",
		Version: "5.0.0",
		Kind:    "docs",
	})
	if out.Error != nil {
		t.Fatalf("GetUsageExamples() error = %+v", out.Error)
	}
	if len(out.Examples) == 0 {
		t.Fatal("no docs snippets")
	}
	found := false
	for _, example := range out.Examples {
		if example.Kind == "docs" && strings.Contains(example.Content, "<rh-button") {
			found = true
			if example.Language != "html" {
				t.Errorf("language = %q, want html", example.Language)
			}
			if example.Path == "" || example.Source == "" {
				t.Errorf("docs snippet has no path or source: %+v", example)
			}
		}
	}
	if !found {
		t.Errorf("examples = %+v, want a button docs snippet", out.Examples)
	}
}

func TestUsageExamplesPatternHTML(t *testing.T) {
	out := fixtureService(t).GetUsageExamples(context.Background(), UsageExamplesInput{
		TagName: "rh-button",
		Version: "5.0.0",
		Kind:    "pattern",
	})
	if out.Error != nil {
		t.Fatalf("GetUsageExamples() error = %+v", out.Error)
	}
	if len(out.Examples) != 1 {
		t.Fatalf("examples = %+v, want one pattern", out.Examples)
	}
	pattern := out.Examples[0]
	if pattern.Kind != "pattern" || !strings.Contains(pattern.Content, "<rh-button") {
		t.Errorf("pattern = %+v", pattern)
	}
	if pattern.Source == "" {
		t.Error("pattern has no source")
	}
}

func TestUsageExamplesTwigSlots(t *testing.T) {
	out := fixtureService(t).GetUsageExamples(context.Background(), UsageExamplesInput{
		TagName: "rh-card",
		Version: "5.0.0",
		Kind:    "twig",
	})
	if out.Error != nil {
		t.Fatalf("GetUsageExamples() error = %+v", out.Error)
	}
	if len(out.Examples) != 1 {
		t.Fatalf("examples = %+v, want one twig template", out.Examples)
	}
	twig := out.Examples[0]
	if twig.Kind != "twig" || twig.Language != "twig" {
		t.Errorf("twig example = %+v", twig)
	}
	for _, want := range []string{"rh-card", "slot=\"header\"", "slot=\"footer\"", "{% block content %}"} {
		if !strings.Contains(twig.Content, want) {
			t.Errorf("twig content missing %q:\n%s", want, twig.Content)
		}
	}
	if strings.Contains(strings.ToLower(twig.Content), "rhds/") {
		t.Errorf("twig content claims a library key:\n%s", twig.Content)
	}
	if !strings.Contains(strings.ToLower(twig.Note), "not a drupal library key") {
		t.Errorf("note = %q, want the Drupal disclaimer", twig.Note)
	}
}

func TestUsageExamplesUnknownTag(t *testing.T) {
	out := fixtureService(t).GetUsageExamples(context.Background(), UsageExamplesInput{
		TagName: "rh-nope",
		Version: "5.0.0",
	})
	if out.Error == nil || out.Error.Code != "component_not_found" {
		t.Fatalf("error = %+v, want component_not_found", out.Error)
	}
}

func TestUsageExamplesInvalidKind(t *testing.T) {
	out := fixtureService(t).GetUsageExamples(context.Background(), UsageExamplesInput{
		TagName: "rh-button",
		Version: "5.0.0",
		Kind:    "storybook",
	})
	if out.Error == nil || out.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v, want invalid_argument", out.Error)
	}
}

func TestUsageExamplesRespectsBudget(t *testing.T) {
	svc := fixtureService(t)
	svc.maxResponseBytes = 2000
	out := svc.GetUsageExamples(context.Background(), UsageExamplesInput{
		TagName: "rh-card",
		Version: "5.0.0",
	})
	if out.Error != nil {
		t.Fatalf("GetUsageExamples() error = %+v", out.Error)
	}
	size := wireResponseBytes(out)
	t.Logf("usage examples wire response = %d bytes (test budget 2000)", size)
	if size > 2000 {
		t.Errorf("response = %d bytes, over the 2000 byte budget", size)
	}
	if !out.Truncated {
		t.Error("truncated = false, want true")
	}
	if !hasWarning(out.Resolved.Warnings, "budget") {
		t.Errorf("warnings = %v, want a budget warning", out.Resolved.Warnings)
	}
}
