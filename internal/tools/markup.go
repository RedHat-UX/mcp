// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"bennypowers.dev/cem/lsp/document"
	cemtypes "bennypowers.dev/cem/lsp/types"
	"bennypowers.dev/cem/manifest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.lsp.dev/protocol"
	"golang.org/x/net/html"
)

// ValidateMarkupInput is the input for rhds_validate_markup.
type ValidateMarkupInput struct {
	Markup  string `json:"markup" jsonschema:"HTML or Twig markup to validate"`
	Format  string `json:"format,omitempty" jsonschema:"Markup format: html or twig; defaults to html"`
	Version string `json:"version,omitempty" jsonschema:"RHDS elements version; defaults to the latest bundled version"`
}

// MarkupIssue is one finding from rhds_validate_markup.
type MarkupIssue struct {
	Rule       string `json:"rule"`
	Severity   string `json:"severity"`
	Confidence string `json:"confidence"`
	Message    string `json:"message"`
	Element    string `json:"element,omitempty"`
	Attribute  string `json:"attribute,omitempty"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	// Truncated reports that Message was shortened by a fixed cap. A warning
	// names the field and its true original length.
	Truncated bool `json:"truncated,omitempty"`
}

// ValidateMarkupOutput is the output for rhds_validate_markup.
type ValidateMarkupOutput struct {
	Resolved  ResolvedVersion `json:"resolved"`
	Issues    []MarkupIssue   `json:"issues"`
	Warnings  []string        `json:"warnings,omitempty"`
	Truncated bool            `json:"truncated"`
	Error     *ToolError      `json:"error,omitempty"`
}

// globalAttributes may appear on any HTML element and are not declared in CEM.
// The list follows the HTML Standard global attributes; event handler content
// attributes (on*) are matched separately.
var globalAttributes = []string{
	"accesskey", "anchor", "autocapitalize", "autocorrect", "autofocus", "class",
	"contenteditable", "dir", "draggable", "enterkeyhint", "exportparts", "hidden",
	"id", "inert", "inputmode", "is", "itemid", "itemprop", "itemref",
	"itemscope", "itemtype", "lang", "nonce", "part", "popover", "role", "slot",
	"spellcheck", "style", "tabindex", "title", "translate",
	"virtualkeyboardpolicy", "writingsuggestions",
}

func isGlobalAttribute(name string) bool {
	lower := strings.ToLower(name)
	if slices.Contains(globalAttributes, lower) {
		return true
	}
	if strings.HasPrefix(lower, "aria-") || strings.HasPrefix(lower, "data-") {
		return true
	}
	// Event handler content attributes are global and are not declared in CEM.
	return strings.HasPrefix(lower, "on") && len(lower) > len("on")
}

// twigBlockOpeners are Jinja/Twig block tags that require a matching end tag.
var twigBlockOpeners = []string{
	"apply", "autoescape", "cache", "call", "embed", "filter", "for", "if",
	"macro", "raw", "sandbox", "spaceless", "trans", "verbatim", "with",
}

// twigVerbatimOpeners hold their body as literal text, so tags inside them must
// not affect the balance count.
var twigVerbatimOpeners = []string{"raw", "verbatim"}

func twigControlFlowUnbalanced(markup string) bool {
	markup = stripTwigComments(markup)
	depth := 0
	verbatim := ""
	remaining := markup
	for {
		start := strings.Index(remaining, "{%")
		if start < 0 {
			break
		}
		rest := remaining[start+2:]
		end := strings.Index(rest, "%}")
		if end < 0 {
			return true
		}
		tag := strings.TrimSpace(rest[:end])
		tag = strings.TrimPrefix(tag, "-")
		tag = strings.TrimSuffix(tag, "-")
		fields := strings.Fields(tag)
		if len(fields) > 0 {
			word := fields[0]
			switch {
			case verbatim != "":
				if word == "end"+verbatim {
					depth--
					verbatim = ""
				}
			case slices.Contains(twigVerbatimOpeners, word):
				depth++
				verbatim = word
			case word == "block":
				// {% block name %} opens a block; {% block name expr %} is the
				// inline shorthand and has no endblock. scoped blocks open.
				if len(fields) == 2 || (len(fields) == 3 && fields[2] == "scoped") {
					depth++
				}
			case word == "set":
				// {% set name = value %} assigns; {% set name %} opens a block.
				if !strings.Contains(tag, "=") {
					depth++
				}
			case slices.Contains(twigBlockOpeners, word):
				depth++
			case strings.HasPrefix(word, "end"):
				depth--
				if depth < 0 {
					return true
				}
			}
		}
		remaining = rest[end+2:]
	}
	return depth != 0
}

// stripTwigComments removes {# ... #} comments so tags inside them do not
// affect the balance count.
func stripTwigComments(markup string) string {
	var out strings.Builder
	out.Grow(len(markup))
	for {
		start := strings.Index(markup, "{#")
		if start < 0 {
			out.WriteString(markup)
			return out.String()
		}
		out.WriteString(markup[:start])
		rest := markup[start+2:]
		end := strings.Index(rest, "#}")
		if end < 0 {
			// An unclosed comment consumes the rest of the input.
			return out.String()
		}
		markup = rest[end+2:]
	}
}

// normalizeMarkupCase lowercases HTML tag and attribute names without
// changing attribute values, text, or byte offsets. The tree-sitter queries in
// cem's DocumentManager match only lowercase tag names, while HTML itself
// treats names case-insensitively.
func normalizeMarkupCase(markup string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(markup))
	var normalized strings.Builder
	normalized.Grow(len(markup))
	var closings []string
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			if tokenizer.Err() == io.EOF {
				break
			}
			// On tokenizer failure, leave the input untouched rather than
			// returning a partial document with shifted offsets.
			return markup
		}
		raw := tokenizer.Raw()
		switch tokenType {
		case html.StartTagToken, html.EndTagToken:
			normalized.Write(lowercaseRawTagNames(raw))
		case html.SelfClosingTagToken:
			lowered := lowercaseRawTagNames(raw)
			if name, ok := selfClosingCustomElementName(lowered); ok {
				closings = append(closings, name)
			}
			normalized.Write(normalizeSelfClosingCustomElement(lowered))
		default:
			normalized.Write(raw)
		}
	}
	// Closing tags are appended after the original input, so every byte offset
	// in the input still points at the same source position. They let the HTML
	// grammar build element nodes for the converted start tags; without them,
	// adjacent self-closing custom elements are not reported at all.
	for i := len(closings) - 1; i >= 0; i-- {
		normalized.WriteString("</")
		normalized.WriteString(closings[i])
		normalized.WriteString(">")
	}
	return normalized.String()
}

// selfClosingCustomElementName returns the lowercased tag name when raw is a
// self-closing tag for a custom element.
func selfClosingCustomElementName(raw []byte) (string, bool) {
	i := 0
	if i < len(raw) && raw[i] == '<' {
		i++
	}
	start := i
	for i < len(raw) && !isHTMLSpace(raw[i]) && raw[i] != '>' && raw[i] != '/' {
		i++
	}
	if i == start || !strings.Contains(string(raw[start:i]), "-") {
		return "", false
	}
	return string(raw[start:i]), true
}

// lowercaseRawTagNames lowercases the tag name and attribute names inside one
// raw start, end, or self-closing tag token. Quoted and unquoted values are
// copied verbatim.
func lowercaseRawTagNames(raw []byte) []byte {
	out := make([]byte, len(raw))
	copy(out, raw)

	i := 0
	if i < len(out) && out[i] == '<' {
		i++
	}
	if i < len(out) && out[i] == '/' {
		i++
	}
	for i < len(out) && !isHTMLSpace(out[i]) && out[i] != '>' && out[i] != '/' {
		out[i] = asciiLower(out[i])
		i++
	}

	for i < len(out) {
		for i < len(out) && (isHTMLSpace(out[i]) || out[i] == '/') {
			i++
		}
		if i >= len(out) || out[i] == '>' {
			break
		}

		start := i
		for i < len(out) && !isHTMLSpace(out[i]) && out[i] != '=' && out[i] != '>' && out[i] != '/' {
			out[i] = asciiLower(out[i])
			i++
		}
		if i == start {
			// Defensive: never spin on an unexpected byte.
			i++
			continue
		}

		for i < len(out) && isHTMLSpace(out[i]) {
			i++
		}
		if i >= len(out) || out[i] != '=' {
			continue
		}
		i++
		for i < len(out) && isHTMLSpace(out[i]) {
			i++
		}
		if i < len(out) && (out[i] == '"' || out[i] == '\'') {
			quote := out[i]
			i++
			for i < len(out) && out[i] != quote {
				i++
			}
			if i < len(out) {
				i++
			}
			continue
		}
		for i < len(out) && !isHTMLSpace(out[i]) && out[i] != '>' {
			i++
		}
	}
	return out
}

// normalizeSelfClosingCustomElement rewrites a self-closing custom-element tag
// into an equivalent start tag without changing its length. cem's tree-sitter
// query returns no matches for self-closing custom elements, so without this
// <rh-icon /> would be skipped and never validated. The slash is replaced with
// a space, preserving every byte offset and therefore every reported position.
func normalizeSelfClosingCustomElement(raw []byte) []byte {
	i := 0
	if i < len(raw) && raw[i] == '<' {
		i++
	}
	start := i
	for i < len(raw) && !isHTMLSpace(raw[i]) && raw[i] != '>' && raw[i] != '/' {
		i++
	}
	custom := false
	for _, b := range raw[start:i] {
		if b == '-' {
			custom = true
			break
		}
	}
	if !custom {
		return raw
	}
	for j := len(raw) - 1; j > 0; j-- {
		if raw[j] == '/' && j+1 < len(raw) && raw[j+1] == '>' {
			raw[j] = ' '
			break
		}
	}
	return raw
}

func isHTMLSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func asciiLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func (s *Service) documentManager() (cemtypes.Manager, error) {
	s.documentsOnce.Do(func() {
		s.documents, s.documentsErr = document.NewDocumentManager()
	})
	return s.documents, s.documentsErr
}

// ValidateMarkup implements rhds_validate_markup using the interim
// bennypowers.dev/cem/lsp/document entry point.
//
// GATE (cem#458): this stays on lsp/document until bennypowers/cem#458 is
// merged and released in a cem tag. Do not add a replace directive, fork, or
// vendored copy to move it earlier. When the gate opens, replace the
// DocumentManager path below with the exported cem/validate/markup API and
// move the HTML and Twig fixtures with it.
//
// Slot-name and content-model rules are intentionally deferred: CEM does not
// encode required slots or cardinality, and the #458 API will expose the
// structure-aware findings this spike cannot make through DocumentManager.
func (s *Service) ValidateMarkup(_ context.Context, input ValidateMarkupInput) (out ValidateMarkupOutput) {
	defer func() { out = fitValidateMarkup(out, s.responseBudget(false)) }()
	out = ValidateMarkupOutput{Issues: []MarkupIssue{}}
	if failure := s.dataFailure(); failure != nil {
		out.Error = failure
		return out
	}

	format := input.Format
	if format == "" {
		format = "html"
	}
	var extension string
	switch format {
	case "html":
		extension = "html"
	case "twig":
		extension = "twig"
	default:
		out.Error = invalidArgument("format must be html or twig")
		return out
	}

	version, failure := s.resolveVersion(packageElements, kindManifest, input.Version)
	if failure != nil {
		out.Error = failure
		return out
	}
	out.Resolved = ResolvedVersion{Elements: version, Source: "bundled"}

	pkg, failure := s.loadManifest(version)
	if failure != nil {
		out.Error = failure
		return out
	}

	manager, err := s.documentManager()
	if err != nil {
		out.Error = &ToolError{
			Code:    "markup_parser_unavailable",
			Message: "could not initialize the HTML parser",
			Hint:    err.Error(),
		}
		return out
	}

	uri := fmt.Sprintf("file:///rhds-mcp/inline-%d.%s", s.docSeq.Add(1), extension)
	markup := normalizeMarkupCase(input.Markup)
	doc := manager.OpenDocument(uri, markup, 1)
	defer manager.CloseDocument(uri)
	if doc == nil {
		out.Error = &ToolError{
			Code:    "markup_parse_failed",
			Message: "the parser could not create a document for this markup",
		}
		return out
	}

	matches, err := doc.FindCustomElements(manager)
	if err != nil {
		out.Error = &ToolError{
			Code:    "markup_parse_failed",
			Message: "the parser could not read this markup",
			Hint:    err.Error(),
		}
		return out
	}

	unbalanced := format == "twig" && twigControlFlowUnbalanced(input.Markup)
	if unbalanced {
		out.Warnings = append(out.Warnings,
			"twig control flow is unbalanced; structural findings are low confidence")
	}

	elements := declarationsByTag(pkg)
	for _, match := range matches {
		tagName := strings.ToLower(match.TagName)
		element, known := elements[tagName]
		if !known {
			if strings.HasPrefix(tagName, "rh-") {
				out.Issues = append(out.Issues, MarkupIssue{
					Rule:       "unknown-tag",
					Severity:   "error",
					Confidence: "high",
					Message:    fmt.Sprintf("unknown RHDS custom element %q", tagName),
					Element:    tagName,
					Line:       lineOf(match.Range),
					Column:     columnOf(match.Range),
				})
			}
			continue
		}

		if element.IsDeprecated() {
			out.Issues = append(out.Issues, MarkupIssue{
				Rule:       "deprecated-element",
				Severity:   "warning",
				Confidence: "high",
				Message:    deprecationMessage(fmt.Sprintf("%s is deprecated", tagName), element.Deprecated),
				Element:    tagName,
				Line:       lineOf(match.Range),
				Column:     columnOf(match.Range),
			})
		}

		attributes := make(map[string]*manifest.Attribute, len(element.CustomElement.Attributes))
		for i := range element.CustomElement.Attributes {
			name := strings.ToLower(element.CustomElement.Attributes[i].Name)
			attributes[name] = &element.CustomElement.Attributes[i]
		}

		for _, rawName := range sortedKeys(match.Attributes) {
			name := strings.ToLower(rawName)
			attributeMatch := match.Attributes[rawName]
			if isGlobalAttribute(name) {
				continue
			}
			declaration, ok := attributes[name]
			if !ok {
				out.Issues = append(out.Issues, MarkupIssue{
					Rule:       "unknown-attribute",
					Severity:   "warning",
					Confidence: "high",
					Message:    fmt.Sprintf("%q is not an attribute of %s", name, tagName),
					Element:    tagName,
					Attribute:  name,
					Line:       lineOf(attributeMatch.Range),
					Column:     columnOf(attributeMatch.Range),
				})
				continue
			}
			if declaration.IsDeprecated() {
				out.Issues = append(out.Issues, MarkupIssue{
					Rule:       "deprecated-attribute",
					Severity:   "warning",
					Confidence: "high",
					Message: deprecationMessage(
						fmt.Sprintf("attribute %q on %s is deprecated", name, tagName),
						declaration.Deprecated),
					Element:   tagName,
					Attribute: name,
					Line:      lineOf(attributeMatch.Range),
					Column:    columnOf(attributeMatch.Range),
				})
			}
			if !isValidAttributeValue(declaration, attributeMatch.Value) {
				out.Issues = append(out.Issues, MarkupIssue{
					Rule:       "invalid-attribute-value",
					Severity:   "error",
					Confidence: "high",
					Message: fmt.Sprintf(
						"%q is not a valid value for %s attribute %q; valid values: %v",
						attributeMatch.Value, tagName, name, enumValueLabels(declaration)),
					Element:   tagName,
					Attribute: name,
					Line:      lineOf(attributeMatch.Range),
					Column:    columnOf(attributeMatch.Range),
				})
			}
		}
	}

	if unbalanced {
		for i := range out.Issues {
			out.Issues[i].Confidence = "low"
			if out.Issues[i].Severity == "error" {
				out.Issues[i].Severity = "warning"
			}
		}
	}

	sort.Slice(out.Issues, func(i, j int) bool {
		a, b := out.Issues[i], out.Issues[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Message < b.Message
	})
	return out
}

// isValidAttributeValue reports whether value is valid for attribute. It
// replaces manifest.Attribute.IsValidValue, which strips only double quotes:
// published RHDS manifests declare union literals with single quotes, so its
// check rejects every valid enum value.
func isValidAttributeValue(attribute *manifest.Attribute, value string) bool {
	parts := attribute.EnumValues()
	if parts == nil {
		return true
	}
	for _, part := range parts {
		if value == unquoteTypeLiteral(part) {
			return true
		}
	}
	return false
}

// enumValueLabels returns the union members without their literal quotes, for
// display in diagnostics.
func enumValueLabels(attribute *manifest.Attribute) []string {
	parts := attribute.EnumValues()
	labels := make([]string, len(parts))
	for i, part := range parts {
		labels[i] = unquoteTypeLiteral(part)
	}
	return labels
}

// unquoteTypeLiteral removes one matching pair of single or double quotes from
// a TypeScript string literal.
func unquoteTypeLiteral(part string) string {
	if len(part) >= 2 {
		first, last := part[0], part[len(part)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return part[1 : len(part)-1]
		}
	}
	return part
}

// validateMarkupWithEnv implements rhds_validate_markup for an MCP call. It
// resolves the workspace elements version instead of always validating
// against the latest bundled version.
func (s *Service) validateMarkupWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input ValidateMarkupInput,
) (*mcp.CallToolResult, ValidateMarkupOutput) {
	resolved, required, failure := s.prepareResolution(
		ctx, newCallEnv(request), input.Version, "", true)
	if required != nil {
		return required, ValidateMarkupOutput{}
	}
	if failure != nil {
		out := ValidateMarkupOutput{Issues: []MarkupIssue{}, Error: failure}
		return nil, fitValidateMarkup(out, defaultResponseBudget)
	}
	if resolved.Resolved.Elements == "" {
		return nil, s.ValidateMarkup(ctx, input)
	}
	input.Version = resolved.Resolved.Elements
	out := s.ValidateMarkup(ctx, input)
	mergeResolution(&out.Resolved, resolved.Resolved)
	return nil, out
}

func declarationsByTag(pkg *manifest.Package) map[string]*manifest.CustomElementDeclaration {
	elements := make(map[string]*manifest.CustomElementDeclaration)
	for i := range pkg.Modules {
		for _, declaration := range pkg.Modules[i].Declarations {
			element, ok := declaration.(*manifest.CustomElementDeclaration)
			if ok && element.TagName != "" {
				elements[strings.ToLower(element.TagName)] = element
			}
		}
	}
	return elements
}

func deprecationMessage(prefix string, deprecated manifest.Deprecated) string {
	if deprecated == nil {
		return prefix
	}
	if reason, ok := deprecated.Value().(string); ok && reason != "" {
		return prefix + ": " + reason
	}
	return prefix
}

func lineOf(r protocol.Range) int {
	return int(r.Start.Line) + 1
}

func columnOf(r protocol.Range) int {
	return int(r.Start.Character) + 1
}
