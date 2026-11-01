// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package data

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func writeGzip(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(payload); err != nil {
		t.Fatalf("writing gzip payload: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func indexJSON(t *testing.T, artifacts ...Artifact) []byte {
	t.Helper()
	encoded, err := json.Marshal(Index{SchemaVersion: SchemaVersion, Artifacts: artifacts})
	if err != nil {
		t.Fatalf("marshaling index: %v", err)
	}
	return encoded
}

// validArtifact returns an artifact with non-empty integrity fields. It is
// used by tests that never read the artifact.
func validArtifact(pkg, version, kind, path string) Artifact {
	return Artifact{
		Package: pkg,
		Version: version,
		Kind:    kind,
		Path:    path,
		SHA256:  strings.Repeat("0", sha256.Size*2),
		Bytes:   1,
	}
}

func checksum(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// indexOnlyFS fails any read other than the index, proving that Open reads
// only the index at startup.
type indexOnlyFS struct {
	index []byte
	opens []string
}

func (f *indexOnlyFS) Open(name string) (fs.File, error) {
	f.opens = append(f.opens, name)
	if name != IndexPath {
		return nil, fmt.Errorf("unexpected read of %q", name)
	}
	return fstest.MapFS{IndexPath: &fstest.MapFile{Data: f.index}}.Open(name)
}

func TestEmbeddedIncludesGitkeep(t *testing.T) {
	if _, err := fs.Stat(Embedded(), "dist/.gitkeep"); err != nil {
		t.Fatalf("embedded dist/.gitkeep: %v", err)
	}
}

func TestOpenWithNoIndexReturnsNoDataError(t *testing.T) {
	fsys := fstest.MapFS{".gitkeep": &fstest.MapFile{}}

	_, err := Open(fsys)
	if err == nil {
		t.Fatal("Open() error = nil, want ErrNoData")
	}
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("Open() error = %v, want ErrNoData", err)
	}

	var structured *Error
	if !errors.As(err, &structured) {
		t.Fatalf("Open() error = %T, want *Error", err)
	}
	if structured.Code != "no_bundled_data" {
		t.Errorf("error code = %q, want %q", structured.Code, "no_bundled_data")
	}
	if structured.Hint == "" {
		t.Error("no-data error should carry a hint")
	}
}

func TestOpenReadsOnlyTheIndex(t *testing.T) {
	fsys := &indexOnlyFS{index: indexJSON(t,
		validArtifact("elements", "5.0.0", "manifest", "elements/5.0.0/custom-elements.json"))}

	store, err := Open(fsys)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := fsys.opens, []string{IndexPath}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Open() read %v, want only %q", got, IndexPath)
	}
	if store.Index().SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %d, want %d", store.Index().SchemaVersion, SchemaVersion)
	}
}

func TestReadDecompressesAndVerifiesChecksum(t *testing.T) {
	payload := []byte(`{"schemaVersion":"2.1.0","modules":[]}`)
	compressed := writeGzip(t, payload)

	fsys := fstest.MapFS{
		IndexPath: &fstest.MapFile{Data: indexJSON(t, Artifact{
			Package: "elements",
			Version: "5.0.0",
			Kind:    "manifest",
			Path:    "elements/5.0.0/custom-elements.json.gz",
			SHA256:  checksum(compressed),
			Bytes:   int64(len(compressed)),
		})},
		"elements/5.0.0/custom-elements.json.gz": &fstest.MapFile{Data: compressed},
	}

	store, err := Open(fsys)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	got, err := store.Read("elements", "5.0.0", "manifest")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("Read() = %q, want %q", got, payload)
	}

	// A second read is served from the in-memory cache.
	if _, err := store.Read("elements", "5.0.0", "manifest"); err != nil {
		t.Fatalf("second Read() error = %v", err)
	}
}

func TestReadChecksumMismatch(t *testing.T) {
	payload := []byte("payload")
	fsys := fstest.MapFS{
		IndexPath: &fstest.MapFile{Data: indexJSON(t, Artifact{
			Package: "tokens",
			Version: "3.2.0",
			Kind:    "tokens",
			Path:    "tokens/3.2.0/rhds.tokens.json",
			SHA256:  strings.Repeat("0", sha256.Size*2),
			Bytes:   int64(len(payload)),
		})},
		"tokens/3.2.0/rhds.tokens.json": &fstest.MapFile{Data: payload},
	}

	store, err := Open(fsys)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Read("tokens", "3.2.0", "tokens"); !errors.Is(err, ErrChecksum) {
		t.Fatalf("Read() error = %v, want ErrChecksum", err)
	}
}

