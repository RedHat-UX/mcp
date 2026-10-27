// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package lock

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testIntegrity = "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, sha512.Size))

func TestStableVersions(t *testing.T) {
	got := StableVersions([]string{
		"1.0.0", "1.0.1", "2.0.0-beta.1", "2.0.0", "2.1.0-rc.1", "10.0.0",
	})
	want := []string{"1.0.0", "1.0.1", "2.0.0", "10.0.0"}
	if len(got) != len(want) {
		t.Fatalf("StableVersions() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("StableVersions() = %v, want %v", got, want)
		}
	}
}

func TestHighestSatisfying(t *testing.T) {
	versions := []string{
		"1.0.0", "1.1.0", "1.1.1", "1.1.2",
		"2.0.0", "2.0.1", "2.1.0", "2.1.1", "2.2.0", "2.2.4",
		"3.0.0", "3.0.1", "3.0.2", "3.1.0", "3.2.0",
	}
	tests := []struct {
		rangeSpec string
		want      string
	}{
		{"^1.0.0", "1.1.2"},
		{"^1.1.2", "1.1.2"},
		{"^2.0.1", "2.2.4"},
		{"^2.1.1", "2.2.4"},
		{"^3.0.0", "3.2.0"},
		{"^3.0.1", "3.2.0"},
		{"^3.1.0", "3.2.0"},
		{"^3.2.0", "3.2.0"},
		{"~2.0.0", "2.0.1"},
		{"2.1.0", "2.1.0"},
		{">=2.0.0 <3.0.0", "2.2.4"},
		{"^4.0.0", ""},
	}
	for _, tt := range tests {
		got, ok := HighestSatisfying(tt.rangeSpec, versions)
		if tt.want == "" {
			if ok {
				t.Errorf("HighestSatisfying(%q) = %q, want no match", tt.rangeSpec, got)
			}
			continue
		}
		if !ok {
			t.Errorf("HighestSatisfying(%q) = no match, want %q", tt.rangeSpec, tt.want)
			continue
		}
		if got != tt.want {
			t.Errorf("HighestSatisfying(%q) = %q, want %q", tt.rangeSpec, got, tt.want)
		}
	}
}

func TestRangeParserEdgeCases(t *testing.T) {
	tests := []struct {
		version   string
		rangeSpec string
		want      bool
	}{
		// >1.2 means the whole 1.2.x line is excluded: >=1.3.0.
		{"1.2.9", ">1.2", false},
		{"1.3.0", ">1.2", true},
		{"2.0.0", ">1.2", true},
		// <=1.2 includes the whole 1.2.x line.
		{"1.2.9", "<=1.2", true},
		{"1.3.0", "<=1.2", false},
		// ^0.0 is >=0.0.0 <0.1.0, not <1.0.0.
		{"0.0.9", "^0.0", true},
		{"0.1.0", "^0.0", false},
		{"1.0.0", "^0.0", false},
		// A partial hyphen upper bound covers the whole minor line.
		{"2.3.9", "1.2.3 - 2.3", true},
		{"2.4.0", "1.2.3 - 2.3", false},
		// An exact hyphen upper bound is inclusive.
		{"2.3.4", "1.2.3 - 2.3.4", true},
		{"2.3.5", "1.2.3 - 2.3.4", false},
		// A partial major upper bound covers the whole major line.
		{"2.9.9", "1.2.3 - 2", true},
		{"3.0.0", "1.2.3 - 2", false},
		// =1.2 matches the whole 1.2.x line; =1.2.3 is exact.
		{"1.2.9", "=1.2", true},
		{"1.3.0", "=1.2", false},
		{"1.2.3", "=1.2.3", true},
		{"1.2.4", "=1.2.3", false},
	}
	for _, tt := range tests {
		if got := satisfies(tt.version, tt.rangeSpec); got != tt.want {
			t.Errorf("satisfies(%q, %q) = %v, want %v", tt.version, tt.rangeSpec, got, tt.want)
		}
	}
}

func TestVerifiedElementsTokensTable(t *testing.T) {
	// This is the verified table from docs/PLAN.md section 5.3. The generated
	// mapping must reproduce it from each release's dependency range.
	tests := []struct {
		rangeSpec string
		want      string
	}{
		{"^1.0.0", "1.1.2"},
		{"^1.1.2", "1.1.2"},
		{"^2.0.1", "2.2.4"},
		{"^2.1.1", "2.2.4"},
		{"^3.0.0", "3.2.0"},
		{"^3.0.1", "3.2.0"},
		{"^3.1.0", "3.2.0"},
		{"^3.2.0", "3.2.0"},
	}
	versions := []string{
		"1.0.0", "1.1.0", "1.1.1", "1.1.2",
		"2.0.0", "2.0.1", "2.1.0", "2.1.1", "2.2.0", "2.2.1", "2.2.2", "2.2.3", "2.2.4",
		"3.0.0", "3.0.1", "3.0.2", "3.1.0", "3.2.0",
	}
	for _, tt := range tests {
		got, ok := HighestSatisfying(tt.rangeSpec, versions)
		if !ok || got != tt.want {
			t.Errorf("range %s = %q (ok=%v), want %q", tt.rangeSpec, got, ok, tt.want)
		}
	}
}

