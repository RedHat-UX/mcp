// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"runtime/debug"
	"strings"
)

// Module paths of the upstream libraries this binary mounts.
const (
	cemModulePath      = "bennypowers.dev/cem"
	asimonimModulePath = "bennypowers.dev/asimonim"
)

// unknownUpstreamVersion is the provenance version used when the build does
// not report a usable module version, for example a local build that reports
// "(devel)" or a missing dependency.
const unknownUpstreamVersion = "unknown"

// bundledUpstreamVersion reports the version of modulePath that this binary
// was built with, or [unknownUpstreamVersion] when the build info cannot say.
func bundledUpstreamVersion(modulePath string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownUpstreamVersion
	}
	return upstreamVersionFrom(info, modulePath)
}

// upstreamVersionFrom derives a dependency version from build info. A missing
// module, an empty version, and the "(devel)" placeholder all fall back to
// [unknownUpstreamVersion]; the version is never invented.
func upstreamVersionFrom(info *debug.BuildInfo, modulePath string) string {
	if info == nil || modulePath == "" {
		return unknownUpstreamVersion
	}
	for _, dependency := range info.Deps {
		if dependency == nil || dependency.Path != modulePath {
			continue
		}
		version := strings.TrimSpace(dependency.Version)
		if version == "" || version == "(devel)" {
			return unknownUpstreamVersion
		}
		return version
	}
	return unknownUpstreamVersion
}
