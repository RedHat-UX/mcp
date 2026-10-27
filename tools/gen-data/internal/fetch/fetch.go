// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Package fetch downloads the pinned inputs for tools/gen-data. It is the only
// package in the module that performs network requests.
package fetch

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// Default endpoints. Tests override the fields on Client.
const (
	DefaultRegistryBase = "https://registry.npmjs.org"
	DefaultAPIBase      = "https://api.github.com"
	DefaultCodeloadBase = "https://codeload.github.com"
	DefaultRawBase      = "https://raw.githubusercontent.com"

	// DefaultMaxDownloadBytes caps a single npm tarball or API response.
	DefaultMaxDownloadBytes int64 = 256 << 20
	// DefaultMaxEntryBytes caps one extracted tarball entry.
	DefaultMaxEntryBytes int64 = 64 << 20
	// DefaultMaxExtractedBytes caps the total decompressed tarball size.
	DefaultMaxExtractedBytes int64 = 1 << 30
	// DefaultMaxEntries caps the number of headers in a tarball.
	DefaultMaxEntries = 100_000
)

// ExtractLimits bounds tarball extraction. Zero values use the defaults.
type ExtractLimits struct {
	MaxEntryBytes int64
	MaxTotalBytes int64
	MaxEntries    int
}

// Client fetches registry and GitHub inputs.
type Client struct {
	HTTP         *http.Client
	UserAgent    string
	Token        string
	RegistryBase string
	APIBase      string
	CodeloadBase string
	RawBase      string
	Retries      int
	// MaxDownloadBytes caps Download. Zero uses DefaultMaxDownloadBytes.
	MaxDownloadBytes int64
}

// New returns a client with production defaults.
func New() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: 5 * time.Minute,
		},
		UserAgent:        "rhds-mcp-gen-data/1.0",
		RegistryBase:     DefaultRegistryBase,
		APIBase:          DefaultAPIBase,
		CodeloadBase:     DefaultCodeloadBase,
		RawBase:          DefaultRawBase,
		Retries:          3,
		MaxDownloadBytes: DefaultMaxDownloadBytes,
	}
}

// RegistryMetadata is the subset of npm registry metadata gen-data needs.
type RegistryMetadata struct {
	Name     string                     `json:"name"`
	Versions map[string]RegistryVersion `json:"versions"`
}

// RegistryVersion is one published npm version.
type RegistryVersion struct {
	Version string         `json:"version"`
	Dist    RegistryDist   `json:"dist"`
	Raw     map[string]any `json:"-"`
}

// RegistryDist pins a tarball.
type RegistryDist struct {
	Integrity string `json:"integrity"`
	Tarball   string `json:"tarball"`
	Shasum    string `json:"shasum"`
}