func TestGeneratedMappingFromReleaseRanges(t *testing.T) {
	// Derived from every stable elements release's package.json during
	// `make data-update`, not hand-maintained in production code. The ranges
	// are the ones verified in docs/PLAN.md section 5.3.
	encoded, err := os.ReadFile(filepath.Join("..", "..", "testdata", "elements-tokens-ranges.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Version string `json:"version"`
		Range   string `json:"range"`
	}
	if err := json.Unmarshal(encoded, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 44 {
		t.Fatalf("fixture has %d elements releases, want 44", len(entries))
	}

	tokensVersions := []string{
		"1.0.0", "1.1.0", "1.1.1", "1.1.2",
		"2.0.0", "2.0.1", "2.1.0", "2.1.1", "2.2.0", "2.2.1", "2.2.2", "2.2.3", "2.2.4",
		"3.0.0", "3.0.1", "3.0.2", "3.1.0", "3.2.0",
	}
	expected := map[string]string{
		"^1.0.0": "1.1.2",
		"^1.1.2": "1.1.2",
		"^2.0.1": "2.2.4",
		"^2.1.0": "2.2.4",
		"^2.1.1": "2.2.4",
		"^3.0.0": "3.2.0",
		"^3.0.1": "3.2.0",
		"^3.0.2": "3.2.0",
		"^3.1.0": "3.2.0",
		"^3.2.0": "3.2.0",
	}
	observedRanges := make(map[string]bool)
	for _, entry := range entries {
		observedRanges[entry.Range] = true
		want, ok := expected[entry.Range]
		if !ok {
			t.Errorf("elements %s has an unexpected @rhds/tokens range %q", entry.Version, entry.Range)
			continue
		}
		got, found := HighestSatisfying(entry.Range, tokensVersions)
		if !found || got != want {
			t.Errorf("elements %s range %s -> %q (found=%v), want %s", entry.Version, entry.Range, got, found, want)
		}
	}
	for want := range expected {
		if !observedRanges[want] {
			t.Errorf("verified range %s does not appear in any release", want)
		}
	}
}

func TestDigestIsStableAndChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.lock.json")
	l := &Lock{
		SchemaVersion: SchemaVersion,
		Packages: []Package{{
			Name: "@rhds/elements",
			ID:   "elements",
			Versions: []PackageVersion{{
				Version:   "5.0.0",
				Integrity: testIntegrity,
				Tarball:   "https://example.test/elements-5.0.0.tgz",
			}},
		}},
		Docs: Docs{
			Repo: "RedHat-UX/red-hat-design-system",
			Snapshots: []DocSnapshot{{
				Tag:    "v5.0.0",
				Commit: "d2321d5a1feb5b677682305dee2c073422eda67c",
			}},
		},
	}
	if err := l.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if err := Save(path, l); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	second, err := loaded.Canonical()
	if err != nil {
		t.Fatalf("Canonical() = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("Save() and Canonical() differ:\n%s\n%s", first, second)
	}
	d1, err := loaded.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, err := loaded.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("Digest() not stable: %s != %s", d1, d2)
	}

	loaded.Packages[0].Versions[0].Version = "5.0.1"
	d3, err := loaded.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if d3 == d1 {
		t.Fatal("Digest() did not change with content")
	}
}

func TestLoadRejectsMalformedLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.lock.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() = nil error, want error for unsupported schema")
	}
}