func TestReadWrongByteCount(t *testing.T) {
	payload := []byte("payload")
	fsys := fstest.MapFS{
		IndexPath: &fstest.MapFile{Data: indexJSON(t, Artifact{
			Package: "tokens",
			Version: "3.2.0",
			Kind:    "tokens",
			Path:    "tokens/3.2.0/rhds.tokens.json",
			SHA256:  checksum(payload),
			Bytes:   int64(len(payload) + 1),
		})},
		"tokens/3.2.0/rhds.tokens.json": &fstest.MapFile{Data: payload},
	}

	store, err := Open(fsys)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_, err = store.Read("tokens", "3.2.0", "tokens")
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("Read() error = %v, want ErrChecksum", err)
	}
}

func TestReadCorruptGzip(t *testing.T) {
	stored := []byte("this is not gzip")
	fsys := fstest.MapFS{
		IndexPath: &fstest.MapFile{Data: indexJSON(t, Artifact{
			Package: "tokens",
			Version: "3.2.0",
			Kind:    "tokens",
			Path:    "tokens/3.2.0/rhds.tokens.json.gz",
			SHA256:  checksum(stored),
			Bytes:   int64(len(stored)),
		})},
		"tokens/3.2.0/rhds.tokens.json.gz": &fstest.MapFile{Data: stored},
	}

	store, err := Open(fsys)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_, err = store.Read("tokens", "3.2.0", "tokens")
	var structured *Error
	if !errors.As(err, &structured) {
		t.Fatalf("Read() error = %T, want *Error", err)
	}
	if structured.Code != "artifact_read_failed" {
		t.Errorf("error code = %q, want artifact_read_failed", structured.Code)
	}
}

func TestReadUnknownArtifactReturnsStructuredError(t *testing.T) {
	store, err := Open(fstest.MapFS{IndexPath: &fstest.MapFile{Data: indexJSON(t)}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	_, err = store.Read("elements", "9.9.9", "manifest")
	if err == nil {
		t.Fatal("Read() error = nil, want structured error")
	}
	var structured *Error
	if !errors.As(err, &structured) {
		t.Fatalf("Read() error = %T, want *Error", err)
	}
	if structured.Code != "artifact_not_found" {
		t.Errorf("error code = %q, want %q", structured.Code, "artifact_not_found")
	}
}

func TestOpenRejectsArtifactPathTraversal(t *testing.T) {
	fsys := fstest.MapFS{IndexPath: &fstest.MapFile{Data: indexJSON(t,
		validArtifact("elements", "5.0.0", "manifest", "../secret.json"))}}

	if _, err := Open(fsys); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("Open() error = %v, want ErrInvalidIndex", err)
	}
}

func TestOpenRejectsMissingIntegrityFields(t *testing.T) {
	tests := []struct {
		name     string
		artifact Artifact
	}{
		{
			name: "missing sha256",
			artifact: Artifact{
				Package: "elements", Version: "5.0.0", Kind: "manifest",
				Path: "manifest.json", Bytes: 1,
			},
		},
		{
			name: "missing bytes",
			artifact: Artifact{
				Package: "elements", Version: "5.0.0", Kind: "manifest",
				Path: "manifest.json", SHA256: strings.Repeat("0", sha256.Size*2),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{IndexPath: &fstest.MapFile{Data: indexJSON(t, tt.artifact)}}
			if _, err := Open(fsys); !errors.Is(err, ErrInvalidIndex) {
				t.Fatalf("Open() error = %v, want ErrInvalidIndex", err)
			}
		})
	}
}

func TestOpenRejectsDuplicateArtifacts(t *testing.T) {
	fsys := fstest.MapFS{IndexPath: &fstest.MapFile{Data: indexJSON(t,
		validArtifact("elements", "5.0.0", "manifest", "a.json"),
		validArtifact("elements", "5.0.0", "manifest", "b.json"),
	)}}
	if _, err := Open(fsys); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("Open() error = %v, want ErrInvalidIndex", err)
	}
}

