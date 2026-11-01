// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package normalize

import (
	"errors"
	"fmt"
	"strings"
)

// ExtractThemeMap reads the `:root` declarations from a generated
// css/default-theme.css and splits light-dark() values into light and dark
// maps keyed by CSS custom property name. The runtime consumes the resulting
// JSON map and never parses CSS.
func ExtractThemeMap(css []byte) (map[string]map[string]string, error) {
	cleaned := stripCSSComments(string(css))
	themes := map[string]map[string]string{
		"light": {},
		"dark":  {},
	}
	declarations, err := rootDeclarations(cleaned)
	if err != nil {
		return nil, err
	}
	for _, declaration := range declarations {
		name, value, ok := splitDeclaration(declaration)
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !strings.HasPrefix(name, "--") {
			continue
		}
		if strings.HasPrefix(value, "light-dark(") {
			arms, err := splitFunctionArgs(value, "light-dark")
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if len(arms) != 2 {
				return nil, fmt.Errorf("%s: light-dark() has %d arms, want 2", name, len(arms))
			}
			themes["light"][name] = resolveThemeArgument(arms[0])
			themes["dark"][name] = resolveThemeArgument(arms[1])
			continue
		}
		themes["light"][name] = value
		themes["dark"][name] = value
	}
	return themes, nil
}

func stripCSSComments(input string) string {
	var builder strings.Builder
	builder.Grow(len(input))
	inString := byte(0)
	for i := 0; i < len(input); i++ {
		c := input[i]
		if inString != 0 {
			builder.WriteByte(c)
			if c == inString && (i == 0 || input[i-1] != '\\') {
				inString = 0
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			inString = c
			builder.WriteByte(c)
		case c == '/' && i+1 < len(input) && input[i+1] == '*':
			for i < len(input)-1 && (input[i] != '*' || input[i+1] != '/') {
				i++
			}
			i++
			builder.WriteByte(' ')
		default:
			builder.WriteByte(c)
		}
	}
	return builder.String()
}

func rootDeclarations(css string) ([]string, error) {
	var declarations []string
	for i := 0; i < len(css); {
		selectorStart := i
		for i < len(css) && css[i] != '{' {
			i++
		}
		if i >= len(css) {
			break
		}
		selector := strings.TrimSpace(css[selectorStart:i])
		body, next, err := readBlock(css, i)
		if err != nil {
			return nil, err
		}
		i = next
		if !isRootSelector(selector) {
			continue
		}
		declarations = append(declarations, splitDeclarations(body)...)
	}
	return declarations, nil
}

func isRootSelector(selector string) bool {
	for _, part := range strings.Split(selector, ",") {
		if strings.TrimSpace(part) == ":root" {
			return true
		}
	}
	return false
}

func readBlock(input string, open int) (body string, next int, err error) {
	depth := 0
	inString := byte(0)
	for i := open; i < len(input); i++ {
		c := input[i]
		if inString != 0 {
			if c == inString && input[i-1] != '\\' {
				inString = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inString = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return input[open+1 : i], i + 1, nil
			}
		}
	}
	return "", 0, errors.New("unterminated CSS block")
}

func splitDeclarations(body string) []string {
	var declarations []string
	depth := 0
	inString := byte(0)
	start := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inString != 0 {
			if c == inString && body[i-1] != '\\' {
				inString = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inString = c
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			if depth == 0 {
				declarations = append(declarations, body[start:i])
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(body[start:]); tail != "" {
		declarations = append(declarations, tail)
	}
	return declarations
}

func splitDeclaration(declaration string) (name, value string, ok bool) {
	depth := 0
	inString := byte(0)
	for i := 0; i < len(declaration); i++ {
		c := declaration[i]
		if inString != 0 {
			if c == inString && (i == 0 || declaration[i-1] != '\\') {
				inString = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inString = c
		case '(':
			depth++
		case ')':
			depth--
		case ':':
			if depth == 0 {
				return declaration[:i], declaration[i+1:], true
			}
		}
	}
	return "", "", false
}
