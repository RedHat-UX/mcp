// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testBundled = []string{"5.1.0", "5.0.0", "4.2.2", "4.0.0", "3.2.0"}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating directory for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}

func resolveIn(t *testing.T, root string, explicit string) Result {
	t.Helper()
	result, err := Resolve(Options{
		Root:            root,
		Explicit:        explicit,
		BundledElements: testBundled,
		Limits:          DefaultLimits(),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	return result
}

func hasCandidate(result Result, source, version string) bool {
	for _, candidate := range result.Candidates {
		if candidate.Source == source && candidate.Version == version {
			return true
		}
	}
	return false
}

func TestExplicitVersionSkipsWorkspace(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package-lock.json": packageLockV3("4.0.0"),
		"package.json":      `{"dependencies":{"@rhds/elements":"^3.0.0"}}`,
	})
	result := resolveIn(t, root, "5.0.0")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q, want 5.0.0", result.Elements)
	}
	if result.Source != "version argument" {
		t.Errorf("source = %q, want version argument", result.Source)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Level != LevelExplicit {
		t.Errorf("candidates = %+v, want one explicit candidate", result.Candidates)
	}
	if containsDiffSource(result.Candidates, "package-lock.json") {
		t.Error("explicit version should skip workspace reads")
	}
}

func TestPackageLockV3Hoisted(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"package-lock.json": packageLockV3("5.0.0")})
	result := resolveIn(t, root, "")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q, want 5.0.0", result.Elements)
	}
	if result.Source != "package-lock.json" {
		t.Errorf("source = %q", result.Source)
	}
	candidate := result.Candidates[0]
	if candidate.Precedence != PrecedenceLockfile || candidate.Level != LevelLockfile {
		t.Errorf("candidate = %+v", candidate)
	}
	if !hasCandidate(result, "package-lock.json", "5.0.0") {
		t.Errorf("candidates = %+v", result.Candidates)
	}
}

func TestPackageLockV1NestedDependencies(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package-lock.json": `{
			"lockfileVersion": 1,
			"dependencies": {
				"lit": {"version": "3.0.0"},
				"@rhds/elements": {"version": "4.2.2"}
			}
		}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "4.2.2" {
		t.Errorf("elements = %q, want 4.2.2", result.Elements)
	}
}

func TestPackageLockMajorDisagreementIsAmbiguous(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package-lock.json": `{
			"lockfileVersion": 3,
			"packages": {
				"node_modules/@rhds/elements": {"version": "4.2.2"},
				"packages/a/node_modules/@rhds/elements": {"version": "5.0.0"}
			}
		}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "" {
		t.Errorf("elements = %q, want empty while ambiguous", result.Elements)
	}
	if result.Ambiguity == nil {
		t.Fatal("ambiguity = nil, want candidates")
	}
	if len(result.Ambiguity.Candidates) != 2 {
		t.Errorf("ambiguous candidates = %+v", result.Ambiguity.Candidates)
	}
	if len(result.Ambiguity.Majors) != 2 || result.Ambiguity.Majors[0] != 4 || result.Ambiguity.Majors[1] != 5 {
		t.Errorf("majors = %v, want [4 5]", result.Ambiguity.Majors)
	}
}

func TestPnPMLockfileImportersAndPackages(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-lock.yaml": `lockfileVersion: '9.0'
importers:
  .:
    dependencies:
      '@rhds/elements':
        specifier: ^5.0.0
        version: 5.0.0
  packages/app:
    devDependencies:
      '@rhds/elements':
        specifier: ^4.0.0
        version: 4.2.2
packages:
  '@rhds/elements@5.0.0':
    resolution: {integrity: sha512-x}
  '@rhds/elements@4.2.2':
    resolution: {integrity: sha512-y}
`,
	})
	result := resolveIn(t, root, "")
	if result.Ambiguity == nil {
		t.Fatalf("ambiguity = nil, want importer disagreement: %+v", result)
	}
	if len(result.Ambiguity.Majors) != 2 {
		t.Errorf("majors = %v, want two majors", result.Ambiguity.Majors)
	}
}

func TestYarnClassicLockfile(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"yarn.lock": `# yarn lockfile v1

"@rhds/elements@^5.0.0":
  version "5.0.0"
  resolved "https://registry.yarnpkg.com/@rhds/elements/-/elements-5.0.0.tgz"
`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q, want 5.0.0", result.Elements)
	}
	if result.Source != "yarn.lock" {
		t.Errorf("source = %q", result.Source)
	}
}

func TestYarnBerryLockfile(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"yarn.lock": `# This file is generated by running "yarn install" inside your project.
__metadata:
  version: 8

"@rhds/elements@npm:^4.0.0":
  version: 4.2.2
  resolution: "@rhds/elements@npm:4.2.2"
`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "4.2.2" {
		t.Errorf("elements = %q, want 4.2.2", result.Elements)
	}
}

func TestBunLockfile(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"bun.lock": `{
  // Bun's text lockfile is JSONC.
  "lockfileVersion": 1,
  "workspaces": {
    "": {"dependencies": {"@rhds/elements": "^5.0.0"}},
  },
  "packages": {
    "@rhds/elements": ["@rhds/elements@5.0.0", "", {}, "sha512-x"],
  },
}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q, want 5.0.0", result.Elements)
	}
	if result.Source != "bun.lock" {
		t.Errorf("source = %q", result.Source)
	}
}

