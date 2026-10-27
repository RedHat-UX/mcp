// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package normalize

import (
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
	fence := ""
	htmlCodeDepth := 0
	for _, line := range body {
		trimmed := strings.TrimLeft(line, " \t")
		if fence != "" {
			processed = append(processed, line)
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if marker := fenceMarker(trimmed); marker != "" {
			fence = marker
			processed = append(processed, line)
			continue
		}
		if isIndentedCode(line) {
			processed = append(processed, line)
			continue
		}
		if htmlCodeDepth > 0 || opensHTMLCode(line) {
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
			return stripHTMLImages(segment)
		})
		removed = removed || changed
		if removed && strings.TrimSpace(rewritten) == "" {
			// An image-only line is dropped entirely.
			continue
		}
		processed = append(processed, rewritten)
	}

	content := collapseBlankLines(strings.Join(processed, "\n"))
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

func isIndentedCode(line string) bool {
	if !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "\t") {
		return false
	}
	// Indented HTML in these docs is layout, not a code block. Treat a line
	// that begins with a tag as prose so its images are processed.
	trimmed := strings.TrimLeft(line, " \t")
	return !strings.HasPrefix(trimmed, "<")
}

// opensHTMLCode reports whether a line starts an HTML block whose contents are
// a code sample rather than prose. Images inside these blocks are examples,
// not asset references.
func opensHTMLCode(line string) bool {
	lower := strings.ToLower(line)
	for _, tag := range []string{"<rh-code-block", "<pre", "<code", "<script"} {
		if strings.Contains(lower, tag) {
			return true
		}
	}
	return false
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

func collapseBlankLines(text string) string {
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return text
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
