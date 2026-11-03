# RHDS MCP

`@rhds/mcp` is the Red Hat Design System MCP server. It gives coding agents offline,
version-aware answers about RHDS elements, design tokens, design guidelines, and
migrations.

Status: pre-release. See [`docs/PLAN.md`](docs/PLAN.md) for the spec and the phase
issues for progress. The current release is a prerelease: the MCP surface may still
change.

## Install

Download a binary from the [releases page](https://github.com/RedHat-UX/mcp/releases).
Each release includes a `SHA256SUMS` file.

| OS | Architecture | Asset |
|---|---|---|
| Linux | x86-64 | `rhds-mcp-linux-x64` |
| Linux | arm64 | `rhds-mcp-linux-arm64` |
| macOS | Intel | `rhds-mcp-darwin-x64` |
| macOS | Apple silicon | `rhds-mcp-darwin-arm64` |
| Windows | x86-64 | `rhds-mcp-win32-x64.exe` |
| Windows | arm64 | `rhds-mcp-win32-arm64.exe` |

Linux binaries require glibc 2.34 or newer: RHEL 9+, Ubuntu 22.04+, Debian 12+, and
current Fedora. On RHEL 8 or another older distribution, run the Linux binary inside a
UBI 9 container.

```sh
# macOS and Linux
chmod +x rhds-mcp-linux-x64
mkdir -p ~/.local/bin
mv rhds-mcp-linux-x64 ~/.local/bin/rhds-mcp
~/.local/bin/rhds-mcp --version

# Verify the download
sha256sum -c SHA256SUMS
```

Windows users can rename the asset to `rhds-mcp.exe` and put it on `PATH`.

There is no npm package yet. `go install` is not supported because generated data is
not committed.

### Build from source

Requires Go, a C toolchain for cgo, and network access while generating data.

```sh
git clone https://github.com/RedHat-UX/mcp.git
cd mcp
make data build
./dist/bin/rhds-mcp --version
```

## Configure a client

Every example below assumes the binary is at `/absolute/path/to/rhds-mcp`. Use the
real absolute path, and use forward slashes or escaped backslashes in JSON on Windows.

The server reads the workspace from the client's MCP roots when the client provides
them. Otherwise it uses the process working directory.

### Claude Code

```sh
claude mcp add --transport stdio rhds -- /absolute/path/to/rhds-mcp
```

Add `--scope user` to make the server available in every project. The equivalent
project file is `.mcp.json`:

```json
{
  "mcpServers": {
    "rhds": {
      "type": "stdio",
      "command": "/absolute/path/to/rhds-mcp",
      "args": []
    }
  }
}
```

### Codex CLI

```sh
codex mcp add rhds -- /absolute/path/to/rhds-mcp
```

Or add this to `~/.codex/config.toml`, or to `.codex/config.toml` in a trusted project:

```toml
[mcp_servers.rhds]
command = "/absolute/path/to/rhds-mcp"
args = []
```

### VS Code

Create `.vscode/mcp.json` in the workspace:

```json
{
  "servers": {
    "rhds": {
      "type": "stdio",
      "command": "/absolute/path/to/rhds-mcp",
      "args": []
    }
  }
}
```

### Cursor

Create `~/.cursor/mcp.json` for every project, or `.cursor/mcp.json` for one workspace:

```json
{
  "mcpServers": {
    "rhds": {
      "command": "/absolute/path/to/rhds-mcp",
      "args": []
    }
  }
}
```

### Pi

```sh
pi mcp add rhds -- /absolute/path/to/rhds-mcp
```

Add `--local` to write project configuration instead of user configuration. The file
format is the same as Cursor: `~/.pi/agent/mcp.json` or `.pi/mcp.json`.

```json
{
  "mcpServers": {
    "rhds": {
      "command": "/absolute/path/to/rhds-mcp",
      "args": []
    }
  }
}
```

### OpenCode

Add this to `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "rhds": {
      "type": "local",
      "command": ["/absolute/path/to/rhds-mcp"],
      "enabled": true
    }
  }
}
```

### Other clients

Use the stdio configuration from the Cursor or Claude Code section. The server speaks
MCP over stdin and stdout and writes logs to stderr.

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

## License

GPL-3.0-or-later. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
