# RHDS MCP

`@rhds/mcp` is the Red Hat Design System MCP server. It gives coding agents offline,
version-aware answers about RHDS elements, design tokens, design guidelines, and
migrations.

Status: pre-release, under development. See [`docs/PLAN.md`](docs/PLAN.md) for the spec
and the phase issues for progress.

## Upstream servers

`@rhds/mcp` bundles cem and asimonim and mounts their tools and resources on its own
server. Installing `@rhds/mcp` is enough for RHDS work; you do not need their MCP
servers as well.

Each release pins exact cem and asimonim versions. Those pins are listed in the
release notes. Updating `@rhds/mcp` is how you pick up upstream fixes, and clients
that prompt for package updates will surface new releases.

### If you also run cem or asimonim

MCP servers cannot see each other's registries, so neither side can detect or drop
duplicates. With both enabled, the same capability appears under two client
prefixes, for example two `validate_html` entries backed by different bundled
versions. That costs tool-listing context and can produce different answers.

License: GPL-3.0-or-later.
