// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package data

import (
	"strings"
	"testing"
)

func TestLoadTokenMigrations(t *testing.T) {
	migrations, err := LoadTokenMigrations()
	if err != nil {
		t.Fatalf("LoadTokenMigrations() = %v", err)
	}
	if migrations.SchemaVersion != 1 {
		t.Errorf("schema version = %d, want 1", migrations.SchemaVersion)
	}
	if len(migrations.Migrations) == 0 {
		t.Fatal("no migrations")
	}
	if migrations.Provenance.TokensChangelog == "" || migrations.Provenance.Method == "" {
		t.Errorf("provenance = %+v", migrations.Provenance)
	}
}

func TestTokenMigrationsCoverRenamesAndDeprecations(t *testing.T) {
	migrations, err := LoadTokenMigrations()
	if err != nil {
		t.Fatal(err)
	}
	oneToTwo, ok := migrations.Majors(1, 2)
	if !ok {
		t.Fatal("no 1 -> 2 migration")
	}
	if len(oneToTwo.Renames) < 40 {
		t.Errorf("1 -> 2 renames = %d, want at least 40", len(oneToTwo.Renames))
	}
	if got := oneToTwo.Renames[0]; got.From == "" || got.To == "" || len(got.Variants) == 0 {
		t.Errorf("rename = %+v, want from, to, and variants", got)
	}
	twoToThree, ok := migrations.Majors(2, 3)
	if !ok {
		t.Fatal("no 2 -> 3 migration")
	}
	if len(twoToThree.Deprecations) == 0 {
		t.Error("2 -> 3 has no deprecations")
	}
}

func TestTokenMigrationsRenameNamesArePrefixed(t *testing.T) {
	migrations, err := LoadTokenMigrations()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, migration := range migrations.Migrations {
		for _, rename := range migration.Renames {
			if !strings.HasPrefix(rename.From, "--rh-") || !strings.HasPrefix(rename.To, "--rh-") {
				t.Errorf("rename %+v does not use --rh- names", rename)
			}
			names := append([]string{rename.From}, variantNames(rename.From, rename.Variants)...)
			for _, name := range names {
				if seen[name] {
					t.Errorf("duplicate rename source %q", name)
				}
				seen[name] = true
			}
		}
	}
}

func TestTokenMigrationsProvenanceRecordsCuration(t *testing.T) {
	migrations, err := LoadTokenMigrations()
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(migrations.Provenance.Notes, "\n")
	for _, want := range []string{"red 60", "gray-100"} {
		if !strings.Contains(notes, want) {
			t.Errorf("provenance notes do not mention %q:\n%s", want, notes)
		}
	}
}

func variantNames(base string, variants []string) []string {
	names := make([]string, 0, len(variants))
	for _, suffix := range variants {
		names = append(names, base+suffix)
	}
	return names
}

func TestTokenMigrationsBetweenHandlesDirectJump(t *testing.T) {
	migrations := &TokenMigrations{Migrations: []TokenMajorMigration{{
		FromMajor: 1, ToMajor: 3, FromVersion: "1.0.0", ToVersion: "3.0.0",
	}}}
	got := migrations.Between(1, 3)
	if len(got) != 1 || got[0].FromMajor != 1 || got[0].ToMajor != 3 {
		t.Fatalf("Between(1, 3) = %+v, want the direct entry", got)
	}
}

func TestTokenMigrationsRejectsCoverageGap(t *testing.T) {
	migrations := &TokenMigrations{
		SchemaVersion: 1,
		Provenance: TokenMigrationProvenance{
			TokensChangelog: "changelog",
			NormalizedData:  "normalized",
			Method:          "method",
			Reviewed:        "2026-10-27",
		},
		Migrations: []TokenMajorMigration{
			{FromMajor: 1, ToMajor: 2, FromVersion: "1.0.0", ToVersion: "2.0.0"},
			{FromMajor: 3, ToMajor: 4, FromVersion: "3.0.0", ToVersion: "4.0.0"},
		},
	}
	if err := migrations.Validate(); err == nil {
		t.Fatal("Validate() accepted a coverage gap between major 2 and 3")
	}
}

func TestTokenMigrationsProvenanceClaimMatchesDiff(t *testing.T) {
	migrations, err := LoadTokenMigrations()
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(migrations.Provenance.Notes, "\n")
	if !strings.Contains(notes, "102") {
		t.Errorf("provenance does not record the 102-name diff:\n%s", notes)
	}
	migration, ok := migrations.Majors(1, 2)
	if !ok {
		t.Fatal("no 1 -> 2 migration")
	}
	if migration.FromVersion != "1.1.2" || migration.ToVersion != "2.0.0" {
		t.Errorf("diff basis = %s -> %s", migration.FromVersion, migration.ToVersion)
	}
}
