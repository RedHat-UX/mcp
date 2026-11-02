// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package data

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// TokenMigrationsPath is the embedded path of the curated token migration
// data. It is a source file, not generated data, so it is committed and
// reviewed like code.
const TokenMigrationsPath = "tokens-migrations.json"

//go:embed tokens-migrations.json
var tokenMigrationsJSON []byte

// TokenMigrations is the curated cross-major token migration document.
type TokenMigrations struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Provenance    TokenMigrationProvenance `json:"provenance"`
	Migrations    []TokenMajorMigration    `json:"migrations"`
}

// TokenMigrationProvenance records where the curated data came from.
type TokenMigrationProvenance struct {
	TokensChangelog string   `json:"tokensChangelog"`
	NormalizedData  string   `json:"normalizedData"`
	Method          string   `json:"method"`
	Reviewed        string   `json:"reviewed"`
	Notes           []string `json:"notes,omitempty"`
}

// TokenMajorMigration describes token changes between two majors.
type TokenMajorMigration struct {
	FromMajor    int                `json:"fromMajor"`
	ToMajor      int                `json:"toMajor"`
	FromVersion  string             `json:"fromVersion"`
	ToVersion    string             `json:"toVersion"`
	Renames      []TokenRename      `json:"renames,omitempty"`
	Removals     []TokenRemoval     `json:"removals,omitempty"`
	Deprecations []TokenDeprecation `json:"deprecations,omitempty"`
}

// TokenRename is one token rename. Variants lists suffixes that follow the
// same rename, for example "-rgb" and "-hsl".
type TokenRename struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Variants []string `json:"variants,omitempty"`
}

// TokenRemoval is a token that no longer exists.
type TokenRemoval struct {
	Name        string `json:"name"`
	Replacement string `json:"replacement,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// TokenDeprecation is a token or a family of tokens that still exists but
// should not be used in new code. Exactly one of Name and Suffix is set.
type TokenDeprecation struct {
	Name        string `json:"name,omitempty"`
	Suffix      string `json:"suffix,omitempty"`
	Replacement string `json:"replacement,omitempty"`
	Note        string `json:"note,omitempty"`
}

// TokenMigrationsJSON returns the embedded curated document for resources.
func TokenMigrationsJSON() []byte {
	return tokenMigrationsJSON
}

// LoadTokenMigrations parses and validates the embedded migration document.
func LoadTokenMigrations() (*TokenMigrations, error) {
	var migrations TokenMigrations
	decoder := json.NewDecoder(strings.NewReader(string(tokenMigrationsJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&migrations); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", TokenMigrationsPath, err)
	}
	if err := migrations.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", TokenMigrationsPath, err)
	}
	return &migrations, nil
}

// Validate checks the invariants that make the document safe to serve.
func (m *TokenMigrations) Validate() error {
	if m == nil {
		return errors.New("nil token migrations")
	}
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema version %d", m.SchemaVersion)
	}
	if m.Provenance.TokensChangelog == "" ||
		m.Provenance.NormalizedData == "" ||
		m.Provenance.Method == "" ||
		m.Provenance.Reviewed == "" {
		return errors.New("provenance is incomplete")
	}
	if len(m.Migrations) == 0 {
		return errors.New("no migrations")
	}
	pairs := make(map[string]bool, len(m.Migrations))
	for _, migration := range m.Migrations {
		if migration.FromMajor < 1 || migration.ToMajor <= migration.FromMajor {
			return fmt.Errorf("invalid major range %d -> %d", migration.FromMajor, migration.ToMajor)
		}
		if migration.FromVersion == "" || migration.ToVersion == "" {
			return fmt.Errorf("migration %d -> %d has no source versions",
				migration.FromMajor, migration.ToMajor)
		}
		key := fmt.Sprintf("%d-%d", migration.FromMajor, migration.ToMajor)
		if pairs[key] {
			return fmt.Errorf("duplicate migration %s", key)
		}
		pairs[key] = true

		seen := make(map[string]bool)
		for _, rename := range migration.Renames {
			if err := validateTokenName("rename source", rename.From); err != nil {
				return err
			}
			if err := validateTokenName("rename target", rename.To); err != nil {
				return err
			}
			for _, suffix := range rename.Variants {
				if !strings.HasPrefix(suffix, "-") {
					return fmt.Errorf("rename %s variant %q does not start with '-'", rename.From, suffix)
				}
			}
			for _, name := range rename.Names() {
				if seen[name] {
					return fmt.Errorf("duplicate rename source %q", name)
				}
				seen[name] = true
			}
		}
		for _, removal := range migration.Removals {
			if err := validateTokenName("removal", removal.Name); err != nil {
				return err
			}
		}
		for _, deprecation := range migration.Deprecations {
			if (deprecation.Name == "") == (deprecation.Suffix == "") {
				return fmt.Errorf("deprecation must set exactly one of name and suffix: %+v", deprecation)
			}
			if deprecation.Name != "" {
				if err := validateTokenName("deprecation", deprecation.Name); err != nil {
					return err
				}
			}
			if deprecation.Suffix != "" && !strings.HasPrefix(deprecation.Suffix, "-") {
				return fmt.Errorf("deprecation suffix %q does not start with '-'", deprecation.Suffix)
			}
		}
	}
	if err := validateCoverage(m.Migrations); err != nil {
		return err
	}
	return nil
}

// validateCoverage rejects a gap between migration ranges. Direct jumps are
// allowed as long as their range starts at or before the covered major.
func validateCoverage(migrations []TokenMajorMigration) error {
	if len(migrations) == 0 {
		return nil
	}
	sorted := append([]TokenMajorMigration(nil), migrations...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].FromMajor != sorted[j].FromMajor {
			return sorted[i].FromMajor < sorted[j].FromMajor
		}
		return sorted[i].ToMajor < sorted[j].ToMajor
	})
	covered := sorted[0].FromMajor
	for _, migration := range sorted {
		if migration.FromMajor > covered {
			return fmt.Errorf("gap in migration coverage after major %d", covered)
		}
		if migration.ToMajor > covered {
			covered = migration.ToMajor
		}
	}
	return nil
}

// Names returns the rename source and its variant sources.
func (r TokenRename) Names() []string {
	names := make([]string, 0, 1+len(r.Variants))
	names = append(names, r.From)
	for _, suffix := range r.Variants {
		names = append(names, r.From+suffix)
	}
	return names
}

// Majors returns the migration between two adjacent majors.
func (m *TokenMigrations) Majors(from, to int) (TokenMajorMigration, bool) {
	if m == nil {
		return TokenMajorMigration{}, false
	}
	for _, migration := range m.Migrations {
		if migration.FromMajor == from && migration.ToMajor == to {
			return migration, true
		}
	}
	return TokenMajorMigration{}, false
}

// Between returns every migration needed to move from one major to another,
// in ascending order. A same-major range returns no migrations.
func (m *TokenMigrations) Between(from, to int) []TokenMajorMigration {
	if m == nil || to <= from {
		return nil
	}
	if direct, ok := m.Majors(from, to); ok {
		return []TokenMajorMigration{direct}
	}
	result := make([]TokenMajorMigration, 0, to-from)
	for major := from; major < to; major++ {
		migration, ok := m.Majors(major, major+1)
		if !ok {
			return nil
		}
		result = append(result, migration)
	}
	return result
}

func validateTokenName(label, name string) error {
	if !strings.HasPrefix(name, "--rh-") || len(name) <= len("--rh-") {
		return fmt.Errorf("%s %q is not an --rh-* token name", label, name)
	}
	return nil
}
