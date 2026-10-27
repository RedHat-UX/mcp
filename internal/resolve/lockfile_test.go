// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"testing"
)

func TestParseLockfileData(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []string
	}{
		{"package-lock.json", packageLockV3("5.0.0"), []string{"5.0.0"}},
		{"pnpm-lock.yaml", "lockfileVersion: '9.0'\npackages:\n  '@rhds/elements@4.2.2': {}\n", []string{"4.2.2"}},
		{"yarn.lock", "\"@rhds/elements@^5.0.0\":\n  version \"5.0.0\"\n", []string{"5.0.0"}},
		{"bun.lock", `{"packages":{"@rhds/elements":["@rhds/elements@5.0.0"]}}`, []string{"5.0.0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates, err := parseLockfileData(test.name, []byte(test.data), DefaultLimits())
			if err != nil {
				t.Fatalf("parseLockfileData() error = %v", err)
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

func TestParseLockfileDataRejectsOversizedInput(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxFileBytes = 8
	_, err := parseLockfileData("package-lock.json", []byte(packageLockV3("5.0.0")), limits)
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
		candidates, err := parseLockfileData(name, []byte(data), DefaultLimits())
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
		}
	})
}
