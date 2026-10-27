// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package fetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	// Deterministic order for the fixture itself.
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	for _, name := range names {
		content := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type tarEntry struct {
	name     string
	content  string
	linkname string
	typeflag byte
}

func makeTarGzEntries(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o644,
			Typeflag: entry.typeflag,
			Linkname: entry.linkname,
		}
		if entry.typeflag == tar.TypeReg {
			header.Size = int64(len(entry.content))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func integrityOf(data []byte) string {
	sum := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func TestExtractTarGzSelectsAndStripsPrefix(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"package/custom-elements.json": `{"schemaVersion":"2.1.0"}`,
		"package/package.json":         `{"name":"@rhds/elements"}`,
		"package/README.md":            "readme",
	})
	entries, err := ExtractTarGz(bytes.NewReader(archive), "package/", func(name string) bool {
		return strings.HasSuffix(name, ".json")
	})
	if err != nil {
		t.Fatalf("ExtractTarGz() = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %v, want 2", entries)
	}
	if got := string(entries["custom-elements.json"]); got != `{"schemaVersion":"2.1.0"}` {
		t.Errorf("custom-elements.json = %q", got)
	}
}

func TestVerifyIntegrity(t *testing.T) {
	data := []byte("tarball bytes")
	good := integrityOf(data)
	if err := VerifyIntegrity(data, good); err != nil {
		t.Fatalf("VerifyIntegrity() = %v", err)
	}
	if err := VerifyIntegrity(data, "sha512-"+base64.StdEncoding.EncodeToString(make([]byte, 64))); err == nil {
		t.Fatal("VerifyIntegrity() accepted wrong hash")
	}
	// A sha1 shasum is accepted as a fallback only when explicitly requested.
	sum1 := hex.EncodeToString([]byte{0x01})
	if err := VerifyIntegrity(data, sum1); err == nil {
		t.Fatal("VerifyIntegrity() accepted a non-sha512 integrity string")
	}
}

func TestExtractTarGzRejectsTraversalAndSymlinks(t *testing.T) {
	archive := makeTarGzEntries(t, []tarEntry{
		{name: "package/../../evil.txt", content: "evil", typeflag: tar.TypeReg},
		{name: "/etc/passwd", content: "absolute", typeflag: tar.TypeReg},
		{name: "package/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		{name: "package/ok.txt", content: "okay", typeflag: tar.TypeReg},
	})
	entries, err := ExtractTarGz(bytes.NewReader(archive), "package/", nil)
	if err != nil {
		t.Fatalf("ExtractTarGz() = %v", err)
	}
	if len(entries) != 1 || string(entries["ok.txt"]) != "okay" {
		t.Fatalf("entries = %v, want only ok.txt", entries)
	}
}

func TestExtractTarGzEntryLimit(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"package/big.txt": "too large"})
	if _, err := ExtractTarGzLimited(bytes.NewReader(archive), "package/", nil, 2); err == nil {
		t.Fatal("ExtractTarGzLimited() accepted an entry over the cap")
	}
}

func TestExtractTarGzAggregateLimits(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"package/a.txt": "hello",
		"package/b.txt": "world",
	})
	if _, err := ExtractTarGzWithLimits(bytes.NewReader(archive), "package/", nil, ExtractLimits{
		MaxTotalBytes: 1,
	}); err == nil {
		t.Fatal("ExtractTarGzWithLimits() accepted content over the total cap")
	}
	if _, err := ExtractTarGzWithLimits(bytes.NewReader(archive), "package/", nil, ExtractLimits{
		MaxEntries: 1,
	}); err == nil {
		t.Fatal("ExtractTarGzWithLimits() accepted too many entries")
	}
}

func TestOpenLimit(t *testing.T) {
	data := []byte("downloaded tarball")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()

	client := New()
	client.MaxDownloadBytes = 4
	if _, err := client.Open(context.Background(), server.URL); err == nil {
		t.Fatal("Open() accepted a response over the cap")
	}
}

func TestRegistryMetadataLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"@rhds/elements","versions":{}}`))
	}))
	defer server.Close()

	client := New()
	client.RegistryBase = server.URL
	client.MaxDownloadBytes = 4
	if _, err := client.RegistryMetadata(context.Background(), "@rhds/elements"); err == nil {
		t.Fatal("RegistryMetadata() accepted a response over the cap")
	}
}

func TestDownloadLimit(t *testing.T) {
	data := []byte("downloaded tarball")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()

	client := New()
	client.MaxDownloadBytes = 4
	if _, err := client.Download(context.Background(), server.URL, integrityOf(data)); err == nil {
		t.Fatal("Download() accepted a response over the cap")
	}
}

func TestRegistryMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/@rhds%2felements" && r.URL.Path != "/@rhds/elements" {
			t.Errorf("unexpected path %q (escaped %q)", r.URL.Path, r.URL.EscapedPath())
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		_, _ = w.Write([]byte(`{
			"name": "@rhds/elements",
			"versions": {
				"5.0.0": {"dist": {"integrity": "sha512-abc", "tarball": "https://example.test/e.tgz", "shasum": "def"}}
			}
		}`))
	}))
	defer server.Close()

	client := New()
	client.RegistryBase = server.URL
	metadata, err := client.RegistryMetadata(context.Background(), "@rhds/elements")
	if err != nil {
		t.Fatalf("RegistryMetadata() = %v", err)
	}
	if metadata.Name != "@rhds/elements" {
		t.Errorf("name = %q", metadata.Name)
	}
	if got := metadata.Versions["5.0.0"].Dist.Integrity; got != "sha512-abc" {
		t.Errorf("integrity = %q", got)
	}
}

func TestDownloadVerifiesIntegrity(t *testing.T) {
	data := []byte("downloaded tarball")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()

	client := New()
	got, err := client.Download(context.Background(), server.URL, integrityOf(data))
	if err != nil {
		t.Fatalf("Download() = %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Download() = %q, want %q", got, data)
	}
	if _, err := client.Download(context.Background(), server.URL, integrityOf([]byte("other"))); err == nil {
		t.Fatal("Download() accepted a bad integrity hash")
	}
}

func TestGitHubCommitAndSubmodule(t *testing.T) {
	const commit = "d2321d5a1feb5b677682305dee2c073422eda67c"
	const submodule = "3e53486f83eba02fdecdb67390d1c4704bda5817"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/RedHat-UX/red-hat-design-system/commits/v5.0.0":
			_, _ = w.Write([]byte(`{"sha":"` + commit + `"}`))
		case "/repos/RedHat-UX/red-hat-design-system/contents/docs/ai-guidelines":
			if got := r.URL.Query().Get("ref"); got != "v5.0.0" {
				t.Errorf("ref = %q", got)
			}
			_, _ = w.Write([]byte(`{"type":"file","sha":"` + submodule + `"}`))
		case "/repos/RedHat-UX/red-hat-design-system/contents/docs":
			_, _ = w.Write([]byte(`[{"name":"ai-guidelines","type":"file","sha":"` + submodule + `"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New()
	client.APIBase = server.URL
	got, err := client.GitHubCommit(context.Background(), "RedHat-UX/red-hat-design-system", "v5.0.0")
	if err != nil || got != commit {
		t.Fatalf("GitHubCommit() = %q, %v; want %q", got, err, commit)
	}
	sub, err := client.GitHubSubmoduleCommit(context.Background(), "RedHat-UX/red-hat-design-system", "v5.0.0", "docs/ai-guidelines")
	if err != nil || sub != submodule {
		t.Fatalf("GitHubSubmoduleCommit() = %q, %v; want %q", sub, err, submodule)
	}
	missing, err := client.GitHubSubmoduleCommit(context.Background(), "RedHat-UX/red-hat-design-system", "v5.0.0", "docs/nope")
	if err != nil || missing != "" {
		t.Fatalf("missing submodule = %q, %v; want empty", missing, err)
	}
}

func TestGitModules(t *testing.T) {
	client := New()
	client.RawBase = "https://raw.example.test"
	// The parser is separate from the HTTP call so it can be tested directly.
	parsed := ParseGitModules(`
[submodule "docs/ai-guidelines"]
	path = docs/ai-guidelines
	url = https://github.com/project-felt/ai-guidelines.git
`)
	url, ok := parsed["docs/ai-guidelines"]
	if !ok || url != "https://github.com/project-felt/ai-guidelines.git" {
		t.Fatalf("ParseGitModules() = %v", parsed)
	}
}
