#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright 2026 Red Hat, Inc.
#
# Check NFR-06: a stripped platform binary is 40 MB or less and a platform
# npm tarball is 16 MB or less. The npm platform packages are produced in
# Phase F; this script checks the wrapper tarball when npm is available.

set -eu

BINARY_LIMIT=41943040
TARBALL_LIMIT=16777216

# require_size rejects empty and non-numeric values so a malformed tool output
# fails the check instead of silently passing.
require_size() {
	case "${1:-}" in
	'' | *[!0-9]*)
		return 1
		;;
	*)
		return 0
		;;
	esac
}

if [ "${1:-}" = "--self-test" ]; then
	require_size 0 || exit 1
	require_size 123 || exit 1
	if require_size "" || require_size "nope" || require_size "12x"; then
		echo "size-check: require_size accepted a non-numeric value" >&2
		exit 1
	fi
	exit 0
fi

binary="${1:-dist/bin/rhds-mcp}"

if [ ! -f "$binary" ]; then
	echo "size-check: $binary does not exist; run make build first" >&2
	exit 1
fi

binary_size=$(wc -c <"$binary" | tr -d ' ')
printf 'size-check: %s is %s bytes\n' "$binary" "$binary_size"
if [ "$binary_size" -gt "$BINARY_LIMIT" ]; then
	echo "size-check: binary exceeds the 40 MB limit" >&2
	exit 1
fi

if command -v npm >/dev/null 2>&1 && [ -f npm/package.json ]; then
	pack_json=$(npm pack --dry-run --json --ignore-scripts ./npm 2>/dev/null) || {
		echo "size-check: npm pack failed" >&2
		exit 1
	}
	tarball_size=$(printf '%s\n' "$pack_json" |
		jq -er '.[0].size | select(type == "number")') || {
		echo "size-check: npm pack did not report a tarball size" >&2
		exit 1
	}
	if ! require_size "$tarball_size"; then
		echo "size-check: npm pack reported a non-numeric size: $tarball_size" >&2
		exit 1
	fi
	printf 'size-check: npm wrapper tarball is %s bytes\n' "$tarball_size"
	if [ "$tarball_size" -gt "$TARBALL_LIMIT" ]; then
		echo "size-check: tarball exceeds the 16 MB limit" >&2
		exit 1
	fi
fi

# Phase F produces per-platform packages under dist/npm. Check them when the
# build pipeline has staged them.
for tarball in dist/npm/*.tgz; do
	[ -e "$tarball" ] || continue
	tarball_size=$(wc -c <"$tarball" | tr -d ' ')
	printf 'size-check: platform tarball %s is %s bytes\n' "$tarball" "$tarball_size"
	if [ "$tarball_size" -gt "$TARBALL_LIMIT" ]; then
		echo "size-check: platform tarball $tarball exceeds the 16 MB limit" >&2
		exit 1
	fi
done