func TestInstalledHoistedPackage(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"node_modules/@rhds/elements/package.json": `{"name":"@rhds/elements","version":"5.0.0"}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q", result.Elements)
	}
	if result.Source != "node_modules/@rhds/elements/package.json" {
		t.Errorf("source = %q", result.Source)
	}
	if !hasCandidate(result, "node_modules/@rhds/elements/package.json", "5.0.0") {
		t.Errorf("candidates = %+v", result.Candidates)
	}
}

func TestInstalledNestedWorkspacePackage(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json": `{"workspaces":["packages/app"]}`,
		"packages/app/node_modules/@rhds/elements/package.json": `{"name":"@rhds/elements","version":"4.2.2"}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "4.2.2" {
		t.Errorf("elements = %q, want 4.2.2", result.Elements)
	}
	if !strings.Contains(result.Source, "packages/app/node_modules") {
		t.Errorf("source = %q", result.Source)
	}
}

func TestWorkspacePackageJSONDependency(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json":              `{"private":true,"workspaces":["packages/app"]}`,
		"packages/app/package.json": `{"dependencies":{"@rhds/elements":"^4.0.0"}}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "4.0.0" {
		t.Errorf("elements = %q, want 4.0.0", result.Elements)
	}
	if result.Source != "packages/app/package.json" {
		t.Errorf("source = %q", result.Source)
	}
}

func TestPNPMWorkspacePackageDependency(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json":              `{"private":true}`,
		"pnpm-workspace.yaml":       "packages:\n  - packages/*\n",
		"packages/app/package.json": `{"dependencies":{"@rhds/elements":"^5.0.0"}}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q, want 5.0.0 (from pnpm workspace package)", result.Elements)
	}
	if result.Source != "packages/app/package.json" {
		t.Errorf("source = %q", result.Source)
	}
}

