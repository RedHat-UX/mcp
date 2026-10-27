// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Package markdown parses the small Markdown subset used by the bundled RHDS
// docs and release notes. It keeps fenced code blocks intact so a "#" inside a
// code sample is never mistaken for a heading.
package markdown

import (
	"strings"
)

// Chunk is a Markdown heading and the prose below it. The intro chunk before
// the first heading has an empty Heading and Level 0.
type Chunk struct {
	Heading string
	Level   int
	Body    string
	// Line is the 1-based line number where the chunk starts.
	Line int
}

// Sections returns the sections at the requested heading level. Deeper
// headings stay inside their parent section's body, which is what release-note
// version sections need. Level 0 returns every chunk from Chunks.
func Sections(content []byte, level int) []Chunk {
	if level <= 0 {
		return Chunks(content)
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	var (
		sections []Chunk
		current  *Chunk
		body     []string
		fence    string
	)
	flush := func() {
		if current == nil {
			return
		}
		current.Body = trimBlankLines(strings.Join(body, "\n"))
		if current.Level == level {
			sections = append(sections, *current)
		}
		body = nil
	}
	for index, line := range lines {
		if fence != "" {
			if current != nil {
				body = append(body, line)
			}
			if closesFence(line, fence) {
				fence = ""
			}
			continue
		}
		if marker, ok := opensFence(line); ok {
			fence = marker
			if current != nil {
				body = append(body, line)
			}
			continue
		}
		heading, headingLevel, ok := atxHeading(line)
		if ok && headingLevel <= level {
			flush()
			current = &Chunk{Heading: heading, Level: headingLevel, Line: index + 1}
			continue
		}
		if current != nil {
			body = append(body, line)
		}
	}
	flush()
	return sections
}

// Chunks splits Markdown into heading-delimited chunks. Fenced code blocks and
// indented code are preserved byte for byte, and headings inside them are not
// treated as headings.
func Chunks(content []byte) []Chunk {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	var (
		chunks  []Chunk
		current = Chunk{Line: 1}
		body    []string
		fence   string
	)
	flush := func() {
		current.Body = trimBlankLines(strings.Join(body, "\n"))
		if current.Heading == "" && current.Body == "" {
			return
		}
		chunks = append(chunks, current)
	}
	for index, line := range lines {
		if fence != "" {
			body = append(body, line)
			if closesFence(line, fence) {
				fence = ""
			}
			continue
		}
		if marker, ok := opensFence(line); ok {
			fence = marker
			body = append(body, line)
			continue
		}
		if heading, level, ok := atxHeading(line); ok {
			flush()
			current = Chunk{Heading: heading, Level: level, Line: index + 1}
			body = nil
			continue
		}
		body = append(body, line)
	}
	flush()
	return chunks
}

// CodeBlock is one fenced code block.
type CodeBlock struct {
	Language string
	Content  string
	// StartLine is the 1-based line number of the opening fence.
	StartLine int
}

// CodeBlocks returns every fenced code block in order. It is not a general
// Markdown parser; it is enough for docs snippets and release-note examples.
func CodeBlocks(text string) []CodeBlock {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var (
		blocks  []CodeBlock
		fence   string
		content []string
		block   CodeBlock
	)
	for index, line := range lines {
		if fence == "" {
			marker, language, ok := fenceOpen(line)
			if !ok {
				continue
			}
			fence = marker
			block = CodeBlock{Language: language, StartLine: index + 1}
			content = nil
			continue
		}
		if closesFence(line, fence) {
			block.Content = strings.TrimRight(strings.Join(content, "\n"), "\n")
			blocks = append(blocks, block)
			fence = ""
			content = nil
			continue
		}
		content = append(content, line)
	}
	return blocks
}

func opensFence(line string) (string, bool) {
	marker, _, ok := fenceOpen(line)
	return marker, ok
}

func fenceOpen(line string) (marker, language string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return "", "", false
	}
	switch {
	case strings.HasPrefix(trimmed, "```"):
		marker = "```"
	case strings.HasPrefix(trimmed, "~~~"):
		marker = "~~~"
	default:
		return "", "", false
	}
	language = strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
	if fields := strings.Fields(language); len(fields) > 0 {
		language = fields[0]
	}
	return marker, language, true
}

func closesFence(line, marker string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return false
	}
	return strings.HasPrefix(trimmed, marker)
}

func atxHeading(line string) (text string, level int, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return "", 0, false
	}
	count := 0
	for count < len(trimmed) && trimmed[count] == '#' {
		count++
	}
	if count == 0 || count > 6 {
		return "", 0, false
	}
	rest := trimmed[count:]
	if rest != "" && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
		return "", 0, false
	}
	text = strings.TrimSpace(rest)
	// An ATX closing sequence is a run of '#' preceded by whitespace.
	if index := strings.LastIndexByte(text, '#'); index >= 0 {
		tail := text[index:]
		if strings.Trim(tail, "#") == "" && index > 0 && (text[index-1] == ' ' || text[index-1] == '\t') {
			text = strings.TrimSpace(text[:index])
		}
	}
	return text, count, true
}

func trimBlankLines(text string) string {
	text = strings.TrimLeft(text, "\n")
	text = strings.TrimRight(text, "\n")
	return text
}
