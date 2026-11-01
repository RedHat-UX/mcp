// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Package data embeds the generated RHDS data tree and loads artifacts lazily.
//
// The embedded tree may contain nothing but a .gitkeep file on a clean
// checkout. In that case Open returns ErrNoData and data tools report a
// structured error; the server still starts and serves its instructions.
package data

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"golang.org/x/mod/semver"
)

//go:embed all:dist
var embedded embed.FS

// Generate the dist tree from the committed lock. Run from the repository
// root with `make data`. It is a no-op when dist/index.json already matches
// the lock digest.
//
//go:generate go run ../../tools/gen-data -lock sources.lock.json -out dist

const (
	// IndexPath is the path of the data index inside the embedded dist tree.
	IndexPath = "index.json"

	// SchemaVersion is the index schema version this build understands.
	SchemaVersion = 1
)

// Artifact describes one generated file in the data tree.
type Artifact struct {
	// Package is "elements", "tokens", or "docs".
	Package string `json:"package"`
	// Version is the resolved RHDS package version or docs tag.
	Version string `json:"version"`
	// Kind is the artifact kind within the package, for example "manifest".
	Kind string `json:"kind"`
	// Path is relative to the root of the dist tree.
	Path string `json:"path"`
	// SHA256 and Bytes describe the stored file, before decompression.
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

type artifactKey struct {
	pkg     string
	version string
	kind    string
}

func artifactKeyOf(pkg, version, kind string) artifactKey {
	return artifactKey{pkg: pkg, version: version, kind: kind}
}

// Index is the startup manifest of the embedded data tree.
type Index struct {
	SchemaVersion int `json:"schemaVersion"`
	// GeneratorVersion changes when tools/gen-data output format or
	// normalization changes. `make data` regenerates when it does not match.
	GeneratorVersion int `json:"generatorVersion,omitempty"`
	// LockSHA256 is the digest of internal/data/sources.lock.json that
	// produced this tree. It lets `make data` decide whether it is stale.
	LockSHA256 string `json:"lockSHA256,omitempty"`
	// ElementsTokens maps an elements version to the highest bundled tokens
	// version that satisfies that release's own dependency range.
	ElementsTokens map[string]string `json:"elementsTokens,omitempty"`
	Artifacts      []Artifact        `json:"artifacts"`

	byKey map[artifactKey]Artifact
}

func (i *Index) build() error {
	i.byKey = make(map[artifactKey]Artifact, len(i.Artifacts))
	for _, artifact := range i.Artifacts {
		switch {
		case artifact.Package == "":
			return &Error{Code: "invalid_index", Message: "artifact is missing a package"}
		case artifact.Version == "":
			return &Error{Code: "invalid_index", Message: "artifact is missing a version"}
		case artifact.Kind == "":
			return &Error{Code: "invalid_index", Message: "artifact is missing a kind"}
		case artifact.Path == "":
			return &Error{Code: "invalid_index", Message: "artifact is missing a path"}
		case !fs.ValidPath(artifact.Path):
			return &Error{
				Code:    "invalid_index",
				Message: fmt.Sprintf("artifact path %q is not a clean relative path", artifact.Path),
			}
		case artifact.SHA256 == "":
			return &Error{
				Code:    "invalid_index",
				Message: fmt.Sprintf("artifact %s is missing a sha256", artifact.Path),
			}
		case artifact.Bytes <= 0:
			return &Error{
				Code:    "invalid_index",
				Message: fmt.Sprintf("artifact %s is missing a byte count", artifact.Path),
			}
		}
		key := artifactKeyOf(artifact.Package, artifact.Version, artifact.Kind)
		if _, exists := i.byKey[key]; exists {
			return &Error{
				Code:    "invalid_index",
				Message: fmt.Sprintf("duplicate artifact for %s@%s %s", artifact.Package, artifact.Version, artifact.Kind),
			}
		}
		i.byKey[key] = artifact
	}
	return nil
}

// Artifact returns the artifact matching the package, version, and kind.
func (i *Index) Artifact(pkg, version, kind string) (Artifact, bool) {
	artifact, ok := i.byKey[artifactKeyOf(pkg, version, kind)]
	return artifact, ok
}

// Has reports whether the index describes an artifact.
func (i *Index) Has(pkg, version, kind string) bool {
	_, ok := i.Artifact(pkg, version, kind)
	return ok
}

// TokensForElements returns the tokens version coupled to an elements
// release, as generated from that release's package.json dependency range.
func (i *Index) TokensForElements(elementsVersion string) (string, bool) {
	version, ok := i.ElementsTokens[elementsVersion]
	return version, ok
}

// Versions returns the versions that have an artifact of the given kind,
// newest first.
func (i *Index) Versions(pkg, kind string) []string {
	versions := make([]string, 0)
	for _, artifact := range i.Artifacts {
		if artifact.Package == pkg && artifact.Kind == kind {
			versions = append(versions, artifact.Version)
		}
	}
	sort.Slice(versions, func(a, b int) bool {
		return compareVersions(versions[a], versions[b]) > 0
	})
	return versions
}

func canonicalVersion(version string) string {
	return semver.Canonical("v" + strings.TrimPrefix(version, "v"))
}

func compareVersions(a, b string) int {
	ca, cb := canonicalVersion(a), canonicalVersion(b)
	if ca != "" && cb != "" {
		return semver.Compare(ca, cb)
	}
	return strings.Compare(a, b)
}

// Error is a structured data error suitable for a tool response.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Hint == "" {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message + " (" + e.Hint + ")"
}

