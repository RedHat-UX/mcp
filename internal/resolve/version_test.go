// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"testing"
)

func TestParseVersionSpec(t *testing.T) {
	tests := []struct {
		spec      string
		want      string
		major     int
		minor     int
		patch     int
		partial   bool
		hasMinor  bool
		parseable bool
	}{
		{"5.0.0", "5.0.0", 5, 0, 0, false, true, true},
		{"^5.0.0", "5.0.0", 5, 0, 0, false, true, true},
		{"~4.2.2", "4.2.2", 4, 2, 2, false, true, true},
		{">=3.1.0 <4", "3.1.0", 3, 1, 0, false, true, true},
		{"v5.0.0", "5.0.0", 5, 0, 0, false, true, true},
		{"npm:@rhds/elements@5.0.0", "5.0.0", 5, 0, 0, false, true, true},
		{"workspace:^5.0.0", "5.0.0", 5, 0, 0, false, true, true},
		{"https://cdn.jsdelivr.net/npm/@rhds/elements@5.0.0/+esm", "5.0.0", 5, 0, 0, false, true, true},
		{"5.0.0-beta.1", "5.0.0-beta.1", 5, 0, 0, false, true, true},
		{"^4", "4", 4, 0, 0, true, false, true},
		{"4", "4", 4, 0, 0, true, false, true},
		{"4.x", "4", 4, 0, 0, true, false, true},
		{"4.*", "4", 4, 0, 0, true, false, true},
		{"~4", "4", 4, 0, 0, true, false, true},
		{"~4.2", "4.2", 4, 2, 0, true, true, true},
		{"4.2", "4.2", 4, 2, 0, true, true, true},
		{"npm:@rhds/elements@^4", "4", 4, 0, 0, true, false, true},
		{"latest", "", 0, 0, 0, false, false, false},
		{"*", "", 0, 0, 0, false, false, false},
		{"file:../elements", "", 0, 0, 0, false, false, false},
		{"catalog:", "", 0, 0, 0, false, false, false},
		{"workspace:*", "", 0, 0, 0, false, false, false},
		{"", "", 0, 0, 0, false, false, false},
	}
	for _, test := range tests {
		t.Run(test.spec, func(t *testing.T) {
			parsed := ParseVersionSpec(test.spec)
			if parsed.OK != test.parseable {
				t.Fatalf("ParseVersionSpec(%q).OK = %v, want %v", test.spec, parsed.OK, test.parseable)
			}
			if !test.parseable {
				return
			}
			if parsed.Text != test.want {
				t.Errorf("text = %q, want %q", parsed.Text, test.want)
			}
			if parsed.Major != test.major || parsed.Minor != test.minor || parsed.Patch != test.patch {
				t.Errorf("version = %d.%d.%d, want %d.%d.%d",
					parsed.Major, parsed.Minor, parsed.Patch, test.major, test.minor, test.patch)
			}
			if parsed.Partial != test.partial {
				t.Errorf("partial = %v, want %v", parsed.Partial, test.partial)
			}
			if parsed.HasMinor != test.hasMinor {
				t.Errorf("hasMinor = %v, want %v", parsed.HasMinor, test.hasMinor)
			}
		})
	}
}

func TestCompareParsed(t *testing.T) {
	low := ParseVersionSpec("4.9.9")
	high := ParseVersionSpec("5.0.0")
	if CompareParsed(low, high) >= 0 {
		t.Errorf("CompareParsed(%s, %s) = %d, want -1", low.Text, high.Text, CompareParsed(low, high))
	}
	patch := ParseVersionSpec("5.0.1")
	if CompareParsed(patch, high) <= 0 {
		t.Errorf("CompareParsed(%s, %s) = %d, want 1", patch.Text, high.Text, CompareParsed(patch, high))
	}
	rangeMajor := ParseVersionSpec("^5")
	if CompareParsed(rangeMajor, ParseVersionSpec("5.0.0")) != 0 {
		t.Errorf("partial major should compare equal to its zero components")
	}
}

func FuzzParseVersionSpec(f *testing.F) {
	for _, seed := range []string{
		"5.0.0", "^5.0.0", "workspace:*", "npm:@rhds/elements@5.0.0",
		"https://example.test/@rhds/elements@4.2.2/x.js", "",
		"999999999999999999999999999999.0.0", "..", "@@@",
		"^4", "4.x", "~4.2", "npm:@rhds/elements@^3",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		parsed := ParseVersionSpec(spec)
		if parsed.OK {
			if parsed.Major < 0 || parsed.Minor < 0 || parsed.Patch < 0 {
				t.Fatalf("negative component in %+v", parsed)
			}
			if parsed.Text == "" {
				t.Fatalf("parseable version has empty text: %+v", parsed)
			}
			// Parsing must be stable.
			again := ParseVersionSpec(spec)
			if again != parsed {
				t.Fatalf("unstable parse: %+v then %+v", parsed, again)
			}
		}
	})
}
