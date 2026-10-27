// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"sync"
	"testing"
)

func TestManifestIsParsedOncePerVersion(t *testing.T) {
	svc := fixtureService(t)
	for range 3 {
		out := svc.ListComponents(context.Background(), ListComponentsInput{})
		if out.Error != nil {
			t.Fatalf("ListComponents() error = %+v", out.Error)
		}
	}
	if got := svc.manifestParses.Load(); got != 1 {
		t.Errorf("manifest parses = %d, want 1", got)
	}
}

func TestTokensAreParsedOncePerVersion(t *testing.T) {
	svc := fixtureService(t)
	for range 3 {
		out := svc.QueryTokens(context.Background(), QueryTokensInput{Token: "color-brand", Theme: "light"})
		if out.Error != nil {
			t.Fatalf("QueryTokens() error = %+v", out.Error)
		}
	}
	if got := svc.tokenParses.Load(); got != 1 {
		t.Errorf("token parses = %d, want 1", got)
	}
	if got := svc.themeParses.Load(); got != 1 {
		t.Errorf("theme parses = %d, want 1", got)
	}
}

func TestArtifactCachesAreSafeUnderConcurrency(t *testing.T) {
	svc := fixtureService(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.ListComponents(context.Background(), ListComponentsInput{})
			_ = svc.QueryTokens(context.Background(), QueryTokensInput{Token: "color-brand", Theme: "light"})
		}()
	}
	wg.Wait()

	if got := svc.manifestParses.Load(); got != 1 {
		t.Errorf("manifest parses = %d, want 1", got)
	}
	if got := svc.tokenParses.Load(); got != 1 {
		t.Errorf("token parses = %d, want 1", got)
	}
	if got := svc.themeParses.Load(); got != 1 {
		t.Errorf("theme parses = %d, want 1", got)
	}
}
