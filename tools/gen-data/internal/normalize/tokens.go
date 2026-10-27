// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Package normalize converts the published RHDS data into the formats the
// server embeds. Normalization runs only in tools/gen-data; the runtime never
// sees Style Dictionary output.
package normalize

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"bennypowers.dev/asimonim/token"
)

// Result is the normalized token document and the theme data extracted from
// light-dark() composites.
type Result struct {
	// Document is canonical DTCG JSON.
	Document []byte
	// Themes maps theme name to CSS variable name to value. The light-dark()
	// composites are removed from Document and recorded here.
	Themes map[string]map[string]string
	// Stats describes what normalization did.
	Stats Stats
}

// Stats records normalization counts for diagnostics and tests.
type Stats struct {
	Tokens            int            `json:"tokens"`
	ThemeComposites   int            `json:"themeComposites"`
	PaletteComposites int            `json:"paletteComposites"`
	Types             map[string]int `json:"types"`
}

// NormalizeTokens converts Style Dictionary output into DTCG.
//
// The contract mirrors docs/PLAN.md section 5.4:
//   - original.$value wins so aliases survive
//   - $type is recovered from attributes.type when absent
//   - $description, $deprecated, $deprecationMessage, and $extensions survive
//   - Style Dictionary fields are dropped (mapped source types are recorded)
//   - -rgb and -hsl variants remain color tokens, canonicalized to hex
//   - light-dark() composites are moved to the theme map
func NormalizeTokens(input []byte) (*Result, error) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("parsing Style Dictionary JSON: %w", err)
	}
	if decoder.More() {
		return nil, errors.New("style dictionary JSON has trailing content")
	}

	ctx := &normalizer{
		themes: map[string]map[string]string{
			"light": {},
			"dark":  {},
		},
		values: make(map[string]any),
		stats:  Stats{Types: make(map[string]int)},
	}
	document, err := ctx.walkGroup(root, nil, nil)
	if err != nil {
		return nil, err
	}
	if err := ctx.resolvePendingThemes(); err != nil {
		return nil, err
	}

	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	return &Result{
		Document: encoded,
		Themes:   ctx.themes,
		Stats:    ctx.stats,
	}, nil
}

type normalizer struct {
	themes  map[string]map[string]string
	values  map[string]any
	pending []pendingComposite
	stats   Stats
}

// pendingComposite is a color token whose value is a light/dark pair. Later
// majors publish it as light-dark(...); 2.x publishes an array of aliases.
type pendingComposite struct {
	path    string
	cssName string
	arms    []string
}

