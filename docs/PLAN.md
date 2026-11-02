# RHDS MCP Server (@rhds/mcp): Product and Technical Requirements

Status: revised 2026-10-26. Go + GPL architecture.
Ticket: RHATDS-445.

## Execution

Step status lives in one GitHub issue per phase. The issues hold the checkbox lists; this
file holds the spec they reference with §. Work the phases in order:

| Phase | Issue | Gate |
| ----- | ----- | ---- |
| A. Scaffold | [#1](https://github.com/RedHat-UX/mcp/issues/1) | None |
| B. Spike (risky paths, fixture data) | [#2](https://github.com/RedHat-UX/mcp/issues/2) | None; starts upstream asimonim#63, cem#458, asimonim#64 |
| C. Data pipeline | [#3](https://github.com/RedHat-UX/mcp/issues/3) | Soft: asimonim#64 fixture |
| D. Resolution and component tools | [#4](https://github.com/RedHat-UX/mcp/issues/4) | cem#458 merged and in a cem tag |
| E. Tokens, docs, migration | [#5](https://github.com/RedHat-UX/mcp/issues/5) | None |
| F. Release v0.1.0 | [#6](https://github.com/RedHat-UX/mcp/issues/6) | grw license tag, cem and asimonim P0 tags, legal confirmation |

Rules:

- Steps within a phase may run in any order unless one names another as a prerequisite.
  Agents may split a step into sub-issues of its phase issue
- TDD: write the failing test first, then implement (§11). A box is checked only when
  the change is merged to `main` with its tests
- **Gate** steps wait for the named upstream change. Do not work around a gate with a
  fork or `replace` directive on `main`
- Upstream steps change other repositories. Get the maintainer's approval before
  pushing, commenting, or merging there
- If a step changes a decision, update the referenced section here in the same PR

Deferred, not part of v0.1.0: P1 and P2 upstream items (§3), fetch and analyze modes
(§4.2), open questions (§14).

## 1. Summary

Official Red Hat Design System MCP server, published as `@rhds/mcp` under `redhat-ux`.

Delivered as a Go binary that imports the cem and asimonim Go libraries, embeds all
versioned data, and runs locally over stdio. It gives coding agents authoritative,
version-aware answers about RHDS custom elements, tokens, design guidance, and
migration steps.

Default operation is offline and deterministic. It does not aggregate other MCP
servers and it does not require Node at runtime.

## 2. Decisions

| Area          | Decision                                                                             |
| ------------- | ------------------------------------------------------------------------------------ |
| Language      | Go                                                                                   |
| Repository    | `redhat-ux/mcp`                                                                      |
| Go module     | `github.com/redhat-ux/mcp`; command at `github.com/redhat-ux/mcp/cmd/rhds-mcp`       |
| npm package   | `@rhds/mcp`, wrapper in `npm/`                                                       |
| Go version    | `go 1.26.4` minimum (asimonim requirement); pin the build with a `toolchain` directive in `go.mod`, which `setup-go` and grw's `go-version-file` read. The pin is the latest 1.26-line patch (`go1.26.10`); a toolchain directive equal to the `go` directive is redundant and `go mod tidy` removes it |
| MCP SDK       | `github.com/modelcontextprotocol/go-sdk` v1.8.0                                      |
| Protocol      | Negotiated. Advertise `2026-07-28`, `2025-11-25`, `2025-06-18`. See section 8        |
| Transport     | stdio                                                                                |
| License       | `GPL-3.0-or-later`                                                                   |
| Version       | `0.0.1`, tag-driven releases                                                         |
| Data          | All stable `@rhds/elements` and `@rhds/tokens` versions; docs snapshots per major    |
| Upstream code | Import cem and asimonim as Go libraries, statically linked                           |
| cgo           | Required (tree-sitter). Cross-compiled for all six platforms by `go-release-workflows` |
| Runtime Node  | None. Node is only for the npm wrapper                                               |
| npm wrapper   | Node `>=22`, no runtime dependencies beyond Node built-ins                           |
| Network       | None by default                                                                      |

### Why GPL is acceptable

The server is a separate program. Projects that install `@rhds/mcp` in
`devDependencies` and run it as a tool do not become GPL. The GPL boundary is linking
or bundling, not dependency type.

Static linking of cem and asimonim means the `@rhds/mcp` binary is GPLv3. That is the
chosen license for this package. Any redistributor, including an editor extension or a
Docker image that bundles the binary, must publish under GPL-compatible terms as well.

No relicensing of cem or asimonim is required.

Who carries obligations:

- Users who run `@rhds/mcp` at dev time: none. Running a GPL program places no terms on
  its output or on the project that runs it. Tool responses are RHDS data (MIT), not
  GPL code.
- The npm wrapper only spawns the binary. It is not a derived work of the binary, but it
  ships under `GPL-3.0-or-later` too, for one consistent license.
- Red Hat, as distributor: ship license text, offer corresponding source, add no
  restrictions (section 9.3). A public repository covers this.

Legal: Red Hat already ships GPL software routinely. Ask legal to confirm, but this does
not block the spike.

License variant: cem headers say GPL version 3 or later. asimonim PR #63 relicensed the
project to `GPL-3.0-or-later`, so the combined binary is distributed under
`GPL-3.0-or-later`.

## 3. Upstream prerequisites

These are changes needed in the upstream tools for `@rhds/mcp` to reuse them cleanly.
P0 items block v0.1.0, the first public release (section 12).

The spike can start with current APIs:

- Markup validation: until #458 lands, call the public `bennypowers.dev/cem/lsp/document`
  package directly (`FindCustomElements`), as the probe binary did. Replace it with the
  #458 package in Phase D
- Tokens: write the gen-data normalizer against the published 3.2.0 JSON; adopt the #64
  fixture when it lands

Version pinning: during development, pseudo-versions of cem and asimonim are fine.
v0.1.0 requires tagged upstream releases that contain every P0 change. Current tags:
cem `v0.11.4`, asimonim `v0.3.3` (both 2026-10-22, neither has P0 changes yet).

### P0: blocks v0.1

| Tool     | Change                                                                 | Issue | Why                                                                                                                                                            |
| -------- | ---------------------------------------------------------------------- | ----- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| asimonim | Merge PR #63, relicense to `GPL-3.0-or-later`, add npm `license` field | #63   | Target license for `@rhds/mcp`; fixes #62. Green and mergeable as of 2026-10-26                                                                               |
| asimonim | Ship `LICENSE` in the npm wrapper package                              | #65   | Already done in PR #63 (commit `b060016` adds `npm/LICENSE`; `npm pack --dry-run` includes it). PR #63 closes #62 and #65 on merge |
| asimonim | Add a normalized DTCG fixture for RHDS tokens                          | #64   | RHDS published JSON is Style Dictionary output, not canonical DTCG; lock the normalization contract before parsing                                          |
| cem      | Export markup validation from a non-MCP package                        | #458  | `rhds_validate_markup` cannot import `cem/mcp/tools`; handlers are unexported and tied to `MCPContext`                                                         |

Suggested cem API. It must not go in the existing `bennypowers.dev/cem/validate`
package: that package validates manifests against the CEM JSON schema and builds with
`CGO_ENABLED=0` today. Adding tree-sitter would make it cgo-only. Use a subpackage,
for example `bennypowers.dev/cem/validate/markup`:

```go
package markup

type MarkupOptions struct {
    Format         string // "html", "twig", "njk", "hbs"; RHDS uses only html and twig
    ValidateSlots  bool
    ValidateAttrs  bool
    Strict         bool
}

type MarkupIssue struct {
    Rule       string
    Severity   string // error, warning, info
    Confidence string // high, low; low for prose-derived judgements such as expected slots
    Message    string
    Element    string
    Line       int
    Column     int
}

func ValidateMarkup(pkg *manifest.Package, markup string, opts MarkupOptions) ([]MarkupIssue, error)
```

Internals already exist in `cem/mcp/tools/validate_html.go`:
`validateHtmlWithTreeSitter`, `validateElementAttributes`,
`validateSlotContentGuidelines`, `validateAttributeCombinations`,
`validateContentAttributeRedundancy`. Move the reusable parts behind a public API.

Today `validateHtmlWithTreeSitter` reaches tree-sitter through `lsp/document`'s
`DocumentManager`, which links every grammar cem supports (blade, PHP, TypeScript, CSS,
and others). The exported package should depend only on the HTML grammar, plus the
jinja dialect grammar for Twig, to keep binary size down.

Related: cem draft PR #257 (stale since 2026-03) pulls RFC 2119 conditional
requirements out of attribute and slot descriptions ("if `variant` is `icon`, you MUST
set `accessible-label`"). If it lands, those rules could give the markup API
higher-confidence findings than slot prose alone. Not a dependency.

### P1: improves build and composition, not required for spike

| Tool          | Change                                                                        | Issue | Why                                                                                    |
| ------------- | ----------------------------------------------------------------------------- | ----- | -------------------------------------------------------------------------------------- |
| cem           | Decouple `cem/manifest` from `internal/tstype` and tree-sitter                | #459  | Makes the CEM reader build with `CGO_ENABLED=0`; cgo only needed for markup validation |
| cem           | Export MCP registration, for example `RegisterTools(s *mcp.Server, registry)` | #460  | Only needed if open question 3 picks in-process reuse for analyze mode. Section 4.2 currently says subprocesses, which would make this unnecessary |
| asimonim      | Export MCP registration, same shape                                           | #66   | Same condition as #460                                                                 |
| cem           | Keep `manifest`, `breaking`, and `validate` (plus `validate/markup`) public in the layout refactor | #408, #461 | #408 phase 2a lists `breaking` among "CLI feature modules" that could move under `internal/`. `@rhds/mcp` imports `breaking.Compare`. #461 already names `breaking` as library API; #408 should agree |
| cem           | Public version package (`cem/version`) instead of `internal/version`          | #461  | Report upstream version in diagnostics                                                 |
| asimonim      | Public version package                                                        | #67   | Same reason                                                                            |
| cem, asimonim | Document library APIs with `doc.go` examples                                  | #461, #67 | GPL headers dominate current `go doc` output                                       |
| cem, asimonim | State a semver policy for the 0.x libraries                                   | #461, #67 | Pin exact versions and upgrade deliberately; 0.x prerelease is acceptable          |

### P2: future

| Tool  | Change                                                                | Why                                                                                         |
| ----- | --------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| cem   | Split `cem/manifest` into a cgo-free module or build tag              | Smaller dependency surface for consumers that only read manifests                           |
| mappa | None for v1                                                           | Import map generation is out of scope; mappa already has a WASM-backed JS API if it returns |
| `@rhds/tokens` | Add `license` and `repository` to `package.json`             | Published metadata has neither. The tarball does ship an MIT `LICENSE`, so no blocker. RedHat-UX/red-hat-design-tokens#200 |

### Filed upstream issues

- `bennypowers/cem#457`: npm package missing `license`, missing published README
- `bennypowers/cem#458`: export markup validation API outside the MCP package
- `bennypowers/cem#459`: make `cem/manifest` build with `CGO_ENABLED=0`
- `bennypowers/cem#460`: export MCP tool registration for embedding
- `bennypowers/cem#461`: public version package, library docs, and semver policy
- `bennypowers/asimonim#62`: npm package missing `license` (fixed by PR #63)
- `bennypowers/asimonim#63` (PR): relicense to `GPL-3.0-or-later`
- `bennypowers/asimonim#64`: normalized DTCG fixture for RHDS tokens
- `bennypowers/asimonim#65`: ship `LICENSE` in the npm wrapper package (fixed by PR #63)
- `bennypowers/asimonim#66`: export MCP tool and resource registration for embedding
- `bennypowers/asimonim#67`: public version package, library docs, and semver policy

### Release workflow dependency

`bennypowers/go-release-workflows`, branch `fix/license-compliance`, adds the license
handling required for npm distribution:

- precedence: explicit input, then `npm-dir/package.json`, then root `package.json`, then
  the GitHub API
- rejects empty and `NOASSERTION`; no MIT default
- copies a real license file into every platform package and fails if none exists

Verified clean with the new shell tests, `actionlint`, and `shellcheck`.

It also handles the cgo build for all six platforms: Linux cross-compilers on
`ubuntu-latest`, native `macos-latest` runners for darwin, and a container-based
toolchain (`setup-windows-build`) for win32. `@rhds/mcp` reuses that instead of solving
cgo cross-compilation itself.

Two separate dependencies, needed at different times:

- `build-binaries.yml`: already usable. Tag `v1.4` equals `main` today and includes the
  cgo builds. Pin to `v1.4` from Phase A. Its internal action references still say
  `@main` even at the tag, so pinning is not fully hermetic until a later tag fixes that
- `npm-publish.yml` with license compliance: needed only for the first npm publish
  (Phase F). The `fix/license-compliance` branch is local only. Push, merge, and tag
  it (with internal references pinned to the tag) before Phase F

Makefile contract (checked by grw `validate-makefile`):

- One target per platform (`linux-x64`, `linux-arm64`, `darwin-x64`, `darwin-arm64`,
  `win32-x64`, `win32-arm64`) that writes `dist/bin/rhds-mcp-<platform>` (`.exe` on
  win32)
- grw has no pre-build hook, so each platform target depends on `data`. `make data` is
  a no-op when `internal/data/dist/index.json` already matches `sources.lock.json`
  (section 4.3). Pinned inputs keep the six runs identical
- win32 targets run `make data` on the host runner, then build inside the grw container,
  which mounts the source tree. The container needs no network
- grw `build-binaries.yml` v1.4 runs plain `make <platform>` and passes no `VERSION`.
  The Makefile defaults it to `0.0.1`, so Phase F must pass `VERSION=<tag>` or derive it
  from the release tag for release binaries to report the tag
- Optional upstream improvement: a grw input to download a prebuilt data artifact, so
  data is generated once per workflow instead of six times. Not filed

For `@rhds/mcp`: set `license` in `npm/package.json` and copy `LICENSE` into the
wrapper.

Releases of the cem and asimonim npm packages are tracked upstream (#457, #63, #65).
They do not block `@rhds/mcp`, which links the Go libraries.

## 4. Architecture

### 4.1 Process and libraries

One Go binary. One `mcp.Server`. No nested or proxied MCP servers.

Upstream packages imported by the server binary:

- `bennypowers.dev/cem/manifest`
- `bennypowers.dev/cem/breaking`
- `bennypowers.dev/cem/validate/markup` (after P0 export; name to be settled in #458)
- `bennypowers.dev/cem/mcp` and `bennypowers.dev/cem/workspace` for the composed cem
  tools and resources (section 8)
- `bennypowers.dev/asimonim/parser`
- `bennypowers.dev/asimonim/resolver`
- `bennypowers.dev/asimonim/token`
- `bennypowers.dev/asimonim/mcp` and `bennypowers.dev/asimonim/fs` for the composed
  asimonim tools and resources (section 8)

Imported only by `tools/gen-data` (not shipped):

- `bennypowers.dev/asimonim/schema`
- `bennypowers.dev/asimonim/validator`
- `bennypowers.dev/asimonim/convert`, only if gen-data needs a format asimonim already
  emits. `rhds_query_tokens` `format: css` is a `--rh-*` name and a value, so it needs
  no converter.

The upstream MCP layers are mounted on the RHDS server rather than served by separate
processes: `cem/mcp.Register` and `asimonim/mcp.Register` add their tools, resources,
and resource templates to the same `mcp.Server`. The RHDS tool set remains the primary
surface. cem mounts whenever the process working directory is a constructible
workspace, because cem discovers manifests from the workspace itself, its workspace
packages, and `node_modules`. A consumer project that depends on a design system
therefore mounts cem even without a root `custom-elements.json`. Only a workspace that
cannot be constructed, or a cem registration failure, skips cem without failing
startup. asimonim mounts with a nil config so each session resolves its own config from
MCP roots. Per-mount disable lists remove exact tool names, resource URIs, or URI
templates after registration.

asimonim resolves per-session config from MCP roots: directly through
`ServerSession.ListRoots` on `2025-06-18` and `2025-11-25`, and through
multi-round-trip input requests on `2026-07-28` and later. Each session caches its own
roots and config, so clients with different workspaces can share one server. Every
completed tool and resource result reports workspace provenance in `_meta` under
`asimonim/workspace` (`root`, `source`, and the `warning` when roots cannot be read and
the result falls back to `cwd`). A host that already knows the workspace can pass
`WithWorkspace` to asimonim's `Register`, which takes precedence over session roots.
asimonim's conflict probe stays pinned to `2025-11-25`: it only lists features, so it
does not need the `2026-07-28` stateless discovery path.

Where tree-sitter is used (checked against cem `13100d50`):

- `cem/manifest` imports `internal/tstype`, which uses the TypeScript grammar.
  `Attribute.IsValidValue` uses it to split union types. So even reading a manifest
  needs cgo today. P1 #459 removes this.
- Markup validation parses the `markup` string the agent sends as tool input. It does
  not read workspace HTML files. The only workspace reads are version resolution
  (section 6), which uses `golang.org/x/net/html` and plain JSON/YAML parsers, not
  tree-sitter.
- asimonim `parser`, `resolver`, `token`, `schema`, `validator`, and `convert` all
  build with `CGO_ENABLED=0` (verified).

cgo is a build-time cost, not a runtime one. `go-release-workflows` already builds cem
with cgo for all six platforms (section 3).

### 4.2 Modes

**Bundled mode (default).** All manifests, tokens, docs, and release notes are embedded
in the binary with `//go:embed`. Go plus cgo for tree-sitter. No network, no
subprocesses.

**Fetch mode (deferred).** Optional explicit flag to read data from a local directory or
a configured source. Off by default. Not in v0.1.

**Analyze mode (deferred).** Optional explicit flag to inspect the current workspace
with cem or asimonim. Invoke user-installed binaries as separate processes. Never scan
`PATH` and execute the first hit. Report the resolved binary and version. Not in v0.1.

### 4.3 Data flow

```
npm registry + GitHub release tags (+ docs/ai-guidelines submodule) + GitHub releases
        |
        v
tools/gen-data (Go, imports cem/asimonim, not shipped)
        |
        v
internal/data/dist/<package>/<version>/*.json.gz   // normalized DTCG for tokens
internal/data/dist/docs/<tag>/*.md.gz
internal/data/dist/index.json   // version, path, sha256, bytes, schemaVersion
        |
        v
//go:embed -> runtime lazy load -> tools
```

Startup reads only the index. A version's artifacts are decompressed on first use and
cached in memory.

Generated data is not committed. It is a codegen step:

- `make data` (wrapping `go generate ./internal/data`) runs gen-data into
  `internal/data/dist/`, which is gitignored.
- The embed directive is `//go:embed all:dist`. A committed `dist/.gitkeep` keeps the
  pattern valid on a clean checkout, so `go vet`, `golangci-lint`, and unit tests work
  without generated data.
- A binary built without data reports that at startup and returns a structured error
  from every data tool. It never fails silently.
- Unit tests use small fixtures in `testdata/`. Real-artifact tests and release builds
  run `make data` first.
- gen-data pins its inputs in a committed `internal/data/sources.lock.json`: exact npm
  versions with integrity hashes, git tags with commit SHAs, and the `ai-guidelines`
  submodule commit per tag. `make data-update` refreshes the lock (new RHDS release);
  `make data` only reads it. Same lock gives byte-identical output (NFR-05).
- `index.json` records the sha256 of the canonical lock. `make data` compares it with
  the committed lock and is a scoped no-op when every artifact named by the index still
  exists with its recorded size. A missing or stale index regenerates; a no-op makes no
  network requests.
- Generation needs network (npm registry, GitHub). Building from an existing `dist/`
  does not.
- Each GitHub release attaches the generated `dist/` archive with a checksum, so
  anyone can reproduce the binary without hitting npm.

As a result, `go install` is not supported: the module proxy has no generated data.
Build from source with `make data build`.

### 4.4 Size and startup

Measured 2026-10-26 on linux-x64 with a probe binary (cem `manifest`, `breaking`,
`lsp/document`, asimonim `parser`/`resolver`/`validator`, go-sdk v1.8.0, built with
`-trimpath -ldflags="-s -w"`):

- Probe binary: 22.0 MB stripped, 5.8 MB gzipped. `lsp/document` stands in for the
  future `cem/validate/markup` and pulls in every grammar, so a narrower export
  should come in smaller
- Process start to `main`: 7 to 11 ms direct, about 33 ms spawned through Node

Phase B remeasurement, 2026-10-27, linux-x64 (Go 1.27.3, gcc, cem
`d497f94c5b42` / pseudo-version `v0.11.5-0.20261026202837-d497f94c5b42`,
asimonim `98c22de1` / pseudo-version `v0.3.4-0.20261026144458-98c22de15b8a`,
built with `-trimpath -ldflags="-s -w"`, with a 12.5 KB `index.json` present):

- NFR-01, spawn to `initialize` response: direct binary 7.4 ms median,
  8.9 ms p95, 11.7 ms max (n=50); through a Node 22 wrapper process 25.5 ms
  median, 30.6 ms p95, 30.6 ms max (n=30). Budget is 150 ms
- NFR-06, stripped binary: 22.8 MB (21.8 MiB), 6.1 MB gzipped. Budget is 40 MB.
  Platform npm tarballs do not exist yet, so the 16 MB tarball gate is not
  measurable in Phase B
- Startup read only `index.json`. The binary links cem `manifest`,
  `lsp/document` (all grammars), and asimonim `parser`, `resolver`, and `token`.
  `lsp/document` still pulls in every grammar cem supports; cem#458's narrower
  export should reduce size in Phase D

Embedded data:

- CEM per version: 0.75 to 1.2 MB raw, 66 to 140 KB gzipped
- All 44 stable element manifests: roughly 5 MB gzipped
- Docs markdown: 218 KB gzipped at `v1.4.5`, 430 KB at `v5.0.0`; about 1.7 MB for
  five snapshots
- Pattern HTML examples: about 17 KB gzipped per snapshot
- Release notes (`CHANGELOG.md`): 39 KB gzipped
- Tokens: under 1 MB for 18 versions

Expected release binary: 28 to 32 MB per platform.

## 5. Data and version support

### 5.1 Coverage

- `@rhds/elements`: 44 stable versions (78 published including prereleases), majors 1
  through 5, latest 5.0.0
- `@rhds/tokens`: 18 stable versions (58 published including prereleases), majors 1
  through 3, latest 3.2.0
- Docs: snapshots at release tags from `redhat-ux/red-hat-design-system` (78 tags)
- Release notes: all `@rhds/elements` releases

Prereleases are excluded.

Docs snapshots: latest release tag per elements major: `v1.4.5`, `v2.1.2`, `v3.2.0`,
`v4.2.2`, `v5.0.0`. Docs-only topics use the latest snapshot. This trades complete
historical prose for reasonable binary size. Revisit if users need minor-level docs
history. `make data-update` derives the tag set from the latest stable elements version
per major in the lock, so a new major must have a matching docs tag or the update fails.

### 5.2 CEM schema normalization

- Elements v1 through v3: CEM `schemaVersion` 1.0.0
- Elements v4 and v5: CEM `schemaVersion` 2.1.0

Normalize both into one internal model. Do not assume v2.1 fields exist. Do not assume
`demos` or `customElement` fields exist in every version.

`deprecated?: boolean | string` exists in the CEM schema. Current RHDS manifests contain
no deprecated entries. Show deprecation only when the manifest supplies it. Do not invent
stable/beta states.

### 5.3 Elements to tokens coupling

The coupling does not follow majors. It comes from each elements release's own
`dependencies["@rhds/tokens"]` range. Verified at release tags:

| Elements      | `@rhds/tokens` range |
| ------------- | -------------------- |
| 1.0.0         | `^1.0.0`             |
| 1.1.0 - 1.2.x | `^1.1.2`             |
| 1.3.0 - 1.4.x | `^2.0.1`             |
| 2.x           | `^2.0.1` - `^2.1.1`  |
| 3.x           | `^3.0.0` - `^3.0.1`  |
| 4.x           | `^3.0.1` - `^3.1.0`  |
| 5.0.0         | `^3.2.0`             |

gen-data reads the range from each bundled elements `package.json` and records the
highest bundled tokens version that satisfies it in `index.json`. Do not hand-maintain
this table; a test checks the generated mapping against the tag data.

Resolve tokens from the resolved elements version. An explicit `tokensVersion` may
override with a compatibility warning.

### 5.4 Token data

The published `json/rhds.tokens.json` is Style Dictionary output, not canonical DTCG.
For `@rhds/tokens@3.2.0`:

- 547 tokens parsed
- 18 tokens have no `$type` (border radius, letter spacing, line height, opacity)
- Style Dictionary fields are present (`name`, `attributes`, `filePath`, `isSource`,
  `original`)
- 160 published tokens do not exist in the source YAML: `-rgb`, `-hsl` color variants
  and `light-dark(...)` composite theme tokens

Do not feed the published JSON to asimonim as if it were DTCG, and do not translate per
lookup. Normalize the whole document to DTCG in memory at data generation, then parse.

Normalization contract:

1. Walk the Style Dictionary JSON as a tree.
2. For each token:
   - Prefer `original.$value` over the resolved `$value` so aliases survive
   - Recover `$type` from `attributes.type` with an explicit mapping
     (`radius -> dimension`, `opacity -> number`, `line-height -> number`,
     `letter-spacing -> dimension`, and so on)
   - Keep `$description`, `$deprecated`, and `$deprecationMessage`
   - Keep group and token `$extensions`, including `com.redhat.ux`
   - Move Style Dictionary fields to `$extensions["com.style-dictionary"]` or drop them
3. Split generated tokens:
   - `-rgb` and `-hsl` are valid color tokens and stay in DTCG
   - `light-dark(var(...))` composite tokens are CSS expressions, not DTCG values;
     keep them in a separate computed/theme map
4. Validate the normalized document with `asimonim/validator`.
5. Parse with asimonim, resolve aliases, and build `token.Map` with `Prefix: "rh"`.

The normalization code lives in `tools/gen-data`, not in the parser. Upstream issue
#64 locks the contract with a fixture.

Implementation notes from Phase C:

- #64 was still open when this landed. Our fixtures are committed per tokens major
  under `tools/gen-data/testdata/tokens/{1.x,2.x,3.x}/` (input and normalized golden).
  Adopt the upstream fixture once #64 merges and keep our goldens as regression tests.
- Style Dictionary's draft group-marker convention (`foo._` with sibling tokens under
  `foo`) is flattened: the marker becomes the token at `foo` and siblings become
  `foo-bar`. This keeps published CSS names such as `--rh-color-icon-subtle` and
  `--rh-color-icon-subtle-hover` intact, because asimonim derives names from JSON keys
  and would otherwise emit a trailing `-_`.
- Keys that Style Dictionary left in camelCase (`2xsMax`) are rewritten from the token's
  published `name` to kebab case (`2xs-max`), so CSS names match the generated CSS.
- `mediaQuery` is an RHDS vendor type, not a DTCG type. The normalizer keeps
  `$type: "mediaQuery"` and the object value verbatim, records
  `$extensions["com.rhds.mcp"].vendorType = "mediaQuery"`, and does not resolve nested
  `{breakpoint.*}` references. The 3.2.0 release has six such tokens.
- The generated `css/default-theme.css` is authoritative for the theme map. At least one
  release's token JSON disagrees with its own CSS for `rh-surface`, so a disagreement is
  not fatal. Theme-map entries that have no DTCG token are added back at load time as
  lookup tokens, so `rhds_query_tokens` can find `color-accent-base` and report its
  light/dark values.
- The `rh-surface` palette marker (six arms) is prose, not a light/dark token, so
  `color-surface` is absent from normalized DTCG in tokens 2.1.0 through 2.2.4. Its
  `color-surface-*` children remain. This is intentional per the palette rule above.
- `tools/gen-data` records a generator version in `index.json`. `make data` regenerates
  when it changes, so generator updates never leave a stale dist tree. Use
  `make data-force` to regenerate unconditionally.

All embedded token data is normalized DTCG, produced at `@rhds/mcp` development time.
The runtime never sees Style Dictionary output. It parses normalized DTCG with asimonim
on first use of a version and caches the result.

Older majors (tokens 1.x and 2.x) may have a different Style Dictionary shape. #64
covers 3.2.0 only. Reconcile older majors during `@rhds/mcp` development (agent-assisted
is fine) and commit one normalization fixture per tokens major under `testdata/`.

Long term, `red-hat-design-tokens` can migrate to asimonim per its own
`docs/rhdt-migration-prompt.md`, which removes normalization for new versions. Historical
versions still need it.

Theme behavior:

- Token JSON is theme-agnostic
- Light and dark values live in generated `css/default-theme.css` via `light-dark(...)`
  and `-on-light` / `-on-dark` token names
- Saturated palettes are `rh-surface` prose, not a token theme

At data generation time, extract a theme map from the generated CSS into JSON. Runtime
looks up the map. Do not parse CSS per request.

### 5.5 Docs and release notes

Bundle prose markdown from these paths, verified at the five snapshot tags:

| Path                                       | Present at                    |
| ------------------------------------------ | ----------------------------- |
| `elements/<element>/docs/*.md`             | all (150 at v1.4.5, 199 at v5.0.0) |
| `docs/foundations/`, `docs/patterns/`, `docs/accessibility/`, `docs/get-started/`, `docs/tokens/` | all |
| `docs/theming/`, `docs/support/`           | v2.1.2 and later              |
| `docs/personalization/`                    | v3.2.0 and later              |
| `docs/ai-guidelines/`                      | v4.2.2 and later; git submodule |

`docs/ai-guidelines` is a submodule of `project-felt/ai-guidelines` (public, MIT).
gen-data must check out the submodule commit pinned by each tag. A plain tag checkout
leaves it empty.

Also bundle example markup:

- `docs/patterns/**/patterns/*.html` and `docs/theming/patterns/*.html` (v5.0.0 has
  about 65 files) feed `rhds_get_usage_examples`

Processing:

- Strip 11ty frontmatter
- Preserve fenced code, tables, and indentation
- Drop or rewrite image references; selected SVGs optional
- Keep headings for chunking
- Record source tag and path

Indented prose that begins with an HTML tag is treated as prose rather than an
indented code block so template image tags can be stripped. The trade-off is
that a genuinely indented code sample whose first character is `<` is preserved
verbatim as prose. Fences indented four or more spaces stay code, and a closing
fence must use the same character and a run at least as long as the opener.

Bundle release notes for migration. They contain Before/After examples. gen-data writes
the combined changelog plus one release-note artifact per bundled elements version. Also
compute breaking changes from `cem/breaking.Compare` between embedded manifests.

Token renames and removals across tokens majors are not in any manifest. Curate them
into `tokens-migrations.json` during `@rhds/mcp` development (agent-assisted from the
tokens changelog and a name diff between normalized versions), and review them like
code. The committed document records its changelog source, the normalized-name diff,
and curation notes for entries the changelog got wrong.

## 6. Version resolution

Order, first hit wins:

1. Explicit `version` argument
2. Workspace lockfile: `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock` (classic and
   berry), `bun.lock`
3. Installed package: `node_modules/@rhds/elements/package.json`, including hoisted and
   workspace locations
4. Project `package.json` dependencies, devDependencies, overrides, resolutions,
   workspaces, and pnpm catalog entries
5. Bounded scan of HTML and Twig templates for import maps or CDN references, parsed
   with `golang.org/x/net/html`, skipping `node_modules`, with file and byte caps
6. Default to latest bundled stable version with a diagnostic warning

Rules:

- Lockfiles outrank installed packages because nested installs can disagree with project
  intent
- Collect all candidates and report them
- Every response carries `resolved: {elements, tokens, docs, source, warnings}`
- Do not block a tool call on a prompt by default
- Ambiguous means candidates at the same precedence level disagree on the elements
  major. For example, two workspace packages lock different majors, or `package.json`
  and an import map disagree when no lockfile exists. A minor or patch disagreement is
  not ambiguous: pick the highest and warn
- When ambiguous and the client supports elicitation, ask (see section 8 for how,
  per protocol version). Otherwise return a structured error naming the candidates,
  the `version` argument, and config options
- `rhds_resolve_version` exposes resolution without a data query

Workspace root comes from MCP roots when the client supports them (section 8), else
`cwd`.

## 7. Tools

All tools use the `rhds_` prefix, are read-only, and return resolved-version metadata.

### `rhds_resolve_version`

Input: `version?`, `tokensVersion?`.
Output: the `resolved` block (elements, tokens, docs, source, warnings) plus every
candidate found, with its source file and precedence level. Reads no bundled data
beyond the index.

### `rhds_list_components`

Input: `version?`, `includeDeprecated?`, `cursor?`, `pageSize?`.
Output: tag name, summary, module path, package subpath, deprecated state, docs
availability, total count, `nextCursor`.

Paginated: `@rhds/elements@5.0.0` declares 82 custom elements, which overflows the 8k
default budget (NFR-03) with summaries. Default `pageSize` is 25, max 100. The cursor is
opaque (base64 of the resolved version, offset, and deprecated filter). A cursor from a
different resolved version or with a different deprecated filter is rejected with a
structured error.

An entry with `truncated: true` was shortened by a fixed per-field cap, not by the
response budget. A warning names the field, the cap, and the longest original value.
Paging never hides an entry without that signal.

### `rhds_get_component_api`

Input: `tagName`, `version?`, `sections?` (`attributes`, `properties`, `events`, `slots`,
`cssParts`, `cssProperties`, `examples`), `detail` (`summary` or `full`).
Output: normalized API. Summary by default. Section filters replace silent truncation:
a section that does not fit is omitted from `structuredContent`, named in a warning, and
returned as an MCP `resource_link` item pointing at
`rhds://elements/{version}/components/{tag}/{section}`. If the response still exceeds the
budget after optional sections are removed it returns `response_too_large` with the
section resource URIs in its hint.

### `rhds_search`

Input: `query`, `scope?` (`components`, `tokens`, `docs`, `examples`, `all`), `version?`,
`limit?`.
Output: ranked hits with type, title, path, version, and short excerpt. Primary discovery
tool.

### `rhds_validate_markup`

Input: `markup`, `format?` (`html`, `twig`), `version?`.
Output: warnings with element, line, rule, message, and confidence (from
`MarkupIssue.Confidence`, section 3).

Rules:

- Unknown RHDS tag
- Invalid or unknown attribute
- Invalid enum value using `Attribute.IsValidValue`
- Deprecated API when marked
- Slot name not present on the element
- Default-slot use when prose indicates a named slot is expected
- Attribute and content redundancy when detectable

Limits:

- CEM does not encode required slots, content models, or cardinality. Required-slot
  judgement comes from slot prose and is returned as low confidence
- Twig control flow can unbalance HTML. Degrade to warnings when structure is unclear
- Assume the manifest is valid
- Never claim full schema validation

Phase B note: slot-name and default-slot rules are deferred until cem#458 lands. The
spike implements unknown tag, unknown attribute, enum value, and deprecation checks
through `lsp/document`. HTML tag and attribute names are matched case-insensitively.

### `rhds_query_tokens`

Input: `query?`, `token?`, `version?`, `theme?` (`light`, `dark`, `auto`), `format?`
(`value`, `css`, `chain`), `detail?` (`summary` default, `full`), `pageSize?`,
`cursor?`.
Output: token name, CSS custom property, type, value, alias chain, theme value, and
source (`json` or `css/default-theme.css`). `chain` puts the alias path in `value`.
Exact lookup accepts dot paths and `--rh-*` names. When `version` names an
`@rhds/elements` release instead of a tokens release, the coupled tokens version is
used (section 5.3). `summary` uses the 8k budget and `full` may use the 32k cap; when
the budget is reached the response carries `nextCursor`, never a silent truncation.

An entry with `truncated: true` was shortened by a fixed per-field cap, not by the
response budget. This covers individual fields and the alias chain. A warning names
the field, the cap, and the longest original value.

### `rhds_get_design_guidelines`

Input: `topic?`, `element?`, `version?`, `limit?`.
Output: heading chunks with source path and release tag. Respect response budget.

### `rhds_get_migration_guide`

Input: `fromVersion`, `toVersion?` (default: latest bundled stable), `element?`,
`includeTokens?` (default true), `detail?` (`summary` default, `full`),
`sections?` (`changes`, `releaseNotes`, `tokens`; default all), `limit?` (entries per
category).
Output: breaking changes from `cem/breaking.Compare`, plus release-note Before/After
examples, plus token renames and removals from `tokens-migrations.json` when the
coupled tokens major changes (section 5.5). `summary` uses the 8k budget and `full`
may use the 32k cap. The budget is allocated across the selected categories so one
large category does not evict the others by default; `sections`, `limit`, `element`,
and a narrower version range are the follow-up controls.

### `rhds_get_usage_examples`

Input: `tagName`, `version?`, `kind?` (`demo`, `docs`, `pattern`, `twig`),
`detail?` (`summary` default, `full`). Output: CEM `demos`, docs snippets, pattern
HTML from the docs snapshot, generic Twig slot markup. Do not claim a Drupal library
key. `summary` uses the 8k budget and `full` may use the 32k cap, so a single pattern
larger than the summary budget stays reachable.

### Out of scope for v1

- `rhds_get_drupal_asset`: no authoritative public mapping; data likely internal
- `rhds_generate_import_map`: changes per major; mappa can regenerate if needed

## 8. MCP surface

- Tools: typed structs, `AddTool` for inferred 2020-12 schemas, `outputSchema`,
  `readOnlyHint` annotations, human `title`
- Resources: progressive disclosure. Two static resources: `rhds://packages/`
  (bundled versions, kinds, counts, and the resolved workspace version) and
  `rhds://migrations/tokens`. Artifact data is read through four template
  families, each with a no-query form for workspace resolution and a query form
  for a pinned version: `rhds://data/elements{/path*}`, `rhds://data/tokens{/path*}`,
  `rhds://data/docs{/path*}`, and `rhds://data/release-notes{/path*}` (plus the
  same templates followed by `?{+query}`). Per-component API sections at
  `rhds://elements/{version}/components/{tag}/{section}` for `attributes`,
  `properties`, `events`, `slots`, `cssParts`, `cssProperties`, and `examples`.
  Template reads resolve the workspace per session through MCP roots
  (multi-round-trip requests on `2026-07-28` and later, direct roots requests
  earlier, and `cwd` without the roots capability). A workspace resolution is
  cached only when a lockfile supplies the winning candidate; the key covers
  every existing lockfile plus the root `package.json` path, mtime, and size,
  and a workspace with no lockfile is rescanned on every read.
- Prompts: Lit usage, HTML usage, Twig slot template
- Server `instructions`: supported versions, offline behavior, resolution precedence,
  search versus exact lookup
- Logging: `slog` to stderr only. stdout is JSON-RPC exclusively

### 8.1 Protocol versions and feature negotiation

Target the latest version that is still widely supported, and negotiate features with
the client, never assume them.

Advertise through `ServerOptions.SupportedProtocolVersions`, newest first:

| Version      | Why                                                                                  |
| ------------ | ------------------------------------------------------------------------------------ |
| `2026-07-28` | Current. Multi round-trip requests (MRTR): server cannot initiate requests          |
| `2025-11-25` | Widely deployed baseline                                                             |
| `2025-06-18` | Floor. First version with `outputSchema`/structured content and elicitation         |

`2025-03-26` and `2024-11-05` are not advertised. The tool contract relies on structured
output, and supporting text-only fallbacks for those versions is not worth the extra
test matrix. Revisit if a target client still needs one.

Server-to-client input (elicitation for ambiguous versions, roots for the workspace):

| Negotiated version          | Mechanism                                                                       |
| --------------------------- | ------------------------------------------------------------------------------- |
| `2026-07-28`                | Return an input-required result with `inputRequests` (`ElicitParams`, `ListRootsParams`); the client retries with `inputResponses`. `ServerSession.Elicit`/`ListRoots` return errors on this version |
| `2025-06-18`, `2025-11-25`  | `ServerSession.Elicit` / `ServerSession.ListRoots`, only when the client declared `elicitation` / `roots` |

- Check the client's capabilities from `InitializeParams`. Without the capability, fall
  back to `cwd` for roots and a structured error for ambiguity
- Ask for roots only when a call reaches a workspace-dependent step (explicit `version`
  skips it)
- Keep no server-side state that a `2026-07-28` retry depends on. Carry anything needed
  in `requestState`
- Test both mechanisms with SDK in-memory transports at each advertised version

## 9. Packaging and GPL compliance

### 9.1 npm layout

- `@rhds/mcp`: Node wrapper, `bin` entry, `optionalDependencies` for platform packages
- `@rhds/mcp-linux-x64`, `-linux-arm64`, `-darwin-x64`, `-darwin-arm64`,
  `-win32-x64`, `-win32-arm64`: Go binary plus license text and repository link
- Platform packages use `os` and `cpu` fields so npm installs only the matching one
- No postinstall download. Works with `--ignore-scripts` and corporate registries

Wrapper behavior:

- Resolve the platform package, spawn the binary with inherited stdio
- Never write to stdout
- Exit with the child status
- Clear error to stderr when the platform is unsupported

### 9.2 Other distribution

- GitHub release archives with checksums, plus the generated data archive
- Build from source: `make data build`. Needs a C toolchain for cgo. `go install` is not
  supported because generated data is not committed (section 4.3)
- Signed git tags

### 9.3 GPL obligations

| Item                  | Action                                                                                                  |
| --------------------- | ------------------------------------------------------------------------------------------------------- |
| License text          | `GPL-3.0-or-later` in root `LICENSE`, the npm wrapper tarball, and every platform package                |
| Source headers        | SPDX identifiers in Go, JavaScript, and build files                                                     |
| Corresponding source  | Public repository (including `tools/gen-data`) plus `repository` in every package, pointing at the exact release tag; generated data archive attached to each release |
| NOTICE                | Copyright notices for cem, asimonim, MCP SDK, tree-sitter, other Go deps, and the embedded data below    |
| npm metadata          | Set `license` in the npm wrapper and platform packages; do not rely on GitHub API detection alone        |
| No added restrictions | No EULA or marketplace terms that contradict GPL                                                        |
| Reproducibility       | Pin Go toolchain, dependency versions, and cgo toolchain                                                |
| Release workflow      | Use `go-release-workflows` `fix/license-compliance`, merged and tagged; do not float on `@main`          |

Run a dependency license check in CI with an allowlist for GPL-3.0, MIT, Apache-2.0,
BSD, and MPL-2.0. Anything else fails the build.

MPL-2.0 is allowed for `github.com/gosimple/slug`, which `bennypowers.dev/cem/mcp`
pulls in through `cem/generate/demodiscovery`. MPL-2.0 is compatible with
`GPL-3.0-or-later`, so the combined binary keeps one license. Its copyright line and
license are recorded in `NOTICE`.

Embedded data licenses (checked 2026-10-26). All are MIT, which can be combined with
GPL. Reproduce each copyright line and the MIT text in NOTICE:

| Source                                    | License | Copyright line               | Where                              |
| ----------------------------------------- | ------- | ---------------------------- | ---------------------------------- |
| `@rhds/elements` manifests, docs prose, pattern HTML, release notes (`redhat-ux/red-hat-design-system`) | MIT | Copyright 2022 Red Hat, Inc. | `LICENCE.md` (one file covers the whole repo, docs included); npm `license: MIT` |
| `@rhds/tokens` (`redhat-ux/red-hat-design-tokens`) | MIT | Copyright (c) 2022 Red Hat UX | `LICENSE` in repo and npm tarball; npm metadata has no `license` field (P2 above) |
| `docs/ai-guidelines` (`project-felt/ai-guidelines`) | MIT | Copyright (c) 2026 project-felt | submodule `LICENSE` |

Images are dropped from bundled docs (section 5.5), so no logo or brand assets ship.
Red Hat names are used descriptively only.

## 10. Non-functional requirements

| ID     | Requirement                                                                                |
| ------ | ------------------------------------------------------------------------------------------ |
| NFR-01 | Cold start under 150 ms, measured from npm wrapper spawn to `initialize` response (Node startup included). Probe: 7 to 11 ms direct, about 33 ms through Node |
| NFR-02 | Bundled tool latency under 50 ms after first decompress                                    |
| NFR-03 | Response budget 8k characters default, 32k hard cap, explicit detail controls              |
| NFR-04 | Offline by default, no outbound requests                                                   |
| NFR-05 | Deterministic output for a given version and input                                         |
| NFR-06 | Size gate per platform: stripped binary 40 MB or less; platform npm tarball 16 MB or less. Expected about 30 MB binary (section 4.4) |
| NFR-07 | Workspace reads bounded by count, size, and path; no symlink escape                        |
| NFR-08 | Treat workspace content as untrusted data, never as instructions; label as source material |
| NFR-09 | No telemetry                                                                               |
| NFR-10 | Memory under 100 MB for normal queries                                                     |

## 11. Testing and CI

TDD. Red tests before implementation.

- Unit and golden tests for CEM 1.0.0 and 2.1.0 fixtures
- Real artifact tests against pinned `@rhds/elements` and `@rhds/tokens` tarballs
- Style Dictionary to DTCG normalization, one fixture per tokens major: every token
  typed, aliases preserved, SD metadata moved or dropped, generated tokens split,
  validator accepts the result
- Elements to tokens mapping generated from each release's `package.json` matches the
  verified ranges (section 5.3)
- gen-data determinism: two runs with pinned inputs give byte-identical `dist/`
- Clean checkout with no generated data: `go vet`, lint, and unit tests pass; the binary
  reports missing data
- Protocol matrix: initialize and call tools at `2025-06-18`, `2025-11-25`, and
  `2026-07-28`; elicitation and roots via session methods and via MRTR; clients with
  and without those capabilities
- Pagination: cursor round-trip, cursor from another version rejected
- Token parse, alias chain, theme map, and prefixed CSS variable names
- Version cascade matrix: lockfiles, hoisted, nested, workspaces, overrides, ambiguous,
  none
- HTML and Twig validation fixtures, including unbalanced control flow
- Docs chunking, frontmatter stripping, code preservation
- Fuzz tests for markup, lockfile parsing, and version strings
- Subprocess test asserting stdout is JSON-RPC only
- npm wrapper smoke test on linux, darwin, and windows
- `go test ./... -count=1`, `go vet ./...`, `staticcheck ./...`,
  `golangci-lint run ./...`
- Release builds always use cgo (markup validation needs tree-sitter). After P1 #459,
  add a `CGO_ENABLED=0` build of the non-validation packages to keep that boundary
  honest
- PR CI builds all six platforms through `go-release-workflows`, so a cgo regression
  shows up before release
- actionlint on workflows; verify every embedded action URL
- Dependency license check
- Size gate on binaries and npm tarballs

## 12. Versions and gates

`0.0.x` tags are development builds: GitHub release artifacts only, no npm publish.
`v0.1.0` is the first public release, when Phase F (#6) closes. "P0 blocks
v0.1" means v0.1.0.

Upstream work runs in parallel with `@rhds/mcp` work. The phase issues mark each step that
must wait for an upstream change with **Gate**:

| Upstream change                                   | First needed in      |
| ------------------------------------------------- | -------------------- |
| `bennypowers/asimonim#64` fixture                 | Phase C (soft gate)  |
| `bennypowers/cem#458` merged and in a cem tag     | Phase D              |
| `bennypowers/asimonim#63` merged and in a tag     | Phase F              |
| grw `fix/license-compliance` merged and tagged    | Phase F              |

Internal transition work for the community package is tracked privately and is not part
of this repository.

## 13. Non-goals

- Aggregating or proxying cem and asimonim MCP servers
- TypeScript/Node runtime for the server
- Runtime dependency on GPL CLI binaries
- Reimplementing CEM or DTCG parsing
- On-the-fly Style Dictionary type inference during token lookup; normalize the whole
  document first
- Drupal library mapping without an authoritative source
- Import map generation in v1
- Public documentation of the community package deprecation

## 14. Open questions

1. Docs snapshot granularity: per major is chosen; is per minor needed?
2. Analyze mode: does any user need live workspace analysis in v1?
3. Reuse upstream MCP tool handlers in analyze mode, or keep the tool sets separate?
4. Upstream P0 timeline: who lands the cem validation export, the asimonim fixture, and
   the wrapper license fix?
5. Red Hat legal confirmation of GPL-3.0-or-later distribution (not blocking, see
   section 2)
6. Generated token policy: should `light-dark(...)` composite tokens be queryable from
   the computed map, or only exposed through `rhds_query_tokens` theme lookups?
7. When to tag `go-release-workflows` `fix/license-compliance` and bump asimonim, cem,
   and `@rhds/mcp` to that tag?
8. npm scope layout: separate platform packages as proposed, or a single package with
   optional binary downloads?
9. Should RHDS start populating `deprecated` in manifests so lifecycle status has data?
10. Does any target client still speak only `2025-03-26` or older (section 8.1)?
