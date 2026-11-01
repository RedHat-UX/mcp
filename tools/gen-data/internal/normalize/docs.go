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
			if title == "" && strings.TrimLeft(lines[i], " \t") == lines[i] {
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
		lines := collapseBlankLines(prose)
		for start := 0; start < len(lines); {
			end := start + 1
			open := tagOpenAfter(lines[start], false)
			for open && end < len(lines) && lines[end] != "" {
				open = tagOpenAfter(lines[end], open)
				end++
			}
			// Strip images one tag-span chunk at a time. If malformed markup
			// forces stripHTMLImages to leave a chunk untouched, the damage is
			// limited to that chunk instead of the whole prose block.
			chunk := strings.Join(lines[start:end], "\n")
			protected, spans := protectInlineCode(chunk)
			protected = stripHTMLImages(protected)
			processed = append(processed, strings.Split(restoreInlineCode(protected, spans), "\n")...)
			start = end
		}
		prose = prose[:0]
		htmlTagOpen = false
	}

	var openFence *fenceRun
	htmlCodeDepth := 0
	htmlScriptOpen := false
	for _, line := range body {
		if openFence != nil {
			flushProse()
			processed = append(processed, line)
			if openFence.closes(line) {
				openFence = nil
			}
			continue
		}
		if !isIndentedCode(line) {
			if marker, ok := fenceMarker(line); ok {
				flushProse()
				openFence = &marker
				processed = append(processed, line)
				continue
			}
		}
		if !htmlTagOpen && isIndentedCode(line) {
			flushProse()
			processed = append(processed, line)
			continue
		}
		if htmlCodeDepth > 0 || startsHTMLCodeBlock(line) {
			flushProse()
			processed = append(processed, line)
			delta, scriptOpen := htmlCodeDelta(line, htmlScriptOpen)
			htmlScriptOpen = scriptOpen
			htmlCodeDepth += delta
			if htmlCodeDepth < 0 {
				htmlCodeDepth = 0
			}
			if htmlCodeDepth == 0 {
				htmlScriptOpen = false
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

// fenceRun is an open CommonMark code fence. Closing requires the same
// character, a run at least as long, and only whitespace afterwards.
type fenceRun struct {
	char   byte
	length int
}

// fenceMarker recognizes an opening fence indented by at most three spaces.
// Tabs and deeper indentation are indented code, not fences.
func fenceMarker(line string) (fenceRun, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 {
		return fenceRun{}, false
	}
	rest := line[indent:]
	if rest == "" || (rest[0] != '`' && rest[0] != '~') {
		return fenceRun{}, false
	}
	char := rest[0]
	length := 0
	for length < len(rest) && rest[length] == char {
		length++
	}
	if length < 3 {
		return fenceRun{}, false
	}
	if char == '`' && strings.Contains(rest[length:], "`") {
		return fenceRun{}, false
	}
	return fenceRun{char: char, length: length}, true
}

func (f fenceRun) closes(line string) bool {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 {
		return false
	}
	rest := line[indent:]
	length := 0
	for length < len(rest) && rest[length] == f.char {
		length++
	}
	if length < f.length {
		return false
	}
	return strings.TrimSpace(rest[length:]) == ""
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
	// that begins with a tag as prose so its images are processed. Trade-off:
	// an indented code sample starting with "<" is preserved as prose. See
	// docs/PLAN.md section 5.5.
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

// htmlCodeDelta returns the change in HTML code-block depth for one line and
// whether the line leaves a script element open. A script element's content is
// raw text, so tags inside it (including a literal <script>) do not change the
// depth; only </script> closes it. Counting raw substrings gets this wrong when
// docs embed script samples that themselves contain script markup.
func htmlCodeDelta(line string, inScript bool) (int, bool) {
	lower := strings.ToLower(line)
	delta := 0
	for i := 0; i < len(lower); {
		next := strings.IndexByte(lower[i:], '<')
		if next < 0 {
			break
		}
		i += next
		rest := lower[i:]
		if inScript {
			if hasTagPrefix(rest, "</script") {
				delta--
				inScript = false
				i += len("</script")
				continue
			}
			i++
			continue
		}
		if hasTagPrefix(rest, "<script") {
			delta++
			inScript = true
			i += len("<script")
			continue
		}
		matched := false
		for _, tag := range []string{"rh-code-block", "pre", "code"} {
			if hasTagPrefix(rest, "<"+tag) {
				delta++
				i += len(tag) + 1
				matched = true
				break
			}
			if hasTagPrefix(rest, "</"+tag) {
				delta--
				i += len(tag) + 2
				matched = true
				break
			}
		}
		if !matched {
			i++
		}
	}
	return delta, inScript
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
			closing, ok := scanImageDestination(line, next)
			if !ok {
				builder.WriteByte(line[i])
				i++
				continue
			}
			end = closing
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

// scanImageDestination returns the index just past the closing parenthesis of
// an inline image destination starting at open, where line[open] == '('. It
// balances nested parentheses and honors backslash escapes, so destinations
// such as "images/foo_(dark).png" are not truncated. Optional title text is
// skipped, including a quoted title that itself contains parentheses.
// Unbalanced input reports ok=false so the caller can leave the text untouched.
func scanImageDestination(line string, open int) (int, bool) {
	depth := 0
	titleQuote := byte(0)
	for i := open + 1; i < len(line); i++ {
		c := line[i]
		if c == '\\' {
			if i+1 >= len(line) {
				return 0, false
			}
			i++ // Skip the escaped byte.
			continue
		}
		if titleQuote != 0 {
			if c == titleQuote {
				titleQuote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			// A quoted title follows the destination after whitespace.
			// Parentheses inside it must not affect balancing.
			if depth == 0 && i > open+1 && isSpaceOrTab(line[i-1]) {
				titleQuote = c
			}
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return i + 1, true
			}
			depth--
		}
	}
	return 0, false
}

func isSpaceOrTab(c byte) bool {
	return c == ' ' || c == '\t'
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
		if token.Data == "img" && tokenType == html.EndTagToken {
			// Drop the closing tag.
			continue
		}
		if token.Data == "img" && (tokenType == html.StartTagToken || tokenType == html.SelfClosingTagToken) {
			for _, attribute := range token.Attr {
				if attribute.Key == "alt" {
					builder.WriteString(attribute.Val)
					break
				}
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
