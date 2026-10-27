// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package markdown

import (
	"strings"
	"testing"
)

func TestChunksSplitOnHeadingsAndPreserveCode(t *testing.T) {
	input := strings.Join([]string{
		"Intro paragraph.",
		"",
		"## First",
		"",
		"Body one.",
		"",
		"```html",
		"## not a heading",
		"<rh-button>Save</rh-button>",
		"```",
		"",
		"### Nested",
		"",
		"Body two.",
		"",
	}, "\n")

	chunks := Chunks([]byte(input))
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3: %+v", len(chunks), chunks)
	}
	if chunks[0].Heading != "" || !strings.Contains(chunks[0].Body, "Intro paragraph.") {
		t.Errorf("intro chunk = %+v", chunks[0])
	}
	if chunks[1].Heading != "First" || chunks[1].Level != 2 {
		t.Errorf("first heading chunk = %+v", chunks[1])
	}
	if !strings.Contains(chunks[1].Body, "## not a heading") {
		t.Errorf("fenced code heading was treated as a heading: %q", chunks[1].Body)
	}
	if chunks[2].Heading != "Nested" || chunks[2].Level != 3 {
		t.Errorf("nested heading chunk = %+v", chunks[2])
	}
	if strings.Contains(chunks[2].Body, "First") {
		t.Errorf("chunk body leaked into the next heading: %q", chunks[2].Body)
	}
}

func TestChunksHandleCRLF(t *testing.T) {
	chunks := Chunks([]byte("## One\r\n\r\nText.\r\n"))
	if len(chunks) != 1 || chunks[0].Heading != "One" || chunks[0].Body != "Text." {
		t.Fatalf("chunks = %+v", chunks)
	}
}

func TestSectionsReturnsTopLevelSections(t *testing.T) {
	input := strings.Join([]string{
		"# Changelog",
		"",
		"## 2.0.0",
		"",
		"### Major Changes",
		"- Broke a thing.",
		"",
		"## 1.0.0",
		"",
		"### Minor Changes",
		"- Added a thing.",
		"",
	}, "\n")
	sections := Sections([]byte(input), 2)
	if len(sections) != 2 {
		t.Fatalf("sections = %d, want 2: %+v", len(sections), sections)
	}
	if sections[0].Heading != "2.0.0" || sections[1].Heading != "1.0.0" {
		t.Errorf("section headings = %q, %q", sections[0].Heading, sections[1].Heading)
	}
	if !strings.Contains(sections[0].Body, "Broke a thing.") {
		t.Errorf("section body = %q", sections[0].Body)
	}
	if strings.Contains(sections[0].Body, "Added a thing.") {
		t.Errorf("section body crossed a section boundary: %q", sections[0].Body)
	}
}

func TestCodeBlocksParseFences(t *testing.T) {
	input := strings.Join([]string{
		"Before:",
		"",
		"```html",
		"<rh-button>Old</rh-button>",
		"```",
		"",
		"After:",
		"",
		"~~~html",
		"<rh-button>New</rh-button>",
		"~~~",
	}, "\n")
	blocks := CodeBlocks(input)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2: %+v", len(blocks), blocks)
	}
	if blocks[0].Language != "html" || !strings.Contains(blocks[0].Content, "Old") {
		t.Errorf("first block = %+v", blocks[0])
	}
	if blocks[1].Language != "html" || !strings.Contains(blocks[1].Content, "New") {
		t.Errorf("second block = %+v", blocks[1])
	}
}