func TestPNPMWorkspaceInstalledPackage(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml": "packages:\n  - packages/*\n",
		"packages/app/node_modules/@rhds/elements/package.json": `{"name":"@rhds/elements","version":"5.0.0"}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q", result.Elements)
	}
	if !strings.Contains(result.Source, "packages/app/node_modules") {
		t.Errorf("source = %q", result.Source)
	}
}

func TestOverridesAndResolutions(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json": `{
			"dependencies": {"@rhds/elements": "^5.0.0"},
			"overrides": {"@rhds/elements": "5.0.0"},
			"resolutions": {"@rhds/elements": "5.1.0"}
		}`,
	})
	result := resolveIn(t, root, "")
	// All three are package.json-level evidence; the highest wins.
	if result.Elements != "5.1.0" {
		t.Errorf("elements = %q, want 5.1.0", result.Elements)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "highest") {
		t.Errorf("warnings = %v, want a highest-version warning", result.Warnings)
	}
}

func TestConflictingPackageJSONMajorsAreAmbiguous(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json": `{
			"dependencies": {"@rhds/elements": "^4.0.0"},
			"overrides": {"@rhds/elements": "5.0.0"}
		}`,
	})
	result := resolveIn(t, root, "")
	if result.Ambiguity == nil {
		t.Fatalf("ambiguity = nil, want conflicting package.json evidence: %+v", result)
	}
	if len(result.Ambiguity.Majors) != 2 {
		t.Errorf("majors = %v, want two majors", result.Ambiguity.Majors)
	}
}

func TestPNPMCatalogsResolveOnlyReferencedCatalog(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json": `{"dependencies":{"@rhds/elements":"catalog:legacy"}}`,
		"pnpm-workspace.yaml": `packages:
  - packages/*
catalog:
  '@rhds/elements': ^5.0.0
catalogs:
  legacy:
    '@rhds/elements': 4.2.2
`,
	})
	result := resolveIn(t, root, "")
	if result.Ambiguity != nil {
		t.Fatalf("ambiguity = %+v, want only the referenced legacy catalog", result.Ambiguity)
	}
	if result.Elements != "4.2.2" {
		t.Errorf("elements = %q, want the referenced legacy catalog 4.2.2", result.Elements)
	}
}

func TestPNPMDefaultCatalog(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json":        `{"dependencies":{"@rhds/elements":"catalog:"}}`,
		"pnpm-workspace.yaml": "catalog:\n  '@rhds/elements': ^5.0.0\n",
	})
	result := resolveIn(t, root, "")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q, want the default catalog 5.0.0", result.Elements)
	}
}

func TestUnreferencedNamedCatalogIsIgnored(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json": `{"dependencies":{"@rhds/elements":"^5.0.0"}}`,
		"pnpm-workspace.yaml": `catalog:
  '@rhds/elements': ^5.0.0
catalogs:
  legacy:
    '@rhds/elements': 4.2.2
`,
	})
	result := resolveIn(t, root, "")
	if result.Ambiguity != nil {
		t.Fatalf("ambiguity = %+v, want the unreferenced legacy catalog ignored", result.Ambiguity)
	}
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q, want 5.0.0", result.Elements)
	}
}

func TestTemplateImportMap(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"index.html": `<script type="importmap">
			{"imports":{"@rhds/elements":"https://cdn.jsdelivr.net/npm/@rhds/elements@5.0.0/+esm"}}
		</script>`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q", result.Elements)
	}
	if result.Source != "index.html" {
		t.Errorf("source = %q", result.Source)
	}
}

func TestTemplateCDNReferenceInTwig(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"templates/card.html.twig": `{% if show %}<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@rhds/elements@4.2.2/rhds.min.css">{% endif %}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "4.2.2" {
		t.Errorf("elements = %q", result.Elements)
	}
	if result.Source != "templates/card.html.twig" {
		t.Errorf("source = %q", result.Source)
	}
}

func TestTemplateScanSkipsNodeModules(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"node_modules/example/index.html": `<script type="importmap">{"imports":{"@rhds/elements":"https://cdn.jsdelivr.net/npm/@rhds/elements@9.9.9/+esm"}}</script>`,
	})
	result := resolveIn(t, root, "")
	if result.Source != "bundled" {
		t.Errorf("source = %q, want bundled (node_modules must be skipped)", result.Source)
	}
}

func TestTemplateScanDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFiles(t, outside, map[string]string{
		"escape.html": `<script type="importmap">{"imports":{"@rhds/elements":"https://cdn.jsdelivr.net/npm/@rhds/elements@9.9.9/+esm"}}</script>`,
	})
	if err := os.Symlink(filepath.Join(outside, "escape.html"), filepath.Join(root, "escape.html")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result := resolveIn(t, root, "")
	if result.Source != "bundled" {
		t.Errorf("source = %q, want bundled (symlinks must not be followed)", result.Source)
	}
}

func TestTemplateScanRespectsFileCap(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for _, name := range []string{"a.html", "b.html", "c.html", "d.html", "e.html"} {
		files[name] = `<script type="importmap">{"imports":{"@rhds/elements":"https://cdn.jsdelivr.net/npm/@rhds/elements@5.0.0/+esm"}}</script>`
	}
	writeFiles(t, root, files)
	limits := DefaultLimits()
	limits.MaxScanFiles = 2
	result, err := Resolve(Options{Root: root, BundledElements: testBundled, Limits: limits})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "scan stopped") {
		t.Errorf("warnings = %v, want scan cap warning", result.Warnings)
	}
}

func TestLockfileOutranksInstalledPackage(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package-lock.json":                        packageLockV3("4.2.2"),
		"node_modules/@rhds/elements/package.json": `{"name":"@rhds/elements","version":"5.0.0"}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "4.2.2" {
		t.Errorf("elements = %q, want lockfile 4.2.2", result.Elements)
	}
	if result.Source != "package-lock.json" {
		t.Errorf("source = %q", result.Source)
	}
}

func TestMinorDisagreementPicksHighestAndWarns(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json": `{
			"dependencies": {"@rhds/elements": "^5.0.0"},
			"devDependencies": {"@rhds/elements": "~5.1.0"}
		}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "5.1.0" {
		t.Errorf("elements = %q, want highest 5.1.0", result.Elements)
	}
	if result.Ambiguity != nil {
		t.Errorf("ambiguity = %+v, want nil for a minor disagreement", result.Ambiguity)
	}
	joined := strings.Join(result.Warnings, " ")
	if !strings.Contains(joined, "highest") {
		t.Errorf("warnings = %v, want a highest-version warning", result.Warnings)
	}
}

func TestNoEvidenceUsesBundledDefaultWithWarning(t *testing.T) {
	root := t.TempDir()
	result := resolveIn(t, root, "")
	if result.Elements != "5.1.0" {
		t.Errorf("elements = %q, want latest bundled 5.1.0", result.Elements)
	}
	if result.Source != "bundled" {
		t.Errorf("source = %q", result.Source)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Level != LevelBundled {
		t.Errorf("candidates = %+v", result.Candidates)
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "latest bundled") {
		t.Errorf("warnings = %v", result.Warnings)
	}
}

func TestMalformedLockfileWarnsAndFallsBack(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"package-lock.json": `{not json`})
	result := resolveIn(t, root, "")
	if result.Source != "bundled" {
		t.Errorf("source = %q, want bundled", result.Source)
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "package-lock.json") {
		t.Errorf("warnings = %v, want a parse warning", result.Warnings)
	}
}

func TestOversizedLockfileIsSkipped(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"package-lock.json": strings.Repeat(" ", 1024) + packageLockV3("5.0.0")})
	limits := DefaultLimits()
	limits.MaxFileBytes = 128
	result, err := Resolve(Options{Root: root, BundledElements: testBundled, Limits: limits})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if result.Source != "bundled" {
		t.Errorf("source = %q, want bundled", result.Source)
	}
}

func TestInvalidRoot(t *testing.T) {
	_, err := Resolve(Options{Root: filepath.Join(t.TempDir(), "missing"), BundledElements: testBundled})
	if err == nil {
		t.Fatal("Resolve() error = nil, want invalid root error")
	}
}

func TestPartialRangesResolveToHighestBundled(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want string
	}{
		{"caret major", "^4", "4.2.2"},
		{"bare major", "4", "4.2.2"},
		{"x major", "4.x", "4.2.2"},
		{"tilde major", "~4", "4.2.2"},
		{"tilde minor", "~4.0", "4.0.0"},
		{"npm alias partial", "npm:@rhds/elements@^4", "4.2.2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, map[string]string{
				"package.json": `{"dependencies":{"@rhds/elements":"` + test.spec + `"}}`,
			})
			result := resolveIn(t, root, "")
			if result.Elements != test.want {
				t.Errorf("elements = %q, want %q", result.Elements, test.want)
			}
		})
	}
}

func TestExplicitPartialRangeResolvesToBundled(t *testing.T) {
	result := resolveIn(t, t.TempDir(), "^4")
	if result.Elements != "4.2.2" {
		t.Errorf("elements = %q, want 4.2.2", result.Elements)
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "resolved to the highest bundled") {
		t.Errorf("warnings = %v", result.Warnings)
	}
}

func TestMatchingRangeAndExactVersionDoesNotWarn(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json": `{
			"dependencies": {"@rhds/elements": "^4"},
			"devDependencies": {"@rhds/elements": "4.2.2"}
		}`,
	})
	result := resolveIn(t, root, "")
	if result.Elements != "4.2.2" {
		t.Errorf("elements = %q", result.Elements)
	}
	if strings.Contains(strings.Join(result.Warnings, " "), "disagree") {
		t.Errorf("warnings = %v, want no disagreement warning when sources resolve to the same version", result.Warnings)
	}
}

func TestOverrideSelectorKeys(t *testing.T) {
	tests := []struct {
		name        string
		packageJSON string
		want        string
	}{
		{
			name:        "yarn glob",
			packageJSON: `{"resolutions":{"**/@rhds/elements":"5.0.0"}}`,
			want:        "5.0.0",
		},
		{
			name:        "pnpm selector",
			packageJSON: `{"pnpm":{"overrides":{"@rhds/elements@^4.0.0":"4.2.2"}}}`,
			want:        "4.2.2",
		},
		{
			name:        "npm nested dot",
			packageJSON: `{"overrides":{"@rhds/elements":{".":"4.2.2"}}}`,
			want:        "4.2.2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, map[string]string{"package.json": test.packageJSON})
			result := resolveIn(t, root, "")
			if result.Elements != test.want {
				t.Errorf("elements = %q, want %q; candidates = %+v", result.Elements, test.want, result.Candidates)
			}
		})
	}
}