func (n *normalizer) walkGroup(node map[string]any, logicalPath, outputPath []string) (map[string]any, error) {
	result := make(map[string]any, len(node))
	for key, value := range node {
		switch {
		case isMetadataKey(key):
			result[key] = copyValue(value)
		case isStyleDictionaryGroupKey(key):
			// Dropped: these carry no DTCG meaning at group level.
		default:
			child, ok := value.(map[string]any)
			if !ok {
				// Unknown scalar at group level. Drop it rather than emit a
				// value that is not a token or group.
				continue
			}
			entries, err := n.normalizeEntry(
				key,
				child,
				appendPath(logicalPath, key),
				outputPath,
			)
			if err != nil {
				return nil, err
			}
			if err := mergeEntries(result, entries); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// normalizeEntry normalizes a token or group. Style Dictionary uses a "_" key
// for a token that also has child tokens (the draft DTCG group-marker
// convention). asimonim's parser includes the marker in the generated name,
// which would produce --rh-color-icon-subtle-_ instead of the published
// --rh-color-icon-subtle. Flatten that convention instead: hoist the marker
// token to its group's key and prefix sibling keys with the group key. Names
// are unchanged, and the parser no longer sees a marker.
func (n *normalizer) normalizeEntry(
	outputKey string,
	node map[string]any,
	logicalPath, outputPath []string,
) (map[string]any, error) {
	fullOutputPath := appendPath(outputPath, outputKey)
	if isToken(node) {
		normalized, err := n.normalizeToken(outputKey, node, logicalPath)
		if err != nil || normalized == nil {
			return nil, err
		}
		return map[string]any{desiredOutputKey(outputKey, node, fullOutputPath): normalized}, nil
	}

	if marker, ok := node["_"].(map[string]any); ok && isToken(marker) {
		markerNode := copyMap(marker)
		mergeGroupMetadata(markerNode, node)
		normalizedMarker, err := n.normalizeToken(outputKey, markerNode, logicalPath)
		if err != nil {
			return nil, err
		}
		if normalizedMarker != nil {
			markerKey := desiredOutputKey(outputKey, markerNode, fullOutputPath)
			result := map[string]any{markerKey: normalizedMarker}
			for childKey, childValue := range node {
				if childKey == "_" || isMetadataKey(childKey) || isStyleDictionaryGroupKey(childKey) {
					continue
				}
				child, ok := childValue.(map[string]any)
				if !ok {
					continue
				}
				entries, err := n.normalizeEntry(
					markerKey+"-"+childKey,
					child,
					appendPath(logicalPath, childKey),
					outputPath,
				)
				if err != nil {
					return nil, err
				}
				if err := mergeEntries(result, entries); err != nil {
					return nil, err
				}
			}
			return result, nil
		}

		// The marker token was a composite that does not belong in DTCG.
		// Keep the group and its ordinary children.
		withoutMarker := copyMap(node)
		delete(withoutMarker, "_")
		normalized, err := n.walkGroup(withoutMarker, logicalPath, fullOutputPath)
		if err != nil || len(normalized) == 0 {
			return nil, err
		}
		return map[string]any{outputKey: normalized}, nil
	}

	normalized, err := n.walkGroup(node, logicalPath, fullOutputPath)
	if err != nil || len(normalized) == 0 {
		return nil, err
	}
	return map[string]any{outputKey: normalized}, nil
}

func appendPath(path []string, element string) []string {
	result := make([]string, len(path), len(path)+1)
	copy(result, path)
	return append(result, element)
}

func mergeEntries(target, entries map[string]any) error {
	for key, value := range entries {
		if _, exists := target[key]; exists {
			return fmt.Errorf("normalized tree has a duplicate key %q", key)
		}
		target[key] = value
	}
	return nil
}

// desiredOutputKey uses the published token name to correct keys such as
// "2xsMax" that Style Dictionary kept in camelCase. asimonim derives names
// from JSON keys, so a mismatch would produce --rh-breakpoint-2xsMax instead
// of the published --rh-breakpoint-2xs-max.
func desiredOutputKey(outputKey string, node map[string]any, logicalPath []string) string {
	name, ok := node["name"].(string)
	if !ok || name == "" || len(logicalPath) == 0 {
		return outputKey
	}
	desired := strings.TrimPrefix(name, "rh-")
	parent := strings.Join(logicalPath[:len(logicalPath)-1], "-")
	if parent == "" || !strings.HasPrefix(desired, parent+"-") {
		return outputKey
	}
	return strings.TrimPrefix(desired, parent+"-")
}

func mergeGroupMetadata(target, group map[string]any) {
	for _, key := range []string{"$type", "$description", "$deprecated", "$deprecationMessage"} {
		if _, ok := target[key]; ok {
			continue
		}
		if value, ok := group[key]; ok {
			target[key] = copyValue(value)
		}
	}
	groupExtensions, ok := group["$extensions"].(map[string]any)
	if !ok {
		return
	}
	targetExtensions, ok := target["$extensions"].(map[string]any)
	if !ok {
		targetExtensions = make(map[string]any)
	}
	for key, value := range groupExtensions {
		if _, exists := targetExtensions[key]; !exists {
			targetExtensions[key] = copyValue(value)
		}
	}
	target["$extensions"] = targetExtensions
}

func copyMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = copyValue(item)
	}
	return result
}

func (n *normalizer) normalizeToken(key string, node map[string]any, path []string) (map[string]any, error) {
	original, _ := node["original"].(map[string]any)
	attributes, _ := node["attributes"].(map[string]any)

	value := node["$value"]
	if originalValue, ok := original["$value"]; ok {
		value = originalValue
	}
	if value == nil {
		return nil, fmt.Errorf("token %s has no value", strings.Join(path, "."))
	}

	typeName, mappedFrom, err := resolveType(node, original, attributes)
	if err != nil {
		return nil, fmt.Errorf("token %s: %w", strings.Join(path, "."), err)
	}

	// A light/dark composite is a CSS expression, not a DTCG value. Later
	// majors publish light-dark(...); 2.x publishes a two-element array of
	// aliases and an empty resolved value. Record the arms and omit the token
	// from the DTCG document.
	if typeName == token.TypeColor {
		arms, kind, err := compositeArms(node, value)
		if err != nil {
			return nil, fmt.Errorf("token %s: %w", strings.Join(path, "."), err)
		}
		switch kind {
		case themeComposite:
			n.pending = append(n.pending, pendingComposite{
				path:    strings.Join(path, "."),
				cssName: cssNameFor(node, path),
				arms:    arms,
			})
			n.stats.ThemeComposites++
			return nil, nil
		case paletteComposite:
			// rh-surface palettes are prose, not a light/dark theme.
			n.stats.PaletteComposites++
			return nil, nil
		}
	}

	canonical, err := canonicalValue(typeName, value, attributes)
	if err != nil {
		return nil, fmt.Errorf("token %s: %w", strings.Join(path, "."), err)
	}
	n.values[strings.Join(path, ".")] = canonical

	result := map[string]any{
		"$value": canonical,
		"$type":  typeName,
	}
	if description, ok := stringField(node, original, "$description"); ok {
		result["$description"] = description
	}
	if deprecated, ok := field(node, original, "$deprecated"); ok {
		result["$deprecated"] = deprecated
	}
	if message, ok := stringField(node, original, "$deprecationMessage"); ok {
		result["$deprecationMessage"] = message
	}

	extensions := mergeExtensions(node, original)
	if mappedFrom != "" && mappedFrom != typeName {
		styleDictionary := map[string]any{"sourceType": mappedFrom}
		if existing, ok := extensions["com.style-dictionary"].(map[string]any); ok {
			for k, v := range existing {
				styleDictionary[k] = v
			}
		}
		extensions["com.style-dictionary"] = styleDictionary
	}
	if len(extensions) > 0 {
		result["$extensions"] = extensions
	}

	n.stats.Tokens++
	n.stats.Types[typeName]++
	return result, nil
}

func resolveType(node, original, attributes map[string]any) (typeName, mappedFrom string, err error) {
	for _, source := range []map[string]any{node, original} {
		if value, ok := source["$type"].(string); ok && value != "" {
			if value == "mediaQuery" {
				// mediaQuery is not a DTCG type. Its value is a string.
				return token.TypeString, value, nil
			}
			return value, "", nil
		}
	}

	attrType, _ := attributes["type"].(string)
	category, _ := attributes["category"].(string)

	switch {
	case attrType == "radius":
		return token.TypeDimension, attrType, nil
	case attrType == "letter-spacing":
		return token.TypeDimension, attrType, nil
	case attrType == "line-height":
		return token.TypeNumber, attrType, nil
	case attrType == "font-size":
		return token.TypeDimension, attrType, nil
	case attrType == "font-family":
		return token.TypeFontFamily, attrType, nil
	case attrType == "font-weight":
		return token.TypeFontWeight, attrType, nil
	case attrType == "duration" || attrType == "speed":
		return token.TypeDuration, attrType, nil
	case attrType == "timing":
		return token.TypeCubicBezier, attrType, nil
	case attrType == "shadow":
		return token.TypeShadow, attrType, nil
	case category == "opacity":
		return token.TypeNumber, "opacity", nil
	case category == "duration":
		return token.TypeDuration, category, nil
	}

	return "", "", fmt.Errorf(
		"cannot derive $type (attributes.type=%q, attributes.category=%q)", attrType, category)
}

type compositeKind int

const (
	notComposite compositeKind = iota
	themeComposite
	paletteComposite
)

// compositeArms classifies a color token with a composite value. Later majors
// publish light/dark as light-dark(...); 2.x publishes a two-element array of
// aliases. rh-surface palettes are longer alias arrays and are not themes.
func compositeArms(node map[string]any, originalValue any) ([]string, compositeKind, error) {
	// The authored value wins. A six-element array is an rh-surface palette,
	// even in releases where Style Dictionary also resolved it to light-dark().
	if arms, ok := originalValue.([]any); ok && len(arms) >= 2 {
		aliases := make([]string, 0, len(arms))
		for _, arm := range arms {
			text, ok := arm.(string)
			if !ok || !isAlias(text) {
				aliases = nil
				break
			}
			aliases = append(aliases, text)
		}
		if len(aliases) == 2 {
			return aliases, themeComposite, nil
		}
		if len(aliases) > 2 {
			return nil, paletteComposite, nil
		}
	}
	if resolved, ok := node["$value"].(string); ok && strings.HasPrefix(strings.TrimSpace(resolved), "light-dark(") {
		arms, err := splitFunctionArgs(resolved, "light-dark")
		if err != nil {
			return nil, notComposite, err
		}
		if len(arms) != 2 {
			return nil, notComposite, fmt.Errorf("light-dark() has %d arms, want 2", len(arms))
		}
		return []string{strings.TrimSpace(arms[0]), strings.TrimSpace(arms[1])}, themeComposite, nil
	}
	return nil, notComposite, nil
}

func cssNameFor(node map[string]any, path []string) string {
	if name, ok := node["name"].(string); ok && name != "" {
		return "--" + name
	}
	parts := make([]string, 0, len(path))
	for _, part := range path {
		switch part {
		case "_", "-", ".":
			continue
		default:
			parts = append(parts, part)
		}
	}
	return "--rh-" + strings.Join(parts, "-")
}

// resolvePendingThemes resolves composite arms after every ordinary token has
// a canonical value.
func (n *normalizer) resolvePendingThemes() error {
	for _, composite := range n.pending {
		light, err := n.resolveThemeValue(composite.arms[0], 0)
		if err != nil {
			return fmt.Errorf("token %s light arm: %w", composite.path, err)
		}
		dark, err := n.resolveThemeValue(composite.arms[1], 0)
		if err != nil {
			return fmt.Errorf("token %s dark arm: %w", composite.path, err)
		}
		n.themes["light"][composite.cssName] = light
		n.themes["dark"][composite.cssName] = dark
	}
	return nil
}

func (n *normalizer) resolveThemeValue(arm string, depth int) (string, error) {
	if depth > 32 {
		return "", errors.New("theme alias chain is too deep")
	}
	arm = strings.TrimSpace(arm)
	if isAlias(arm) {
		path := strings.TrimSuffix(strings.TrimPrefix(arm, "{"), "}")
		value, ok := n.values[path]
		if !ok {
			return "", fmt.Errorf("theme alias %s is not a token", arm)
		}
		if text, ok := value.(string); ok {
			return n.resolveThemeValue(text, depth+1)
		}
		return fmt.Sprintf("%v", value), nil
	}
	return resolveThemeArgument(arm), nil
}

func canonicalValue(typeName string, value any, attributes map[string]any) (any, error) {
	if text, ok := value.(string); ok {
		if isAlias(text) {
			return text, nil
		}
	}
	switch typeName {
	case token.TypeColor:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("color value is %T, want a string", value)
		}
		return CanonicalColor(text, attributes)
	case token.TypeNumber:
		return canonicalNumber(value)
	case token.TypeDimension:
		return canonicalDimension(value)
	case token.TypeDuration:
		return canonicalDuration(value)
	case token.TypeShadow:
		return canonicalShadow(value)
	case token.TypeCubicBezier:
		return canonicalCubicBezier(value)
	case token.TypeFontFamily, token.TypeFontWeight, token.TypeString, token.TypeStrokeStyle:
		return copyValue(value), nil
	default:
		// Unknown DTCG types are preserved as-is so a future token type does
		// not fail generation. The validator only checks schema consistency.
		return copyValue(value), nil
	}
}