// RegistryMetadata fetches npm's full package document.
func (c *Client) RegistryMetadata(ctx context.Context, packageName string) (*RegistryMetadata, error) {
	endpoint := c.RegistryBase + "/" + url.PathEscape(packageName)
	body, err := c.get(ctx, endpoint, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()

	var metadata RegistryMetadata
	data, err := readLimited(body, c.maxDownloadBytes())
	if err != nil {
		return nil, fmt.Errorf("decoding registry metadata for %s: %w", packageName, err)
	}
	if err := decodeJSON(bytes.NewReader(data), &metadata); err != nil {
		return nil, fmt.Errorf("decoding registry metadata for %s: %w", packageName, err)
	}
	if metadata.Name != packageName {
		return nil, fmt.Errorf("registry returned %q, want %q", metadata.Name, packageName)
	}
	return &metadata, nil
}

// Download fetches a URL and verifies its npm integrity string.
func (c *Client) Download(ctx context.Context, endpoint, integrity string) ([]byte, error) {
	body, err := c.get(ctx, endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	data, err := readLimited(body, c.maxDownloadBytes())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	if err := VerifyIntegrity(data, integrity); err != nil {
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	return data, nil
}

func (c *Client) maxDownloadBytes() int64 {
	if c.MaxDownloadBytes > 0 {
		return c.MaxDownloadBytes
	}
	return DefaultMaxDownloadBytes
}

// Open downloads a URL and returns the response body, bounded by the
// client's download limit. The caller closes it.
func (c *Client) Open(ctx context.Context, endpoint string) (io.ReadCloser, error) {
	body, err := c.get(ctx, endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	data, err := readLimited(body, c.maxDownloadBytes())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// VerifyIntegrity checks an npm "integrity" value. Only sha512 is accepted;
// npm has used sha512 for every @rhds release this project bundles.
func VerifyIntegrity(data []byte, integrity string) error {
	const prefix = "sha512-"
	if !strings.HasPrefix(integrity, prefix) {
		return fmt.Errorf("unsupported integrity %q; want a sha512 value", integrity)
	}
	want, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(integrity, prefix))
	if err != nil {
		return fmt.Errorf("decoding integrity: %w", err)
	}
	sum := sha512.Sum512(data)
	if !equalBytes(sum[:], want) {
		return errors.New("integrity mismatch")
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// ExtractTarGz reads a gzipped tarball, strips prefix, and returns the entries
// accepted by selectFunc. Keys are the paths after the prefix.
func ExtractTarGz(reader io.Reader, prefix string, selectFunc func(string) bool) (map[string][]byte, error) {
	return ExtractTarGzWithLimits(reader, prefix, selectFunc, ExtractLimits{})
}

// ExtractTarGzLimited is ExtractTarGz with an explicit per-entry byte cap.
func ExtractTarGzLimited(reader io.Reader, prefix string, selectFunc func(string) bool, maxEntryBytes int64) (map[string][]byte, error) {
	return ExtractTarGzWithLimits(reader, prefix, selectFunc, ExtractLimits{MaxEntryBytes: maxEntryBytes})
}

// ExtractTarGzWithLimits is ExtractTarGz with explicit extraction bounds.
func ExtractTarGzWithLimits(reader io.Reader, prefix string, selectFunc func(string) bool, limits ExtractLimits) (map[string][]byte, error) {
	return extractTarGz(reader, func(name string) string {
		return path.Clean(strings.TrimPrefix(name, prefix))
	}, selectFunc, limits)
}

// ExtractTarGzTree reads a gzipped tarball and strips its top-level directory.
// GitHub and codeload archives wrap everything in one component whose name
// depends on the ref, so callers cannot always know the prefix in advance.
func ExtractTarGzTree(reader io.Reader, selectFunc func(string) bool) (map[string][]byte, error) {
	return extractTarGz(reader, stripFirstComponent, selectFunc, ExtractLimits{})
}

func extractTarGz(reader io.Reader, transform func(string) string, selectFunc func(string) bool, limits ExtractLimits) (map[string][]byte, error) {
	if limits.MaxEntryBytes <= 0 {
		limits.MaxEntryBytes = DefaultMaxEntryBytes
	}
	if limits.MaxTotalBytes <= 0 {
		limits.MaxTotalBytes = DefaultMaxExtractedBytes
	}
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = DefaultMaxEntries
	}
	result := make(map[string][]byte)
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("opening gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()
	total := &totalLimitReader{reader: gz, remaining: limits.MaxTotalBytes, limit: limits.MaxTotalBytes}
	tr := tar.NewReader(total)
	entries := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar entry: %w", err)
		}
		entries++
		if entries > limits.MaxEntries {
			return nil, fmt.Errorf("tarball exceeds the %d entry limit", limits.MaxEntries)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if path.IsAbs(header.Name) {
			continue
		}
		cleanedName := path.Clean(header.Name)
		if cleanedName == ".." || strings.HasPrefix(cleanedName, "../") {
			continue
		}
		name := transform(header.Name)
		if name == "." || name == "" || path.IsAbs(name) || strings.HasPrefix(name, "../") {
			continue
		}
		if selectFunc != nil && !selectFunc(name) {
			continue
		}
		data, err := readLimited(tr, limits.MaxEntryBytes)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		result[name] = data
	}
	return result, nil
}

// totalLimitReader fails once the decompressed stream exceeds a byte budget.
type totalLimitReader struct {
	reader    io.Reader
	remaining int64
	limit     int64
}

func (r *totalLimitReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	if r.remaining < 0 {
		return n, fmt.Errorf("decompressed content exceeds the %d byte limit", r.limit)
	}
	return n, err
}

func stripFirstComponent(name string) string {
	name = strings.TrimPrefix(name, "./")
	if index := strings.IndexByte(name, '/'); index >= 0 {
		return name[index+1:]
	}
	return ""
}

// GitHubCommit resolves a tag or branch to a commit SHA.
func (c *Client) GitHubCommit(ctx context.Context, repo, ref string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/commits/%s", c.APIBase, repo, url.PathEscape(ref))
	body, err := c.get(ctx, endpoint, c.githubHeaders())
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	var response struct {
		SHA string `json:"sha"`
	}
	if err := decodeJSON(body, &response); err != nil {
		return "", err
	}
	if len(response.SHA) != 40 {
		return "", fmt.Errorf("GitHub returned an invalid commit for %s@%s: %q", repo, ref, response.SHA)
	}
	return response.SHA, nil
}

// GitHubSubmoduleCommit returns the gitlink SHA for a submodule path, or an
// empty string when the ref has no such submodule.
func (c *Client) GitHubSubmoduleCommit(ctx context.Context, repo, ref, submodulePath string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s",
		c.APIBase, repo, escapePath(submodulePath), url.QueryEscape(ref))
	body, err := c.get(ctx, endpoint, c.githubHeaders())
	if err != nil {
		var status *StatusError
		if errors.As(err, &status) && status.Code == http.StatusNotFound {
			return "", nil
		}
		return "", err
	}
	defer func() { _ = body.Close() }()

	var response struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	if err := decodeJSON(body, &response); err != nil {
		return "", err
	}
	if len(response.SHA) != 40 {
		return "", nil
	}
	return response.SHA, nil
}

// GitModulesURL returns the raw URL for a repository's .gitmodules at a commit.
func (c *Client) GitModulesURL(repo, commit string) string {
	return fmt.Sprintf("%s/%s/%s/.gitmodules", c.RawBase, repo, commit)
}

// ParseGitModules extracts submodule path to URL mappings.
func ParseGitModules(content string) map[string]string {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(content))
	var currentPath string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "[submodule"):
			currentPath = ""
		case strings.HasPrefix(line, "path"):
			if _, value, ok := strings.Cut(line, "="); ok {
				currentPath = strings.TrimSpace(value)
			}
		case strings.HasPrefix(line, "url"):
			if currentPath == "" {
				continue
			}
			if _, value, ok := strings.Cut(line, "="); ok {
				result[currentPath] = strings.TrimSpace(value)
			}
		}
	}
	return result
}

