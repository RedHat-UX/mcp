// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package normalize

import (
	"errors"
	"io"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// MarkdownResult is processed documentation prose.
type MarkdownResult struct {
	Content []byte
	Title   string
}

// ProcessMarkdown strips 11ty frontmatter, drops image references, preserves
// fenced code, tables, and indentation, and prepends a source comment.
func ProcessMarkdown(input []byte, source string) MarkdownResult {
	text := strings.ReplaceAll(string(input), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimPrefix(text, "\uFEFF")

	lines := strings.Split(text, "\n")
	title := ""
	var body []string
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		end := -1
		for i := 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == "---" || trimmed == "..." {
				end = i
				break
			}
			if title == "" {
				if key, value, ok := strings.Cut(trimmed, ":"); ok && strings.TrimSpace(key) == "title" {
					title = unquote(strings.TrimSpace(value))
				}
			}
		}
		if end >= 0 {
			body = lines[end+1:]
		} else {
			body = lines
		}
	} else {
		body = lines
	}

	processed := make([]string, 0, len(body))
	prose := make([]string, 0, len(body))
	htmlTagOpen := false
	flushProse := func() {
		if len(prose) == 0 {
			return
		}
		joined := strings.Join(collapseBlankLines(prose), "\n")
		protected, spans := protectInlineCode(joined)
		protected = stripHTMLImages(protected)
		processed = append(processed, strings.Split(restoreInlineCode(protected, spans), "\n")...)
		prose = prose[:0]
		htmlTagOpen = false
	}

	fence := ""
	htmlCodeDepth := 0
	for _, line := range body {
		trimmed := strings.TrimLeft(line, " \t")
		if fence != "" {
			flushProse()
			processed = append(processed, line)
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if marker := fenceMarker(trimmed); marker != "" {
			flushProse()
			fence = marker
			processed = append(processed, line)
			continue
		}
		if !htmlTagOpen && isIndentedCode(line) {
			flushProse()
			processed = append(processed, line)
			continue
		}
		if htmlCodeDepth > 0 || startsHTMLCodeBlock(line) {
			flushProse()
			processed = append(processed, line)
			htmlCodeDepth += htmlCodeDelta(line)
			if htmlCodeDepth < 0 {
				htmlCodeDepth = 0
			}
			continue
		}

		if title == "" {
			if heading := headingText(line); heading != "" {
				title = heading
			}
		}

		var removed bool
		rewritten, changed := mapOutsideInlineCode(line, func(segment string) string {
			segment, found := stripMarkdownImages(segment)
			removed = removed || found
			return segment
		})
		removed = removed || changed
		if removed && strings.TrimSpace(rewritten) == "" {
			// An image-only line is dropped entirely.
			continue
		}
		prose = append(prose, rewritten)
		htmlTagOpen = tagOpenAfter(rewritten, htmlTagOpen)
	}
	flushProse()

	content := strings.Join(processed, "\n")
	content = strings.TrimLeft(content, "\n")
	content = strings.TrimRight(content, "\n")
	if content != "" {
		content += "\n"
	}
	if source != "" {
		content = "<!-- source: " + source + " -->\n\n" + content
	}
	return MarkdownResult{Content: []byte(content), Title: title}
}

func fenceMarker(line string) string {
	if strings.HasPrefix(line, "```") {
		return "```"
	}
	if strings.HasPrefix(line, "~~~") {
		return "~~~"
	}
	return ""
}

const inlineCodePlaceholderPrefix = "\x00rhds-inline-code-"

// protectInlineCode replaces Markdown code spans with placeholders so the
// HTML tokenizer does not treat `<img>` inside them as markup.
func protectInlineCode(text string) (string, []string) {
	var builder strings.Builder
	builder.Grow(len(text))
	spans := make([]string, 0)
	for i := 0; i < len(text); {
		if text[i] != '`' {
			builder.WriteByte(text[i])
			i++
			continue
		}
		run := 1
		for i+run < len(text) && text[i+run] == '`' {
			run++
		}
		closing := strings.Index(text[i+run:], strings.Repeat("`", run))
		if closing < 0 {
			builder.WriteString(text[i:])
			break
		}
		end := i + run + closing + run
		placeholder := inlineCodePlaceholderPrefix + strconv.Itoa(len(spans)) + "\x00"
		spans = append(spans, text[i:end])
		builder.WriteString(placeholder)
		i = end
	}
	return builder.String(), spans
}

func restoreInlineCode(text string, spans []string) string {
	for i, span := range spans {
		placeholder := inlineCodePlaceholderPrefix + strconv.Itoa(i) + "\x00"
		text = strings.ReplaceAll(text, placeholder, span)
	}
	return text
}

// tagOpenAfter reports whether the line leaves an HTML start tag open. It is
// used so continuation lines of a multi-line <img> tag are treated as prose,
// not as indented code.
func tagOpenAfter(line string, open bool) bool {
	index := 0
	for index < len(line) {
		if !open {
			offset := strings.IndexByte(line[index:], '<')
			if offset < 0 {
				return false
			}
			position := index + offset
			if position+1 >= len(line) {
				return true
			}
			next := line[position+1]
			if (next < 'a' || next > 'z') && (next < 'A' || next > 'Z') &&
				next != '/' && next != '!' && next != '?' {
				index = position + 1
				continue
			}
			open = true
			index = position + 1
			continue
		}

		quote := byte(0)
		for ; index < len(line); index++ {
			c := line[index]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
				continue
			}
			switch c {
			case '\'', '"':
				quote = c
			case '>':
				open = false
				index++
			}
			if !open {
				break
			}
		}
		if index >= len(line) {
			return open
		}
	}
	return open
}