// CanonicalColor returns a valid draft DTCG hex color. Colors that Style
// Dictionary emitted as hsl()/rgb()/space-separated channels are reconstructed
// from the token's attributes.hex. Non-opaque colors gain an alpha byte.
func CanonicalColor(value string, attributes map[string]any) (string, error) {
	text := strings.TrimSpace(value)
	if isAlias(text) {
		return text, nil
	}
	if strings.HasPrefix(text, "#") {
		normalized, err := normalizeHex(text)
		if err != nil {
			return "", err
		}
		if alpha, ok := attributeAlpha(attributes); ok && alpha < 0.999 && len(normalized) == 7 {
			normalized += alphaByte(alpha)
		}
		return normalized, nil
	}

	hex, _ := attributes["hex"].(string)
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if hex != "" {
		normalized, err := normalizeHex("#" + hex)
		if err != nil {
			return "", fmt.Errorf("color %q: %w", value, err)
		}
		if alpha, ok := attributeAlpha(attributes); ok && alpha < 0.999 && len(normalized) == 7 {
			normalized += alphaByte(alpha)
		}
		return normalized, nil
	}
	if normalized, ok := parseRGBA(text); ok {
		return normalized, nil
	}
	return "", fmt.Errorf("color %q is neither hex nor accompanied by attributes.hex", value)
}

