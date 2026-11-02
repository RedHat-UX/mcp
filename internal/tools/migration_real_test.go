// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-ux/mcp/internal/data"
)

// TestRealTokenMigrationsMatchBundledNames checks every curated rename and
// deprecation against the actual normalized tokens. It is skipped on a clean
// checkout.
func TestRealTokenMigrationsMatchBundledNames(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "data", "dist", data.IndexPath)); err != nil {
		t.Skip("generated data is absent; run make data")
	}
	dist, err := fs.Sub(data.Embedded(), "dist")
	if err != nil {
		t.Fatal(err)
	}
	store, err := data.Open(dist)
	if err != nil {
		t.Fatalf("data.Open() = %v", err)
	}
	service := New(store, nil)

	migrations, err := data.LoadTokenMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations.Migrations {
		fromNames := tokenNames(t, service, migration.FromVersion)
		toNames := tokenNames(t, service, migration.ToVersion)
		covered := make(map[string]bool)
		pairs := make(map[string]bool)
		for _, rename := range migration.Renames {
			for _, name := range rename.Names() {
				if !fromNames[name] {
					t.Errorf("rename source %q is not in tokens@%s", name, migration.FromVersion)
				}
				covered[name] = true
			}
			if !toNames[rename.To] {
				t.Errorf("rename target %q is not in tokens@%s", rename.To, migration.ToVersion)
			}
			pairs[rename.From+"\x00"+rename.To] = true
			for _, suffix := range rename.Variants {
				if !toNames[rename.To+suffix] {
					t.Errorf("rename target %q is not in tokens@%s", rename.To+suffix, migration.ToVersion)
				}
				pairs[rename.From+suffix+"\x00"+rename.To+suffix] = true
			}
		}
		for from := range pairs {
			old, new, _ := strings.Cut(from, "\x00")
			if pairs[new+"\x00"+old] {
				t.Errorf("rename %q -> %q has a reverse entry", old, new)
			}
		}
		for _, removal := range migration.Removals {
			covered[removal.Name] = true
		}
		for _, deprecation := range migration.Deprecations {
			if deprecation.Name != "" {
				covered[deprecation.Name] = true
				continue
			}
			for name := range fromNames {
				if strings.HasSuffix(name, deprecation.Suffix) {
					covered[name] = true
				}
			}
		}
		disappearing := 0
		for name := range fromNames {
			if toNames[name] {
				continue
			}
			disappearing++
			if !covered[name] {
				t.Errorf("disappearing token %q is not covered by a rename, removal, or deprecation",
					name)
			}
		}
		if migration.FromMajor == 1 && disappearing != 102 {
			t.Errorf("disappearing names = %d for 1.x -> 2.x, want the curated 102", disappearing)
		}
		for _, removal := range migration.Removals {
			if fromNames[removal.Name] && !toNames[removal.Name] {
				continue
			}
			t.Errorf("removal %q is not a removal between tokens@%s and tokens@%s",
				removal.Name, migration.FromVersion, migration.ToVersion)
		}
		for _, deprecation := range migration.Deprecations {
			if deprecation.Name != "" {
				if !toNames[deprecation.Name] {
					t.Errorf("deprecated token %q is not in tokens@%s",
						deprecation.Name, migration.ToVersion)
				}
				continue
			}
			found := false
			for name := range toNames {
				if strings.HasSuffix(name, deprecation.Suffix) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("no token with suffix %q in tokens@%s",
					deprecation.Suffix, migration.ToVersion)
			}
		}
	}
}

func tokenNames(t *testing.T, service *Service, version string) map[string]bool {
	t.Helper()
	tokens, _, failure := service.loadTokens(version)
	if failure != nil {
		t.Fatalf("loading tokens@%s: %+v", version, failure)
	}
	names := make(map[string]bool, len(tokens))
	for _, tok := range tokens {
		names[tok.CSSVariableName()] = true
	}
	return names
}
