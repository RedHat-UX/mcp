// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"fmt"
	"testing"
)

func TestParseLockfileData(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []string
	}{
		{"package-lock.json", packageLockV3("5.0.0"), []string{"5.0.0"}},
		{"package-lock.json", `{"lockfileVersion":2,"packages":{"node_modules/@rhds/elements":{"version":"4.2.2"}},"dependencies":{"@rhds/elements":{"version":"4.2.2"}}}`, []string{"4.2.2"}},
		{"pnpm-lock.yaml", "lockfileVersion: '9.0'\npackages:\n  '@rhds/elements@4.2.2': {}\n", []string{"4.2.2"}},
		{"pnpm-lock.yaml", "lockfileVersion: '9.0'\nsnapshots:\n  '@rhds/elements@5.0.0(react@18.0.0)': {}\n", []string{"5.0.0"}},
		{"yarn.lock", "\"@rhds/elements@^5.0.0\":\n  version \"5.0.0\"\n", []string{"5.0.0"}},
		{"bun.lock", `{"packages":{"@rhds/elements":["@rhds/elements@5.0.0"]}}`, []string{"5.0.0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates, truncated, err := parseLockfileData(test.name, []byte(test.data), DefaultLimits())
			if err != nil {
				t.Fatalf("parseLockfileData() error = %v", err)
			}
			if truncated {
				t.Fatal("truncated = true, want false")
			}
			if len(candidates) != len(test.want) {
				t.Fatalf("candidates = %+v, want versions %v", candidates, test.want)
			}
			for i, version := range test.want {
				if candidates[i].Version != version {
					t.Errorf("candidate %d = %+v, want version %s", i, candidates[i], version)
				}
				if candidates[i].Source != test.name {
					t.Errorf("candidate %d source = %q, want %q", i, candidates[i].Source, test.name)
				}
			}
		})
	}
}

func TestPNPMPeerRangesAreNotPins(t *testing.T) {
	data := `lockfileVersion: '9.0'
importers:
  .:
    dependencies:
      '@rhds/elements':
        specifier: ^4.0.0
        version: 4.2.2
    peerDependencies:
      '@rhds/elements': ^5.0.0
`
	candidates, _, err := parseLockfileData("pnpm-lock.yaml", []byte(data), DefaultLimits())
	if err != nil {
		t.Fatalf("parseLockfileData() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Version != "4.2.2" {
		t.Fatalf("candidates = %+v, want only the resolved 4.2.2", candidates)
	}
}

func TestPNPMV5AndV6PackageKeys(t *testing.T) {
	data := `lockfileVersion: '6.0'
packages:
  /@rhds/elements/4.2.2:
    resolution: {integrity: sha512-x}
  /@rhds/elements@5.0.0:
    resolution: {integrity: sha512-y}
`
	candidates, _, err := parseLockfileData("pnpm-lock.yaml", []byte(data), DefaultLimits())
	if err != nil {
		t.Fatalf("parseLockfileData() error = %v", err)
	}
	got := make(map[string]bool)
	for _, candidate := range candidates {
		got[candidate.Version] = true
	}
	if !got["4.2.2"] || !got["5.0.0"] {
		t.Fatalf("candidates = %+v, want 4.2.2 and 5.0.0", candidates)
	}
}

func TestBunWorkspaceRangesAreNotPins(t *testing.T) {
	data := `{
  "lockfileVersion": 1,
  "workspaces": {
    "": {"dependencies": {"@rhds/elements": "^5.0.0"}},
  },
  "packages": {
    "@rhds/elements": ["@rhds/elements@4.2.2", "", {}, "sha512-x"],
  },
}`
	candidates, _, err := parseLockfileData("bun.lock", []byte(data), DefaultLimits())
	if err != nil {
		t.Fatalf("parseLockfileData() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Version != "4.2.2" {
		t.Fatalf("candidates = %+v, want only the resolved 4.2.2", candidates)
	}
}

func TestCandidateTruncationIsDeterministicAndWarned(t *testing.T) {
	packages := ""
	for i := 0; i < maxCandidates+10; i++ {
		if packages != "" {
			packages += ","
		}
		packages += fmt.Sprintf(`"node_modules/pkg%d/node_modules/@rhds/elements":{"version":"5.0.%d"}`, i, i)
	}
	data := `{"lockfileVersion":3,"packages":{` + packages + `}}`
	first, truncated, err := parseLockfileData("package-lock.json", []byte(data), DefaultLimits())
	if err != nil {
		t.Fatalf("parseLockfileData() error = %v", err)
	}
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	second, _, err := parseLockfileData("package-lock.json", []byte(data), DefaultLimits())
	if err != nil {
		t.Fatalf("parseLockfileData() second error = %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("lengths differ: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("candidate %d differs between runs: %+v then %+v", i, first[i], second[i])
		}
	}
}

func TestParseLockfileDataRejectsOversizedInput(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxFileBytes = 8
	_, _, err := parseLockfileData("package-lock.json", []byte(packageLockV3("5.0.0")), limits)
	if err == nil {
		t.Fatal("parseLockfileData() error = nil, want size error")
	}
}

func FuzzParseLockfileData(f *testing.F) {
	seeds := []struct {
		name string
		data string
	}{
		{"package-lock.json", packageLockV3("5.0.0")},
		{"package-lock.json", `{"lockfileVersion":1,"dependencies":{"@rhds/elements":{"version":"4.0.0"}}}`},
		{"pnpm-lock.yaml", "importers:\n  .:\n    dependencies:\n      '@rhds/elements':\n        version: 5.0.0\n"},
		{"pnpm-lock.yaml", "packages:\n  /@rhds/elements/4.2.2: {}\n"},
		{"yarn.lock", "\"@rhds/elements@^5.0.0\":\n  version \"5.0.0\"\n"},
		{"yarn.lock", "\"@rhds/elements@npm:^4.0.0\":\n  version: 4.2.2\n"},
		{"bun.lock", `{"packages":{"@rhds/elements":["@rhds/elements@5.0.0"]}}`},
		{"bun.lock", `{`},
		{"package-lock.json", `{"packages":{"node_modules/@rhds/elements":{"version":"9.9.9"}}}`},
	}
	for _, seed := range seeds {
		f.Add(seed.name, seed.data)
	}
	f.Fuzz(func(t *testing.T, name, data string) {
		candidates, _, err := parseLockfileData(name, []byte(data), DefaultLimits())
		if err != nil {
			return
		}
		for _, candidate := range candidates {
			if candidate.Source != name {
				t.Fatalf("candidate source = %q, want %q", candidate.Source, name)
			}
			if candidate.Version == "" && candidate.Spec == "" {
				t.Fatalf("candidate has no version or spec: %+v", candidate)
			}
			if len(candidate.Version) > maxCandidateRunes+3 || len(candidate.Spec) > maxCandidateRunes+3 {
				t.Fatalf("candidate string was not truncated: %+v", candidate)
			}
		}
	})
}