// parseRGBA converts a CSS rgb()/rgba() literal to hex. Style Dictionary uses
// rgba() for shadow colors, which have no hex attribute.
func parseRGBA(value string) (string, bool) {
	text := strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(text, "rgba("):
		text = strings.TrimPrefix(text, "rgba(")
	case strings.HasPrefix(text, "rgb("):
		text = strings.TrimPrefix(text, "rgb(")
	default:
		return "", false
	}
	text = strings.TrimSuffix(text, ")")
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == '/' || r == ' ' || r == '\t'
	})
	if len(fields) != 3 && len(fields) != 4 {
		return "", false
	}
	channels := make([]int, 3)
	for i := 0; i < 3; i++ {
		parsed, ok := parseColorChannel(fields[i])
		if !ok {
			return "", false
		}
		channels[i] = parsed
	}
	alpha := 1.0
	if len(fields) == 4 {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(fields[3]), 64)
		if err != nil {
			return "", false
		}
		alpha = parsed
	}
	result := fmt.Sprintf("#%02x%02x%02x", channels[0], channels[1], channels[2])
	if alpha < 0.999 {
		result += alphaByte(alpha)
	}
	return result, true
}

func parseColorChannel(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "%") {
		parsed, err := strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
		if err != nil {
			return 0, false
		}
		return clampByte(int(parsed*255/100 + 0.5)), true
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return clampByte(int(parsed + 0.5)), true
}

