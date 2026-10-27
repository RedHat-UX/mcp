// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package resolve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
)

// templateExtensions are the only files scanned for import maps and CDN
// references.
var templateExtensions = map[string]bool{
	".html": true,
	".htm":  true,
	".twig": true,
}

// skippedTemplateDirectories are dependency and VCS trees. They are large and
// their templates are not the project's own source.
var skippedTemplateDirectories = map[string]bool{
	"node_modules": true,
	".git":         true,
	".hg":          true,
	".svn":         true,
}

// collectTemplateCandidates performs a bounded scan of HTML and Twig
// templates for import maps and CDN references.
func collectTemplateCandidates(
	root string,
	reader *workspaceReader,
	set *candidateSet,
	warnings *[]string,
) {
	scanned := 0
	stopped := false

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		depth := strings.Count(relative, "/")
		if entry.IsDir() {
			if skippedTemplateDirectories[entry.Name()] || depth >= reader.limits.MaxScanDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if depth > reader.limits.MaxScanDepth {
			return nil
		}
		if !templateExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			return nil
		}
		if scanned >= reader.limits.MaxScanFiles {
			if !stopped {
				*warnings = append(*warnings,
					fmt.Sprintf("template scan stopped after %d files", reader.limits.MaxScanFiles))
				stopped = true
			}
			return fs.SkipAll
		}
		scanned++
		payload, err := reader.read(relative)
		if err != nil {
			if errors.Is(err, errTooLarge) {
				*warnings = append(*warnings, fmt.Sprintf("skipped oversized template %s", relative))
				return nil
			}
			return nil
		}
		if err := scanTemplate(relative, payload, set); err != nil {
			*warnings = append(*warnings, fmt.Sprintf("could not parse %s: %v", relative, err))
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		*warnings = append(*warnings, fmt.Sprintf("template scan failed: %v", err))
	}
}

func scanTemplate(source string, payload []byte, set *candidateSet) error {
	document, err := html.Parse(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	walkHTML(document, func(node *html.Node) {
		switch node.Type {
		case html.ElementNode:
			for _, attribute := range node.Attr {
				switch strings.ToLower(attribute.Key) {
				case "src", "href":
					addReferenceCandidate(attribute.Val, source, set)
				}
			}
			if node.Data == "script" && nodeAttribute(node, "type") == "importmap" {
				if importMap := textContent(node); importMap != "" {
					scanImportMap(importMap, source, set)
				}
			}
		case html.TextNode:
			// Twig and inline module scripts often hold the CDN URL as text.
			addReferenceCandidate(node.Data, source, set)
		}
	})
	return nil
}

func walkHTML(node *html.Node, visit func(*html.Node)) {
	if node == nil {
		return
	}
	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

func nodeAttribute(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return attribute.Val
		}
	}
	return ""
}

func textContent(node *html.Node) string {
	var builder strings.Builder
	var collect func(*html.Node)
	collect = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(node)
	return builder.String()
}

func scanImportMap(document, source string, set *candidateSet) {
	var parsed struct {
		Imports map[string]string            `json:"imports"`
		Scopes  map[string]map[string]string `json:"scopes"`
	}
	if err := json.Unmarshal([]byte(document), &parsed); err != nil {
		return
	}
	for _, value := range parsed.Imports {
		addReferenceCandidate(value, source, set)
	}
	for _, scope := range parsed.Scopes {
		for _, value := range scope {
			addReferenceCandidate(value, source, set)
		}
	}
}

// addReferenceCandidate extracts @rhds/elements versions from a URL or import
// map target. It never mutates the document.
func addReferenceCandidate(reference, source string, set *candidateSet) {
	if !strings.Contains(reference, "@rhds/elements") {
		return
	}
	if version, ok := versionFromAtReference(reference); ok {
		set.add(version, reference, source, PrecedenceTemplate)
	}
}

// versionFromAtReference understands the two CDN forms in common use:
//
//	.../@rhds/elements@5.0.0/...
//	.../@rhds/elements/5.0.0/...
func versionFromAtReference(reference string) (string, bool) {
	const atReference = "@rhds/elements@"
	if index := strings.Index(reference, atReference); index >= 0 {
		rest := reference[index+len(atReference):]
		if version, ok := leadingVersion(rest); ok {
			return version, true
		}
	}
	const pathReference = "@rhds/elements/"
	if index := strings.Index(reference, pathReference); index >= 0 {
		rest := reference[index+len(pathReference):]
		if version, ok := leadingVersion(rest); ok {
			return version, true
		}
	}
	return "", false
}

func leadingVersion(text string) (string, bool) {
	parsed := ParseVersionSpecLeading(text)
	if !parsed.OK {
		return "", false
	}
	return parsed.Text, true
}

// ParseVersionSpecLeading is like ParseVersionSpec but only accepts a version
// at the start of the string. It avoids picking a version out of an unrelated
// query parameter.
func ParseVersionSpecLeading(text string) ParsedVersion {
	if text == "" {
		return ParsedVersion{}
	}
	start := 0
	if text[0] == 'v' || text[0] == 'V' {
		start = 1
	}
	parsed, ok := parseVersionAt(text, start)
	if !ok {
		return ParsedVersion{}
	}
	// Reject forms such as 5.0.0.1: only a delimiter or end may follow.
	end := start + len(parsed.Text)
	if end < len(text) && !isVersionDelimiter(text[end]) {
		return ParsedVersion{}
	}
	return parsed
}

func isVersionDelimiter(value byte) bool {
	switch value {
	case '/', '?', '#', ' ', '\t', '\n', '\r', '"', '\'', '>', '<', ')', ',', ';':
		return true
	default:
		return false
	}
}