func TestValidateRejectsMissingFields(t *testing.T) {
	base := func() *Lock {
		return &Lock{
			SchemaVersion: SchemaVersion,
			Packages: []Package{{
				Name: "@rhds/elements",
				ID:   "elements",
				Versions: []PackageVersion{{
					Version:   "5.0.0",
					Integrity: testIntegrity,
					Tarball:   "https://example.test/x.tgz",
				}},
			}},
			Docs: Docs{Repo: "RedHat-UX/red-hat-design-system"},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("baseline Validate() = %v", err)
	}

	l := base()
	l.Packages[0].Versions[0].Integrity = ""
	if err := l.Validate(); err == nil {
		t.Error("Validate() accepted a package version without integrity")
	}

	l = base()
	l.Packages[0].ID = ""
	if err := l.Validate(); err == nil {
		t.Error("Validate() accepted a package without an ID")
	}

	l = base()
	l.Docs.Repo = ""
	if err := l.Validate(); err == nil {
		t.Error("Validate() accepted an empty docs repo")
	}
}

func TestValidateRejectsPrereleasesAndBadDocTags(t *testing.T) {
	base := func() *Lock {
		return &Lock{
			SchemaVersion: SchemaVersion,
			Packages: []Package{{
				Name: "@rhds/elements",
				ID:   "elements",
				Versions: []PackageVersion{{
					Version:   "5.0.0",
					Integrity: testIntegrity,
					Tarball:   "https://example.test/x.tgz",
				}},
			}},
			Docs: Docs{
				Repo: "RedHat-UX/red-hat-design-system",
				Snapshots: []DocSnapshot{{
					Tag:    "v5.0.0",
					Commit: "d2321d5a1feb5b677682305dee2c073422eda67c",
				}},
			},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("baseline Validate() = %v", err)
	}

	l := base()
	l.Packages[0].Versions[0].Version = "5.1.0-beta.1"
	if err := l.Validate(); err == nil {
		t.Error("Validate() accepted a prerelease package version")
	}

	for _, tag := range []string{
		"../../escape",
		"v1.4.5/../../escape",
		"v1.4.5-beta.1",
		"1.4.5",
		"v1.4",
		`v1.4.5\..\escape`,
	} {
		l := base()
		l.Docs.Snapshots[0].Tag = tag
		if err := l.Validate(); err == nil {
			t.Errorf("Validate() accepted docs tag %q", tag)
		}
	}
	if !ValidDocTag("v1.4.5") {
		t.Error("ValidDocTag(v1.4.5) = false")
	}
}

func TestValidateRejectsBadIntegrity(t *testing.T) {
	base := func(integrity string) *Lock {
		return &Lock{
			SchemaVersion: SchemaVersion,
			Packages: []Package{{
				Name: "@rhds/elements",
				ID:   "elements",
				Versions: []PackageVersion{{
					Version:   "5.0.0",
					Integrity: integrity,
					Tarball:   "https://example.test/x.tgz",
				}},
			}},
			Docs: Docs{Repo: "RedHat-UX/red-hat-design-system"},
		}
	}
	if err := base(testIntegrity).Validate(); err != nil {
		t.Fatalf("baseline Validate() = %v", err)
	}
	for _, integrity := range []string{
		"sha256-abc",
		"sha512-not-base64!",
		"sha512-YWJj",
		"sha512-" + base64.StdEncoding.EncodeToString(make([]byte, sha512.Size-1)),
	} {
		if err := base(integrity).Validate(); err == nil {
			t.Errorf("Validate() accepted integrity %q", integrity)
		}
	}
}

func TestValidateRejectsDuplicateDocTagsAndBadSHAs(t *testing.T) {
	base := func() *Lock {
		return &Lock{
			SchemaVersion: SchemaVersion,
			Packages: []Package{{
				Name: "@rhds/elements",
				ID:   "elements",
				Versions: []PackageVersion{{
					Version:   "5.0.0",
					Integrity: testIntegrity,
					Tarball:   "https://example.test/x.tgz",
				}},
			}},
			Docs: Docs{
				Repo: "RedHat-UX/red-hat-design-system",
				Snapshots: []DocSnapshot{{
					Tag:    "v5.0.0",
					Commit: "d2321d5a1feb5b677682305dee2c073422eda67c",
				}},
			},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("baseline Validate() = %v", err)
	}

	l := base()
	l.Docs.Snapshots = append(l.Docs.Snapshots, l.Docs.Snapshots[0])
	if err := l.Validate(); err == nil {
		t.Error("Validate() accepted duplicate docs tags")
	}

	for _, commitment := range []string{
		"D2321D5A1FEB5B677682305DEE2C073422EDA67C",
		"d2321d5a1feb5b677682305dee2c073422eda67",
		"g2321d5a1feb5b677682305dee2c073422eda67c",
		"d2321d5a1feb5b677682305dee2c073422eda67c00",
	} {
		l := base()
		l.Docs.Snapshots[0].Commit = commitment
		if err := l.Validate(); err == nil {
			t.Errorf("Validate() accepted commit %q", commitment)
		}
	}

	l = base()
	l.Docs.Snapshots[0].AIGuidelinesCommit = strings.Repeat("A", 40)
	if err := l.Validate(); err == nil {
		t.Error("Validate() accepted an uppercase ai-guidelines commit")
	}
}

func TestCanonicalJSONIsSorted(t *testing.T) {
	l := &Lock{
		SchemaVersion: SchemaVersion,
		Packages: []Package{
			{Name: "@rhds/tokens", ID: "tokens"},
			{Name: "@rhds/elements", ID: "elements"},
		},
		Docs: Docs{Repo: "RedHat-UX/red-hat-design-system"},
	}
	encoded, err := l.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	packages := generic["packages"].([]any)
	if got := packages[0].(map[string]any)["id"]; got != "elements" {
		t.Fatalf("first package = %v, want elements (sorted by id)", got)
	}
}