func clampByte(value int) int {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return value
}

func attributeAlpha(attributes map[string]any) (float64, bool) {
	rgb, ok := attributes["rgb"].(map[string]any)
	if !ok {
		return 0, false
	}
	switch value := rgb["a"].(type) {
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	case float64:
		return value, true
	case int:
		return float64(value), true
	default:
		return 0, false
	}
}

func alphaByte(alpha float64) string {
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}
	return fmt.Sprintf("%02x", int(alpha*255+0.5))
}

func normalizeHex(value string) (string, error) {
	text := strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(text) != 3 && len(text) != 4 && len(text) != 6 && len(text) != 8 {
		return "", fmt.Errorf("%q is not a 3, 4, 6, or 8 digit hex color", value)
	}
	for _, r := range text {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "", fmt.Errorf("%q contains a non-hex character", value)
		}
	}
	text = strings.ToLower(text)
	if len(text) == 3 || len(text) == 4 {
		expanded := make([]byte, 0, len(text)*2)
		for i := 0; i < len(text); i++ {
			expanded = append(expanded, text[i], text[i])
		}
		text = string(expanded)
	}
	return "#" + text, nil
}

func canonicalNumber(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		return normalizeJSONNumber(string(v))
	case string:
		text := strings.TrimSpace(v)
		if strings.HasSuffix(text, "%") {
			parsed, err := strconv.ParseFloat(strings.TrimSuffix(text, "%"), 64)
			if err != nil {
				return nil, fmt.Errorf("number %q: %w", v, err)
			}
			return json.Number(formatFloat(parsed / 100)), nil
		}
		return normalizeJSONNumber(text)
	case float64:
		return json.Number(formatFloat(v)), nil
	case int:
		return json.Number(strconv.Itoa(v)), nil
	default:
		return nil, fmt.Errorf("number value is %T", value)
	}
}

func normalizeJSONNumber(text string) (any, error) {
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, fmt.Errorf("number %q: %w", text, err)
	}
	return json.Number(formatFloat(parsed)), nil
}

func canonicalDimension(value any) (any, error) {
	switch v := value.(type) {
	case string:
		text := strings.TrimSpace(v)
		if isAlias(text) {
			return text, nil
		}
		number, unit, ok := splitDimension(text)
		if !ok {
			return nil, fmt.Errorf("dimension %q has no unit", v)
		}
		return formatNumberWithUnit(number, unit), nil
	case json.Number:
		number := string(v)
		if isZero(number) {
			return "0px", nil
		}
		return nil, fmt.Errorf("dimension %s has no unit", number)
	case float64:
		if v == 0 {
			return "0px", nil
		}
		return nil, fmt.Errorf("dimension %v has no unit", v)
	case map[string]any:
		return structuredDimension(v)
	default:
		return nil, fmt.Errorf("dimension value is %T", value)
	}
}

