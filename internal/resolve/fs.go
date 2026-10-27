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
	MaxFileBytes   int64
	MaxTotalBytes  int64
	MaxScanFiles   int
	MaxScanDepth   int
	MaxScanEntries int
}

// DefaultLimits are deliberately conservative: version resolution runs on
// every tool call that does not pass an explicit version, and a workspace can
// contain a very large dependency tree.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:   2 << 20,  // 2 MiB per file
		MaxTotalBytes:  16 << 20, // 16 MiB per resolution
		MaxScanFiles:   2000,
		MaxScanDepth:   24,
		MaxScanEntries: 50000,
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
	if l.MaxScanEntries <= 0 {
		l.MaxScanEntries = defaults.MaxScanEntries
	}
	return l
}

// errTooLarge reports a read that exceeded a configured bound.
var errTooLarge = errors.New("file exceeds the workspace read limit")

// errPathEscape reports a workspace path whose resolved target is outside the
// workspace root, usually through a symlinked ancestor.
var errPathEscape = errors.New("workspace path resolves outside the workspace root")

// withinRoot reports whether path stays inside root after cleaning. Callers
// that need symlink safety must resolve both paths first.
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

// workspaceReader enforces path containment and a total byte budget across
// all reads. The workspace root is resolved once so ancestor symlinks cannot
// move it after the call begins.
type workspaceReader struct {
	root      string
	realRoot  string
	limits    Limits
	totalRead int64
}

func newWorkspaceReader(root string, limits Limits) *workspaceReader {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	return &workspaceReader{
		root:     root,
		realRoot: realRoot,
		limits:   limits.withDefaults(),
	}
}

func (r *workspaceReader) read(relative string) ([]byte, error) {
	if r.totalRead >= r.limits.MaxTotalBytes {
		return nil, fmt.Errorf("%w: total workspace read budget exhausted", errTooLarge)
	}
	remaining := r.limits.MaxTotalBytes - r.totalRead
	maxBytes := min(r.limits.MaxFileBytes, remaining)
	payload, err := r.readOne(relative, maxBytes)
	if err != nil {
		return nil, err
	}
	r.totalRead += int64(len(payload))
	return payload, nil
}

// readOne reads a regular file. It resolves every symlink in the path and
// verifies the real target stays under the real workspace root. It opens the
// resolved path and then re-checks the file identity, narrowing the
// time-of-check/time-of-use window.
func (r *workspaceReader) readOne(relative string, maxBytes int64) ([]byte, error) {
	path := filepath.Join(r.root, filepath.FromSlash(relative))
	if !withinRoot(r.root, path) {
		return nil, fmt.Errorf("%w: %q", errPathEscape, relative)
	}
	realPath, err := r.resolveWithin(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(realPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", relative)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes", errTooLarge, relative, info.Size())
	}
	file, err := os.Open(realPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("workspace file %q changed while it was opened", relative)
	}
	// Re-resolve after opening. A symlink swapped in after the first check
	// must not silently redirect the read.
	again, err := filepath.EvalSymlinks(path)
	if err != nil || again != realPath {
		return nil, fmt.Errorf("workspace file %q changed while it was opened", relative)
	}
	current, err := os.Lstat(realPath)
	if err != nil || !os.SameFile(current, opened) {
		return nil, fmt.Errorf("workspace file %q changed while it was opened", relative)
	}

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

// readDir reads a directory only when its resolved target stays under the
// workspace root.
func (r *workspaceReader) readDir(relative string) ([]os.DirEntry, error) {
	path := filepath.Join(r.root, filepath.FromSlash(relative))
	if !withinRoot(r.root, path) {
		return nil, fmt.Errorf("%w: %q", errPathEscape, relative)
	}
	realPath, err := r.resolveWithin(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(realPath)
	if err != nil {
		return nil, err
	}
	again, err := filepath.EvalSymlinks(path)
	if err != nil || again != realPath {
		return nil, fmt.Errorf("workspace directory %q changed while it was read", relative)
	}
	return entries, nil
}

func (r *workspaceReader) resolveWithin(path string) (string, error) {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !withinRoot(r.realRoot, realPath) {
		return "", fmt.Errorf("%w: %q resolves to %q", errPathEscape, path, realPath)
	}
	return realPath, nil
}