// collapseBlankLines collapses runs of blank lines in prose only. Code blocks
// are never passed through it.
func collapseBlankLines(lines []string) []string {
	result := make([]string, 0, len(lines))
	previousBlank := false
	for _, line := range lines {
		if line == "" && previousBlank {
			continue
		}
		result = append(result, line)
		previousBlank = line == ""
	}
	return result
}
func isIndentedCode(line string) bool {
	if !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "\t") {
		return false
	}
	// Indented HTML in these docs is layout, not a code block. Treat a line
	// that begins with a tag as prose so its images are processed.
	trimmed := strings.TrimLeft(line, " \t")
	return !strings.HasPrefix(trimmed, "<")
}

// startsHTMLCodeBlock reports whether a line begins an HTML block whose
// contents are a code sample. Requiring the tag at the start of the line
// avoids treating prose mentions of `<code>` or `<script>` as sticky blocks.
func startsHTMLCodeBlock(line string) bool {
	trimmed := strings.ToLower(strings.TrimLeft(line, " \t"))
	for _, tag := range []string{"rh-code-block", "pre", "script", "code"} {
		if hasTagPrefix(trimmed, "<"+tag) {
			return true
		}
	}
	return false
}

func hasTagPrefix(line, tag string) bool {
	if !strings.HasPrefix(line, tag) {
		return false
	}
	if len(line) == len(tag) {
		return true
	}
	switch line[len(tag)] {
	case ' ', '\t', '>', '/', '\n':
		return true
	default:
		return false
	}
}

func htmlCodeDelta(line string) int {
	lower := strings.ToLower(line)
	delta := 0
	for _, tag := range []string{"rh-code-block", "pre", "code", "script"} {
		delta += strings.Count(lower, "<"+tag) - strings.Count(lower, "</"+tag)
	}
	return delta
}

// mapOutsideInlineCode applies fn to text outside Markdown code spans. Code
// spans delimited by backticks are preserved byte for byte.
func mapOutsideInlineCode(line string, fn func(string) string) (string, bool) {
	var builder strings.Builder
	builder.Grow(len(line))
	removed := false
	for i := 0; i < len(line); {
		if line[i] != '`' {
			start := i
			for i < len(line) && line[i] != '`' {
				i++
			}
			original := line[start:i]
			transformed := fn(original)
			if transformed != original {
				removed = true
			}
			builder.WriteString(transformed)
			continue
		}
		run := 1
		for i+run < len(line) && line[i+run] == '`' {
			run++
		}
		closing := strings.Index(line[i+run:], strings.Repeat("`", run))
		if closing < 0 {
			builder.WriteString(line[i:])
			break
		}
		end := i + run + closing + run
		builder.WriteString(line[i:end])
		i = end
	}
	return builder.String(), removed
}

func headingText(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return ""
	}
	count := 0
	for count < len(trimmed) && trimmed[count] == '#' {
		count++
	}
	if count == 0 || count > 6 {
		return ""
	}
	rest := trimmed[count:]
	if !strings.HasPrefix(rest, " ") {
		return ""
	}
	return strings.TrimSpace(rest)
}

// stripMarkdownImages rewrites ![alt](url) and ![alt][ref] to alt text. It
// returns removed=true when at least one image reference was found.
func stripMarkdownImages(line string) (string, bool) {
	var builder strings.Builder
	builder.Grow(len(line))
	removed := false
	for i := 0; i < len(line); {
		if line[i] != '!' || i+1 >= len(line) || line[i+1] != '[' ||
			(i > 0 && line[i-1] == '\\') {
			builder.WriteByte(line[i])
			i++
			continue
		}
		closeBracket := strings.IndexByte(line[i+2:], ']')
		if closeBracket < 0 {
			builder.WriteByte(line[i])
			i++
			continue
		}
		closeBracket += i + 2
		alt := line[i+2 : closeBracket]
		next := closeBracket + 1
		var end int
		switch {
		case next < len(line) && line[next] == '(':
			closing := strings.IndexByte(line[next+1:], ')')
			if closing < 0 {
				builder.WriteByte(line[i])
				i++
				continue
			}
			end = next + 1 + closing + 1
		case next < len(line) && line[next] == '[':
			closing := strings.IndexByte(line[next+1:], ']')
			if closing < 0 {
				builder.WriteByte(line[i])
				i++
				continue
			}
			end = next + 1 + closing + 1
		default:
			builder.WriteByte(line[i])
			i++
			continue
		}
		builder.WriteString(alt)
		removed = true
		i = end
	}
	return builder.String(), removed
}

// stripHTMLImages removes <img> tags and keeps their alt text. It uses the
// HTML tokenizer rather than regular expressions, per the project rules.
func stripHTMLImages(line string) string {
	if !strings.Contains(strings.ToLower(line), "<img") {
		return line
	}
	var builder strings.Builder
	tokenizer := html.NewTokenizer(strings.NewReader(line))
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			if len(tokenizer.Raw()) > 0 || !errors.Is(tokenizer.Err(), io.EOF) {
				// Malformed markup: leave the line untouched rather than
				// truncating everything after the error.
				return line
			}
			break
		}
		token := tokenizer.Token()
		if token.Data == "img" {
			switch tokenType {
			case html.StartTagToken, html.SelfClosingTagToken:
				for _, attribute := range token.Attr {
					if attribute.Key == "alt" {
						builder.WriteString(attribute.Val)
						break
					}
				}
			case html.EndTagToken:
				// Drop the closing tag.
			}
			continue
		}
		builder.Write(tokenizer.Raw())
	}
	return builder.String()
}

func unquote(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}
