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

// A shorter fence inside a longer one is content, not a closer. Without the
// full marker, a three-backtick line would close a four-backtick block and the
// rest of the sample would be parsed as prose.
func TestCodeBlocksRequireMatchingFenceLength(t *testing.T) {
	input := strings.Join([]string{
		"````markdown",
		"```html",
		"## heading inside the sample",
		"```",
		"after the inner fence",
		"````",
		"## Real heading",
	}, "\n")
	blocks := CodeBlocks(input)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1: %+v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0].Content, "## heading inside the sample") ||
		!strings.Contains(blocks[0].Content, "after the inner fence") {
		t.Errorf("block content = %q, want the whole fenced sample", blocks[0].Content)
	}
	chunks := Chunks([]byte(input))
	if len(chunks) != 2 || chunks[1].Heading != "Real heading" {
		t.Fatalf("chunks = %+v, want the real heading only after the fence", chunks)
	}
	if strings.Contains(chunks[0].Body, "after the inner fence") == false {
		t.Errorf("fenced sample was split out of the intro chunk: %+v", chunks[0])
	}
}

func TestFenceRunLengthMustBeAtLeastThree(t *testing.T) {
	if _, ok := OpensFence("``"); ok {
		t.Error("two backticks opened a fence")
	}
	if _, ok := OpensFence("````"); !ok {
		t.Error("four backticks did not open a fence")
	}
	if ClosesFence("~~~", "````") {
		t.Error("tilde fence closed a backtick fence")
	}
}

func TestATXClosingSequenceStripsEveryHash(t *testing.T) {
	chunks := Chunks([]byte("## foo ###\n\nbody\n"))
	if len(chunks) != 1 || chunks[0].Heading != "foo" {
		t.Fatalf("chunks = %+v, want heading foo", chunks)
	}
	chunks = Chunks([]byte("## ###\n\nbody\n"))
	if len(chunks) != 1 || chunks[0].Heading != "" {
		t.Fatalf("chunks = %+v, want an empty heading", chunks)
	}
}