func structuredDimension(value map[string]any) (string, error) {
	unit, ok := value["unit"].(string)
	if !ok || unit == "" {
		return "", errors.New("structured dimension has no unit")
	}
	number, ok := value["value"]
	if !ok {
		return "", errors.New("structured dimension has no value")
	}
	switch n := number.(type) {
	case json.Number:
		return formatNumberWithUnit(string(n), unit), nil
	case float64:
		return formatNumberWithUnit(formatFloat(n), unit), nil
	case int:
		return formatNumberWithUnit(strconv.Itoa(n), unit), nil
	default:
		return "", fmt.Errorf("structured dimension value is %T", number)
	}
}

func canonicalDuration(value any) (any, error) {
	switch v := value.(type) {
	case string:
		text := strings.TrimSpace(v)
		if isAlias(text) {
			return text, nil
		}
		number, unit, ok := splitDimension(text)
		if !ok || (unit != "ms" && unit != "s") {
			return nil, fmt.Errorf("duration %q must end in ms or s", v)
		}
		return formatNumberWithUnit(number, unit), nil
	case map[string]any:
		unit, _ := v["unit"].(string)
		if unit != "ms" && unit != "s" {
			return nil, fmt.Errorf("duration unit %q must be ms or s", unit)
		}
		number, ok := v["value"]
		if !ok {
			return nil, errors.New("structured duration has no value")
		}
		switch n := number.(type) {
		case json.Number:
			return formatNumberWithUnit(string(n), unit), nil
		case float64:
			return formatNumberWithUnit(formatFloat(n), unit), nil
		}
		return nil, fmt.Errorf("structured duration value is %T", number)
	default:
		return nil, fmt.Errorf("duration value is %T", value)
	}
}

func canonicalShadow(value any) (any, error) {
	switch v := value.(type) {
	case string:
		if isAlias(v) {
			return v, nil
		}
		return nil, fmt.Errorf("shadow %q is a string, want an object", v)
	case map[string]any:
		return canonicalShadowObject(v)
	case []any:
		shadows := make([]any, 0, len(v))
		for _, item := range v {
			shadow, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("shadow list item is %T", item)
			}
			normalized, err := canonicalShadowObject(shadow)
			if err != nil {
				return nil, err
			}
			shadows = append(shadows, normalized)
		}
		return shadows, nil
	default:
		return nil, fmt.Errorf("shadow value is %T", value)
	}
}

func canonicalShadowObject(value map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(value))
	for key, field := range value {
		switch key {
		case "offsetX", "offsetY", "blur", "spread":
			normalized, err := canonicalShadowDimension(field)
			if err != nil {
				return nil, fmt.Errorf("shadow %s: %w", key, err)
			}
			result[key] = normalized
		case "color":
			text, ok := field.(string)
			if !ok {
				return nil, fmt.Errorf("shadow color is %T", field)
			}
			normalized, err := CanonicalColor(text, nil)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		case "inset":
			result[key] = field
		default:
			result[key] = copyValue(field)
		}
	}
	if _, ok := result["color"]; !ok {
		return nil, errors.New("shadow has no color")
	}
	return result, nil
}

func canonicalShadowDimension(value any) (string, error) {
	switch v := value.(type) {
	case string:
		text := strings.TrimSpace(v)
		if isAlias(text) {
			return text, nil
		}
		if number, unit, ok := splitDimension(text); ok {
			return formatNumberWithUnit(number, unit), nil
		}
		if isZero(text) {
			return "0px", nil
		}
		return "", fmt.Errorf("shadow dimension %q has no unit", v)
	case json.Number:
		if isZero(string(v)) {
			return "0px", nil
		}
		return "", fmt.Errorf("shadow dimension %s has no unit", v)
	case float64:
		if v == 0 {
			return "0px", nil
		}
		return "", fmt.Errorf("shadow dimension %v has no unit", v)
	default:
		return "", fmt.Errorf("shadow dimension is %T", value)
	}
}