func TestOutOfRootSymlinkedAncestorIsRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFiles(t, outside, map[string]string{
		"node_modules/@rhds/elements/package.json":                                         `{"name":"@rhds/elements","version":"9.9.9"}`,
		"node_modules/.pnpm/@rhds+elements@9.9.9/node_modules/@rhds/elements/package.json": `{"name":"@rhds/elements","version":"9.9.9"}`,
	})
	if err := os.Symlink(filepath.Join(outside, "node_modules"), filepath.Join(root, "node_modules")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result := resolveIn(t, root, "")
	if result.Source != "bundled" {
		t.Errorf("source = %q, want bundled; an out-of-root symlink must not win", result.Source)
	}
	if hasCandidate(result, "node_modules/@rhds/elements/package.json", "9.9.9") {
		t.Errorf("candidates = %+v, want no out-of-root candidate", result.Candidates)
	}
}

func TestInRootPNPMSymlinkIsFollowed(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		".pnpm/@rhds+elements@5.0.0/node_modules/@rhds/elements/package.json": `{"name":"@rhds/elements","version":"5.0.0"}`,
	})
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "@rhds"), 0o755); err != nil {
		t.Fatalf("creating node_modules: %v", err)
	}
	target := filepath.Join(root, ".pnpm", "@rhds+elements@5.0.0", "node_modules", "@rhds", "elements")
	if err := os.Symlink(target, filepath.Join(root, "node_modules", "@rhds", "elements")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result := resolveIn(t, root, "")
	if result.Elements != "5.0.0" {
		t.Errorf("elements = %q, want the in-root pnpm target 5.0.0", result.Elements)
	}
	if !strings.Contains(result.Source, "node_modules/@rhds/elements/package.json") {
		t.Errorf("source = %q", result.Source)
	}
}