// Is reports whether err is a data error with the same code.
func (e *Error) Is(err error) bool {
	var other *Error
	return errors.As(err, &other) && other.Code == e.Code
}

var (
	// ErrNoData means the binary was built without generated data.
	ErrNoData = &Error{
		Code:    "no_bundled_data",
		Message: "this binary has no bundled RHDS data",
		Hint:    "build with generated data: make data build",
	}
	// ErrArtifactNotFound means the index does not describe the requested artifact.
	ErrArtifactNotFound = &Error{
		Code:    "artifact_not_found",
		Message: "the requested artifact is not bundled",
		Hint:    "call rhds_resolve_version to list bundled versions",
	}
	// ErrInvalidIndex means the embedded index is malformed.
	ErrInvalidIndex = &Error{
		Code:    "invalid_index",
		Message: "the bundled data index is invalid",
		Hint:    "regenerate the data and rebuild",
	}
	// ErrChecksum means an artifact did not match its recorded checksum.
	ErrChecksum = &Error{
		Code:    "checksum_mismatch",
		Message: "a bundled artifact failed checksum verification",
		Hint:    "regenerate the data and rebuild",
	}
)

// Embedded returns the embedded filesystem. It contains the dist tree.
func Embedded() fs.FS {
	return embedded
}

// Store reads artifacts from an embedded or fixture filesystem.
type Store struct {
	fsys  fs.FS
	index *Index

	mu    sync.RWMutex
	cache map[artifactKey][]byte
}

// Open reads the data index. It does not read any artifact.
func Open(fsys fs.FS) (*Store, error) {
	if fsys == nil {
		return nil, ErrNoData
	}
	encoded, err := fs.ReadFile(fsys, IndexPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoData
		}
		return nil, &Error{
			Code:    "index_read_failed",
			Message: "could not read the bundled data index",
			Hint:    err.Error(),
		}
	}

	var index Index
	if err := json.Unmarshal(encoded, &index); err != nil {
		return nil, &Error{
			Code:    "invalid_index",
			Message: "the bundled data index is not valid JSON",
			Hint:    err.Error(),
		}
	}
	if index.SchemaVersion != SchemaVersion {
		return nil, &Error{
			Code: "invalid_index",
			Message: fmt.Sprintf(
				"unsupported data index schema version %d; this build understands %d",
				index.SchemaVersion, SchemaVersion),
			Hint: "upgrade rhds-mcp or regenerate the data",
		}
	}
	if err := index.build(); err != nil {
		return nil, err
	}

	return &Store{
		fsys:  fsys,
		index: &index,
		cache: make(map[artifactKey][]byte),
	}, nil
}

// Index returns the parsed data index.
func (s *Store) Index() *Index {
	return s.index
}

// Has reports whether the store contains an artifact.
func (s *Store) Has(pkg, version, kind string) bool {
	if s == nil || s.index == nil {
		return false
	}
	return s.index.Has(pkg, version, kind)
}

// Read returns an artifact, decompressing it if it has a .gz suffix. Results
// are cached in memory so a version is only decompressed once.
func (s *Store) Read(pkg, version, kind string) ([]byte, error) {
	if s == nil || s.index == nil {
		return nil, ErrNoData
	}
	artifact, ok := s.index.Artifact(pkg, version, kind)
	if !ok {
		return nil, &Error{
			Code: "artifact_not_found",
			Message: fmt.Sprintf(
				"no %s artifact for %s %s", kind, pkg, version),
			Hint: ErrArtifactNotFound.Hint,
		}
	}

	key := artifactKeyOf(pkg, version, kind)
	s.mu.RLock()
	cached, ok := s.cache[key]
	s.mu.RUnlock()
	if ok {
		return cached, nil
	}

	stored, err := fs.ReadFile(s.fsys, artifact.Path)
	if err != nil {
		return nil, &Error{
			Code:    "artifact_read_failed",
			Message: fmt.Sprintf("could not read %s", artifact.Path),
			Hint:    err.Error(),
		}
	}
	if int64(len(stored)) != artifact.Bytes {
		return nil, &Error{
			Code: "checksum_mismatch",
			Message: fmt.Sprintf(
				"%s is %d bytes, index records %d", artifact.Path, len(stored), artifact.Bytes),
			Hint: ErrChecksum.Hint,
		}
	}
	sum := sha256.Sum256(stored)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), artifact.SHA256) {
		return nil, &Error{
			Code:    "checksum_mismatch",
			Message: fmt.Sprintf("%s failed checksum verification", artifact.Path),
			Hint:    ErrChecksum.Hint,
		}
	}

	payload := stored
	if strings.HasSuffix(artifact.Path, ".gz") {
		reader, err := gzip.NewReader(bytes.NewReader(stored))
		if err != nil {
			return nil, &Error{
				Code:    "artifact_read_failed",
				Message: fmt.Sprintf("could not decompress %s", artifact.Path),
				Hint:    err.Error(),
			}
		}
		defer func() { _ = reader.Close() }()
		payload, err = io.ReadAll(reader)
		if err != nil {
			return nil, &Error{
				Code:    "artifact_read_failed",
				Message: fmt.Sprintf("could not decompress %s", artifact.Path),
				Hint:    err.Error(),
			}
		}
	}

	s.mu.Lock()
	if existing, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.cache[key] = payload
	s.mu.Unlock()

	return payload, nil
}