func canonicalCubicBezier(value any) (any, error) {
	switch v := value.(type) {
	case []any:
		if len(v) != 4 {
			return nil, fmt.Errorf("cubicBezier has %d values, want 4", len(v))
		}
		values := make([]any, 0, 4)
		for _, item := range v {
			number, err := canonicalNumber(item)
			if err != nil {
				return nil, err
			}
			values = append(values, number)
		}
		return values, nil
	case string:
		text := strings.TrimSpace(v)
		arms, err := splitFunctionArgs(text, "cubic-bezier")
		if err != nil {
			return nil, err
		}
		if len(arms) != 4 {
			return nil, fmt.Errorf("cubicBezier has %d values, want 4", len(arms))
		}
		values := make([]any, 0, 4)
		for _, arm := range arms {
			number, err := canonicalNumber(strings.TrimSpace(arm))
			if err != nil {
				return nil, err
			}
			values = append(values, number)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("cubicBezier value is %T", value)
	}
}

func splitDimension(text string) (string, string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", false
	}
	index := 0
	if text[0] == '-' || text[0] == '+' {
		index++
	}
	digits := 0
	for index < len(text) && (text[index] >= '0' && text[index] <= '9') {
		index++
		digits++
	}
	if index < len(text) && text[index] == '.' {
		index++
		for index < len(text) && (text[index] >= '0' && text[index] <= '9') {
			index++
			digits++
		}
	}
	if digits == 0 {
		return "", "", false
	}
	number := text[:index]
	unit := strings.TrimSpace(text[index:])
	if unit == "" {
		return "", "", false
	}
	if unit == "%" {
		return number, unit, true
	}
	for _, r := range unit {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return "", "", false
		}
	}
	return number, strings.ToLower(unit), true
}

func formatNumberWithUnit(number, unit string) string {
	parsed, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return number + unit
	}
	if parsed == 0 {
		return "0" + unit
	}
	return formatFloat(parsed) + unit
}

func formatFloat(value float64) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func isZero(text string) bool {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	return err == nil && parsed == 0
}

func isAlias(value string) bool {
	trimmed := strings.TrimSpace(value)
	return len(trimmed) >= 3 && strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")
}

func splitFunctionArgs(value, name string) ([]string, error) {
	trimmed := strings.TrimSpace(value)
	prefix := name + "("
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, ")") {
		return nil, fmt.Errorf("%q is not a %s() expression", value, name)
	}
	body := trimmed[len(prefix) : len(trimmed)-1]
	var args []string
	depth := 0
	start := 0
	inString := byte(0)
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inString != 0 {
			if c == inString {
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
		case ',':
			if depth == 0 {
				args = append(args, body[start:i])
				start = i + 1
			}
		}
	}
	args = append(args, body[start:])
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}
	return args, nil
}

func resolveThemeArgument(argument string) string {
	argument = strings.TrimSpace(argument)
	if !strings.HasPrefix(argument, "var(") {
		return argument
	}
	// var(--name, fallback) -> fallback. Find the first top-level comma.
	body := strings.TrimPrefix(argument, "var(")
	body = strings.TrimSuffix(body, ")")
	depth := 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(body[i+1:])
			}
		}
	}
	return argument
}

func isMetadataKey(key string) bool {
	switch key {
	case "$type", "$description", "$extensions", "$deprecated", "$deprecationMessage", "$schema":
		return true
	default:
		return false
	}
}

func isStyleDictionaryGroupKey(key string) bool {
	switch key {
	case "name", "filePath", "isSource", "original", "attributes", "path", "value", "comment":
		return true
	default:
		return false
	}
}

func isToken(node map[string]any) bool {
	if _, ok := node["$value"]; ok {
		return true
	}
	if original, ok := node["original"].(map[string]any); ok {
		_, hasValue := original["$value"]
		return hasValue
	}
	return false
}

func field(node, original map[string]any, key string) (any, bool) {
	if value, ok := node[key]; ok {
		return copyValue(value), true
	}
	if value, ok := original[key]; ok {
		return copyValue(value), true
	}
	return nil, false
}

func stringField(node, original map[string]any, key string) (string, bool) {
	value, ok := field(node, original, key)
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func mergeExtensions(node, original map[string]any) map[string]any {
	merged := make(map[string]any)
	for _, source := range []map[string]any{original, node} {
		extensions, ok := source["$extensions"].(map[string]any)
		if !ok {
			continue
		}
		for key, value := range extensions {
			merged[key] = copyValue(value)
		}
	}
	return merged
}

func copyValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			result[key] = copyValue(item)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = copyValue(item)
		}
		return result
	default:
		return v
	}
}

// SortedTypes returns the type counts in a stable order. It is a small helper
// for diagnostics and tests.
func SortedTypes(types map[string]int) []string {
	keys := make([]string, 0, len(types))
	for key := range types {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
