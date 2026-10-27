// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Limits bounds every workspace read. Zero values are replaced by
// DefaultLimits.
type Limits struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxScanFiles  int
	MaxScanDepth  int
}

// DefaultLimits are deliberately conservative: version resolution runs on
// every tool call that does not pass an explicit version, and a workspace can
// contain a very large dependency tree.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:  2 << 20,  // 2 MiB per file
		MaxTotalBytes: 16 << 20, // 16 MiB per resolution
		MaxScanFiles:  2000,
		MaxScanDepth:  24,
	}
}

func (l Limits) withDefaults() Limits {
	defaults := DefaultLimits()
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = defaults.MaxFileBytes
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if l.MaxScanFiles <= 0 {
		l.MaxScanFiles = defaults.MaxScanFiles
	}
	if l.MaxScanDepth <= 0 {
		l.MaxScanDepth = defaults.MaxScanDepth
	}
	return l
}

// errTooLarge reports a read that exceeded a configured bound.
var errTooLarge = errors.New("file exceeds the workspace read limit")

// withinRoot reports whether path stays inside root after cleaning. It does
// not resolve symlinks; callers reject symlinks with Lstat before reading.
func withinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	relative = filepath.Clean(relative)
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(relative)
}

// readWorkspaceFile reads one regular file, rejecting symlinks and enforcing
// the size bound. The path must stay inside root.
func readWorkspaceFile(root, relative string, maxBytes int64) ([]byte, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	if !withinRoot(root, path) {
		return nil, fmt.Errorf("path %q escapes the workspace root", relative)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to follow symlink %q", relative)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", relative)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes", errTooLarge, relative, info.Size())
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	// Read at most one byte more than the bound so a file that grows between
	// Lstat and Open is still rejected.
	payload, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("%w: %s", errTooLarge, relative)
	}
	return payload, nil
}

// workspaceReader enforces a total byte budget across all reads.
type workspaceReader struct {
	root      string
	limits    Limits
	totalRead int64
}

func newWorkspaceReader(root string, limits Limits) *workspaceReader {
	return &workspaceReader{root: root, limits: limits.withDefaults()}
}

func (r *workspaceReader) read(relative string) ([]byte, error) {
	if r.totalRead >= r.limits.MaxTotalBytes {
		return nil, fmt.Errorf("%w: total workspace read budget exhausted", errTooLarge)
	}
	remaining := r.limits.MaxTotalBytes - r.totalRead
	maxBytes := min(r.limits.MaxFileBytes, remaining)
	payload, err := readWorkspaceFile(r.root, relative, maxBytes)
	if err != nil {
		return nil, err
	}
	r.totalRead += int64(len(payload))
	return payload, nil
}
