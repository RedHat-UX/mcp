// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package normalize

import (
	"strings"
	"testing"
)

func TestProcessMarkdownStripsFrontmatterAndKeepsStructure(t *testing.T) {
	input := `---
title: Button
layout: element
---
# Button

Use the button.

| a | b |
| - | - |
| 1 | 2 |

` + "```html\n<img src=\"keep.png\" alt=\"code image\">\n![keep](code.png)\n```\n" + `
    indented code
    ![keep-indented](indented.png)

    <img src="indented-html.png" alt="drop me">

![Removed](image.png)

Inline <img src="x.png" alt="html alt"> text.
`
	result := ProcessMarkdown([]byte(input), "redhat-ux/red-hat-design-system@v5.0.0 elements/rh-button/docs/guidelines.md")
	content := string(result.Content)
	if strings.Contains(content, "title: Button") || strings.Contains(content, "layout: element") {
		t.Error("frontmatter was not stripped")
	}
	if result.Title != "Button" {
		t.Errorf("Title = %q, want Button", result.Title)
	}
	if !strings.Contains(content, "# Button") {
		t.Error("heading was removed")
	}
	if !strings.Contains(content, "| a | b |") {
		t.Error("table was removed")
	}
	if !strings.Contains(content, `<img src="keep.png" alt="code image">`) {
		t.Error("HTML inside a fence was modified")
	}
	if !strings.Contains(content, "![keep](code.png)") {
		t.Error("markdown image inside a fence was modified")
	}
	if !strings.Contains(content, "![keep-indented](indented.png)") {
		t.Error("markdown image inside indented code was modified")
	}
	if strings.Contains(content, "indented-html.png") {
		t.Error("image in indented HTML was not removed")
	}
	if strings.Contains(content, "![Removed](image.png)") {
		t.Error("inline markdown image was not removed")
	}
	if !strings.Contains(content, "Removed") {
		t.Error("inline markdown image alt text was not kept")
	}
	if strings.Contains(content, `<img src="x.png"`) {
		t.Error("inline HTML image was not removed")
	}
	if !strings.Contains(content, "html alt") {
		t.Error("inline HTML image alt text was not kept")
	}
	if !strings.Contains(content, "<!-- source: redhat-ux/red-hat-design-system@v5.0.0 elements/rh-button/docs/guidelines.md -->") {
		t.Error("source header is missing")
	}
	if !strings.HasSuffix(content, "\n") {
		t.Error("output does not end with a newline")
	}
}

func TestProcessMarkdownNormalizesCRLF(t *testing.T) {
	result := ProcessMarkdown([]byte("---\r\ntitle: X\r\n---\r\n# X\r\n\r\nbody\r\n"), "src")
	if strings.Contains(string(result.Content), "\r") {
		t.Error("CRLF was not normalized")
	}
	if result.Title != "X" {
		t.Errorf("Title = %q", result.Title)
	}
}

func TestProcessMarkdownFallsBackToHeadingTitle(t *testing.T) {
	result := ProcessMarkdown([]byte("# Getting started\n\ntext\n"), "src")
	if result.Title != "Getting started" {
		t.Errorf("Title = %q, want Getting started", result.Title)
	}
}

func TestProcessMarkdownHandlesTildeFences(t *testing.T) {
	input := "~~~md\n![keep](x.png)\n~~~\n\n![drop](y.png)\n"
	result := ProcessMarkdown([]byte(input), "src")
	content := string(result.Content)
	if !strings.Contains(content, "![keep](x.png)") {
		t.Error("tilde fence content was modified")
	}
	if strings.Contains(content, "![drop](y.png)") {
		t.Error("image outside the fence was not removed")
	}
}

func TestProcessMarkdownPreservesInlineCodeAndHTMLCodeBlocks(t *testing.T) {
	input := "# X\n\nUse `<img src=\"inline.png\" alt=\"code\">` in prose.\n\n" +
		"<rh-code-block>\n  <script type=\"text/html\"><img src=\"sample.svg\" alt=\"sample\"></script>\n</rh-code-block>\n"
	result := ProcessMarkdown([]byte(input), "src")
	content := string(result.Content)
	if !strings.Contains(content, "`<img src=\"inline.png\" alt=\"code\">`") {
		t.Error("inline code was modified")
	}
	if !strings.Contains(content, `<img src="sample.svg" alt="sample">`) {
		t.Error("HTML code block was modified")
	}
}

func TestProcessMarkdownDropsImageOnlyLines(t *testing.T) {
	result := ProcessMarkdown([]byte("# X\n\n![](logo.svg)\n\ntext\n"), "src")
	content := string(result.Content)
	if strings.Contains(content, "logo.svg") {
		t.Error("image reference survived")
	}
	if !strings.Contains(content, "text") {
		t.Error("surrounding text was removed")
	}
	if strings.Contains(content, "\n\n\n") {
		t.Error("image-only line left an extra blank line")
	}
}