func TestTemplateScanRespectsEntryCap(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"zzz.html": `<script type="importmap">{"imports":{"@rhds/elements":"https://cdn.jsdelivr.net/npm/@rhds/elements@9.9.9/+esm"}}</script>`,
	}
	for i := 0; i < 300; i++ {
		files[fmt.Sprintf("noise-%03d.txt", i)] = "noise"
	}
	writeFiles(t, root, files)
	limits := DefaultLimits()
	limits.MaxScanEntries = 100
	limits.MaxScanFiles = 10
	start := time.Now()
	result, err := Resolve(Options{Root: root, BundledElements: testBundled, Limits: limits})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("template scan took %s, want bounded traversal", elapsed)
	}
	if result.Source != "bundled" {
		t.Errorf("source = %q, want bundled because the template is past the entry cap", result.Source)
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "entries") {
		t.Errorf("warnings = %v, want an entry-cap warning", result.Warnings)
	}
}

func TestMapTraversalDoesNotRandomizeSpec(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json": `{"overrides":{
			"@rhds/elements@^5.0.0": "5.0.0",
			"**/@rhds/elements": "5.0.0"
		}}`,
	})
	var previous string
	for i := 0; i < 20; i++ {
		result := resolveIn(t, root, "")
		if len(result.Candidates) != 1 {
			t.Fatalf("candidates = %+v", result.Candidates)
		}
		spec := result.Candidates[0].Spec
		if i > 0 && spec != previous {
			t.Fatalf("candidate spec changed between runs: %q then %q", previous, spec)
		}
		previous = spec
	}
}

func TestLockfileTruncationWarns(t *testing.T) {
	root := t.TempDir()
	packages := ""
	for i := 0; i < maxCandidates+5; i++ {
		if packages != "" {
			packages += ","
		}
		packages += fmt.Sprintf(`"node_modules/pkg%d/node_modules/@rhds/elements":{"version":"5.0.%d"}`, i, i)
	}
	writeFiles(t, root, map[string]string{
		"package-lock.json": `{"lockfileVersion":3,"packages":{` + packages + `}}`,
	})
	result := resolveIn(t, root, "")
	if !strings.Contains(strings.Join(result.Warnings, " "), "candidate collection for package-lock.json stopped") {
		t.Errorf("warnings = %v, want a per-lockfile truncation warning", result.Warnings)
	}
}

func packageLockV3(version string) string {
	return `{"lockfileVersion":3,"packages":{"node_modules/@rhds/elements":{"version":"` + version + `"}}}`
}

func containsDiffSource(candidates []Candidate, source string) bool {
	for _, candidate := range candidates {
		if candidate.Source == source {
			return true
		}
	}
	return false
}