// DocsTarballURL returns the codeload URL for a pinned commit.
func (c *Client) DocsTarballURL(repo, commit string) string {
	return fmt.Sprintf("%s/%s/tar.gz/%s", c.CodeloadBase, repo, commit)
}

// AIGuidelinesTarballURL returns the codeload URL for the guidelines repo.
func (c *Client) AIGuidelinesTarballURL(repo, commit string) string {
	return fmt.Sprintf("%s/%s/tar.gz/%s", c.CodeloadBase, repo, commit)
}

// StatusError is an HTTP response with a non-2xx status.
type StatusError struct {
	Code int
	URL  string
	Body string
}

func (e *StatusError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 200 {
		body = body[:200]
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.URL, e.Code, body)
}

func (c *Client) githubHeaders() map[string]string {
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	if c.Token != "" {
		headers["Authorization"] = "Bearer " + c.Token
	}
	return headers
}

func (c *Client) get(ctx context.Context, endpoint string, headers map[string]string) (io.ReadCloser, error) {
	retries := c.Retries
	if retries < 1 {
		retries = 1
	}
	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("User-Agent", c.UserAgent)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := c.HTTP.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response.Body, nil
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		lastErr = &StatusError{
			Code: response.StatusCode,
			URL:  endpoint,
			Body: string(body),
		}
		if response.StatusCode == http.StatusNotFound {
			return nil, lastErr
		}
		if response.StatusCode < 500 {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

func escapePath(value string) string {
	parts := strings.Split(value, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// TokenFromEnvironment returns a GitHub token from the usual variables.
func TokenFromEnvironment() string {
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func decodeJSON(reader io.Reader, value any) error {
	decoder := json.NewDecoder(reader)
	return decoder.Decode(value)
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(reader)
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("content exceeds the %d byte limit", limit)
	}
	return data, nil
}
