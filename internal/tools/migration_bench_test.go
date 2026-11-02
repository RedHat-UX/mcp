// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/redhat-ux/mcp/internal/data"
)

func realMigrationService(tb testing.TB) *Service {
	tb.Helper()
	if _, err := os.Stat(filepath.Join("..", "data", "dist", data.IndexPath)); err != nil {
		tb.Skip("generated data is absent; run make data")
	}
	dist, err := fs.Sub(data.Embedded(), "dist")
	if err != nil {
		tb.Fatal(err)
	}
	store, err := data.Open(dist)
	if err != nil {
		tb.Fatal(err)
	}
	return New(store, nil)
}

func BenchmarkMigrationGuideCrossMajor(b *testing.B) {
	service := realMigrationService(b)
	input := MigrationGuideInput{FromVersion: "1.0.0", ToVersion: "5.0.0"}
	// Warm the manifest, comparison, release-note, and token-change caches.
	if out := service.GetMigrationGuide(context.Background(), input); out.Error != nil {
		b.Fatalf("warm call: %+v", out.Error)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := service.GetMigrationGuide(context.Background(), input); out.Error != nil {
			b.Fatalf("call: %+v", out.Error)
		}
	}
}

func TestRealMigrationGuideWarmLatency(t *testing.T) {
	service := realMigrationService(t)
	input := MigrationGuideInput{FromVersion: "1.0.0", ToVersion: "5.0.0"}
	coldStart := time.Now()
	if out := service.GetMigrationGuide(context.Background(), input); out.Error != nil {
		t.Fatalf("warm call: %+v", out.Error)
	}
	t.Logf("cold cross-major migration guide = %s", time.Since(coldStart))
	start := time.Now()
	out := service.GetMigrationGuide(context.Background(), input)
	elapsed := time.Since(start)
	if out.Error != nil {
		t.Fatalf("call: %+v", out.Error)
	}
	t.Logf("warm cross-major migration guide = %s", elapsed)
	if elapsed > 100*time.Millisecond {
		t.Errorf("warm migration guide took %s, want under 100ms", elapsed)
	}
}