func TestIndexVersionsAreSortedNewestFirst(t *testing.T) {
	store, err := Open(fstest.MapFS{IndexPath: &fstest.MapFile{Data: indexJSON(t,
		validArtifact("elements", "4.0.0", "manifest", "a"),
		validArtifact("elements", "10.0.0", "manifest", "b"),
		validArtifact("elements", "5.0.0", "manifest", "c"),
	)}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	got := store.Index().Versions("elements", "manifest")
	want := []string{"10.0.0", "5.0.0", "4.0.0"}
	if len(got) != len(want) {
		t.Fatalf("Versions() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Versions() = %v, want %v", got, want)
		}
	}
}

func TestOpenRejectsUnsupportedSchema(t *testing.T) {
	encoded, err := json.Marshal(Index{SchemaVersion: SchemaVersion + 1})
	if err != nil {
		t.Fatalf("marshaling index: %v", err)
	}
	if _, err := Open(fstest.MapFS{IndexPath: &fstest.MapFile{Data: encoded}}); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("Open() error = %v, want ErrInvalidIndex", err)
	}
}

func TestOpenRejectsDuplicateArtifactKeys(t *testing.T) {
	sha := strings.Repeat("a", 64)
	fsys := fstest.MapFS{IndexPath: &fstest.MapFile{Data: indexJSON(t,
		Artifact{Package: "elements", Version: "5.0.0", Kind: "manifest", Path: "a", SHA256: sha, Bytes: 1},
		Artifact{Package: "elements", Version: "5.0.0", Kind: "manifest", Path: "b", SHA256: sha, Bytes: 1},
	)}}
	if _, err := Open(fsys); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("Open() error = %v, want ErrInvalidIndex", err)
	}
}

// countingFS records how many times each artifact is opened.
type countingFS struct {
	fstest.MapFS
	opens map[string]int
}

func (f *countingFS) Open(name string) (fs.File, error) {
	if f.opens == nil {
		f.opens = make(map[string]int)
	}
	f.opens[name]++
	return f.MapFS.Open(name)
}

// ReadFile is promoted from fstest.MapFS and would bypass Open, so count it
// separately.
func (f *countingFS) ReadFile(name string) ([]byte, error) {
	if f.opens == nil {
		f.opens = make(map[string]int)
	}
	f.opens[name]++
	return f.MapFS.ReadFile(name)
}

func TestReadCachesDecompressedArtifact(t *testing.T) {
	payload := []byte("cached payload")
	compressed := writeGzip(t, payload)
	sum := sha256.Sum256(compressed)
	fsys := &countingFS{MapFS: fstest.MapFS{
		IndexPath: &fstest.MapFile{Data: indexJSON(t, Artifact{
			Package: "tokens",
			Version: "3.2.0",
			Kind:    "tokens",
			Path:    "tokens/3.2.0/rhds.tokens.json.gz",
			SHA256:  hex.EncodeToString(sum[:]),
			Bytes:   int64(len(compressed)),
		})},
		"tokens/3.2.0/rhds.tokens.json.gz": &fstest.MapFile{Data: compressed},
	}}

	store, err := Open(fsys)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if _, err := store.Read("tokens", "3.2.0", "tokens"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("tokens", "3.2.0", "tokens"); err != nil {
		t.Fatal(err)
	}
	if got := fsys.opens["tokens/3.2.0/rhds.tokens.json.gz"]; got != 1 {
		t.Fatalf("artifact was opened %d times, want 1 (cache miss)", got)
	}
}

func TestTokensForElements(t *testing.T) {
	store, err := Open(fstest.MapFS{IndexPath: &fstest.MapFile{Data: indexJSONWithMap(t,
		map[string]string{"5.0.0": "3.2.0"})}})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := store.Index().TokensForElements("5.0.0")
	if !ok || got != "3.2.0" {
		t.Fatalf("TokensForElements() = %q, %v", got, ok)
	}
	if _, ok := store.Index().TokensForElements("9.9.9"); ok {
		t.Fatal("TokensForElements() found an unknown version")
	}
}

func indexJSONWithMap(t *testing.T, mapping map[string]string) []byte {
	t.Helper()
	encoded, err := json.Marshal(Index{SchemaVersion: SchemaVersion, ElementsTokens: mapping})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
