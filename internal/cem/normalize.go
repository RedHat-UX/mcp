// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Package cem normalizes Custom Elements Manifest schema versions 1.0.0 and
// 2.1.0 into one internal model. The model is deliberately explicit: a missing
// field stays nil instead of becoming an invented default, and deprecation is
// reported only when the manifest supplies it.
package cem

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bennypowers.dev/cem/manifest"
)

// SupportedSchemaVersions are the CEM schema versions this module understands.
var SupportedSchemaVersions = []string{"1.0.0", "2.1.0"}

// ErrUnsupportedSchema reports a manifest that this normalizer does not
// understand.
var ErrUnsupportedSchema = errors.New("unsupported CEM schema version")

// Model is the normalized manifest.
type Model struct {
	SchemaVersion string      `json:"schemaVersion"`
	Components    []Component `json:"components"`
}

// Component is one normalized custom element.
type Component struct {
	TagName        string        `json:"tagName"`
	Name           string        `json:"name,omitempty"`
	Summary        string        `json:"summary,omitempty"`
	Description    string        `json:"description,omitempty"`
	ModulePath     string        `json:"modulePath,omitempty"`
	PackageSubpath string        `json:"packageSubpath,omitempty"`
	Deprecated     *Deprecation  `json:"deprecated,omitempty"`
	Attributes     []Attribute   `json:"attributes,omitempty"`
	Properties     []Property    `json:"properties,omitempty"`
	Events         []Event       `json:"events,omitempty"`
	Slots          []Slot        `json:"slots,omitempty"`
	CSSParts       []CSSPart     `json:"cssParts,omitempty"`
	CSSProperties  []CSSProperty `json:"cssProperties,omitempty"`
	Examples       []Example     `json:"examples,omitempty"`
}

// Deprecation is present only when the manifest marks an item deprecated.
type Deprecation struct {
	Deprecated bool   `json:"deprecated"`
	Message    string `json:"message,omitempty"`
}

// Attribute is a normalized HTML attribute.
type Attribute struct {
	Name          string       `json:"name"`
	Type          string       `json:"type,omitempty"`
	Default       string       `json:"default,omitempty"`
	Summary       string       `json:"summary,omitempty"`
	Description   string       `json:"description,omitempty"`
	FieldName     string       `json:"fieldName,omitempty"`
	InheritedFrom string       `json:"inheritedFrom,omitempty"`
	Deprecated    *Deprecation `json:"deprecated,omitempty"`
}

// Property is a normalized class field exposed by an element.
type Property struct {
	Name          string       `json:"name"`
	Type          string       `json:"type,omitempty"`
	Default       string       `json:"default,omitempty"`
	Summary       string       `json:"summary,omitempty"`
	Description   string       `json:"description,omitempty"`
	Attribute     string       `json:"attribute,omitempty"`
	Reflects      bool         `json:"reflects,omitempty"`
	Readonly      bool         `json:"readonly,omitempty"`
	InheritedFrom string       `json:"inheritedFrom,omitempty"`
	Deprecated    *Deprecation `json:"deprecated,omitempty"`
}

// Event is a normalized custom event.
type Event struct {
	Name          string       `json:"name"`
	Type          string       `json:"type,omitempty"`
	Summary       string       `json:"summary,omitempty"`
	Description   string       `json:"description,omitempty"`
	InheritedFrom string       `json:"inheritedFrom,omitempty"`
	Deprecated    *Deprecation `json:"deprecated,omitempty"`
}

// Slot is a normalized shadow DOM slot.
type Slot struct {
	Name          string       `json:"name"`
	Summary       string       `json:"summary,omitempty"`
	Description   string       `json:"description,omitempty"`
	InheritedFrom string       `json:"inheritedFrom,omitempty"`
	Deprecated    *Deprecation `json:"deprecated,omitempty"`
}

// CSSPart is a normalized ::part() name.
type CSSPart struct {
	Name          string       `json:"name"`
	Summary       string       `json:"summary,omitempty"`
	Description   string       `json:"description,omitempty"`
	InheritedFrom string       `json:"inheritedFrom,omitempty"`
	Deprecated    *Deprecation `json:"deprecated,omitempty"`
}

// CSSProperty is a normalized CSS custom property.
type CSSProperty struct {
	Name          string       `json:"name"`
	Default       string       `json:"default,omitempty"`
	Syntax        string       `json:"syntax,omitempty"`
	Summary       string       `json:"summary,omitempty"`
	Description   string       `json:"description,omitempty"`
	InheritedFrom string       `json:"inheritedFrom,omitempty"`
	Deprecated    *Deprecation `json:"deprecated,omitempty"`
}

// Example is a normalized CEM demo. Schema 1.0.0 manifests generally have no
// demos; an absent list stays nil.
type Example struct {
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	Source      string `json:"source,omitempty"`
}

// Parse unmarshals and normalizes a CEM document.
func Parse(payload []byte) (*Model, error) {
	var pkg manifest.Package
	if err := json.Unmarshal(payload, &pkg); err != nil {
		return nil, fmt.Errorf("parsing CEM: %w", err)
	}
	return Normalize(&pkg)
}

// Normalize converts a parsed CEM package into the internal model.
func Normalize(pkg *manifest.Package) (*Model, error) {
	if pkg == nil {
		return nil, errors.New("normalizing CEM: nil package")
	}
	if !supportedSchema(pkg.SchemaVersion) {
		return nil, fmt.Errorf("%w %q; supported versions are %s",
			ErrUnsupportedSchema, pkg.SchemaVersion, strings.Join(SupportedSchemaVersions, ", "))
	}

	model := &Model{SchemaVersion: pkg.SchemaVersion}
	for i := range pkg.Modules {
		module := &pkg.Modules[i]
		for _, declaration := range module.Declarations {
			// The CEM reader discriminates the declaration union for us. A
			// declaration that is not a CustomElementDeclaration is not an
			// element, even if it happens to carry a tagName.
			element, ok := declaration.(*manifest.CustomElementDeclaration)
			if !ok || element == nil || element.TagName == "" {
				continue
			}
			model.Components = append(model.Components,
				normalizeComponent(element, module.Path, deprecationOf(element.Deprecated)))
		}
	}
	sort.Slice(model.Components, func(i, j int) bool {
		return model.Components[i].TagName < model.Components[j].TagName
	})
	return model, nil
}

// Component returns the normalized component for tagName.
func (m *Model) Component(tagName string) (*Component, bool) {
	if m == nil {
		return nil, false
	}
	for i := range m.Components {
		if m.Components[i].TagName == tagName {
			return &m.Components[i], true
		}
	}
	return nil, false
}

func supportedSchema(version string) bool {
	for _, candidate := range SupportedSchemaVersions {
		if version == candidate {
			return true
		}
	}
	return false
}

func normalizeComponent(
	element *manifest.CustomElementDeclaration,
	modulePath string,
	deprecated *Deprecation,
) Component {
	component := Component{
		TagName:        element.TagName,
		Name:           element.Name(),
		Summary:        element.Summary,
		Description:    element.Description,
		ModulePath:     modulePath,
		PackageSubpath: trimModulePrefix(modulePath),
		Deprecated:     deprecated,
	}

	attributes := element.OwnAttributes()
	for i := range attributes {
		attribute := &attributes[i]
		component.Attributes = append(component.Attributes, Attribute{
			Name:          attribute.Name,
			Type:          typeText(attribute.Type),
			Default:       attribute.Default,
			Summary:       attribute.Summary,
			Description:   attribute.Description,
			FieldName:     attribute.FieldName,
			InheritedFrom: referenceName(attribute.InheritedFrom),
			Deprecated:    deprecationOf(attribute.Deprecated),
		})
	}

	for _, member := range element.Members {
		switch field := member.(type) {
		case *manifest.CustomElementField:
			if field == nil || isPrivate(field.Privacy) {
				continue
			}
			component.Properties = append(component.Properties, Property{
				Name:          field.Name,
				Type:          typeText(field.Type),
				Default:       field.Default,
				Summary:       field.Summary,
				Description:   field.Description,
				Attribute:     field.Attribute,
				Reflects:      field.Reflects,
				Readonly:      field.Readonly,
				InheritedFrom: referenceName(field.InheritedFrom),
				Deprecated:    deprecationOf(field.Deprecated),
			})
		case *manifest.ClassField:
			if field == nil || isPrivate(field.Privacy) {
				continue
			}
			component.Properties = append(component.Properties, Property{
				Name:          field.Name,
				Type:          typeText(field.Type),
				Default:       field.Default,
				Summary:       field.Summary,
				Description:   field.Description,
				Readonly:      field.Readonly,
				InheritedFrom: referenceName(field.InheritedFrom),
				Deprecated:    deprecationOf(field.Deprecated),
			})
		}
	}

	events := element.OwnEvents()
	for i := range events {
		event := &events[i]
		component.Events = append(component.Events, Event{
			Name:          event.Name,
			Type:          typeText(event.Type),
			Summary:       event.Summary,
			Description:   event.Description,
			InheritedFrom: referenceName(event.InheritedFrom),
			Deprecated:    deprecationOf(event.Deprecated),
		})
	}

	slots := element.OwnSlots()
	for i := range slots {
		slot := &slots[i]
		component.Slots = append(component.Slots, Slot{
			Name:          slot.Name,
			Summary:       slot.Summary,
			Description:   slot.Description,
			InheritedFrom: referenceName(slot.InheritedFrom),
			Deprecated:    deprecationOf(slot.Deprecated),
		})
	}

	parts := element.OwnCssParts()
	for i := range parts {
		part := &parts[i]
		component.CSSParts = append(component.CSSParts, CSSPart{
			Name:          part.Name,
			Summary:       part.Summary,
			Description:   part.Description,
			InheritedFrom: referenceName(part.InheritedFrom),
			Deprecated:    deprecationOf(part.Deprecated),
		})
	}

	properties := element.OwnCssProperties()
	for i := range properties {
		property := &properties[i]
		component.CSSProperties = append(component.CSSProperties, CSSProperty{
			Name:          property.Name,
			Default:       property.Default,
			Syntax:        property.Syntax,
			Summary:       property.Summary,
			Description:   property.Description,
			InheritedFrom: referenceName(property.InheritedFrom),
			Deprecated:    deprecationOf(property.Deprecated),
		})
	}

	// Demos first appear in CEM 2.x. An absent list stays nil.
	for i := range element.Demos {
		demo := &element.Demos[i]
		example := Example{
			Description: demo.Description,
			URL:         demo.URL,
		}
		if demo.Source != nil {
			example.Source = demo.Source.Href
		}
		component.Examples = append(component.Examples, example)
	}

	return component
}

func deprecationOf(deprecated manifest.Deprecated) *Deprecation {
	if deprecated == nil {
		return nil
	}
	info := &Deprecation{Deprecated: true}
	if reason, ok := deprecated.Value().(string); ok {
		info.Message = reason
	}
	return info
}

func typeText(typ *manifest.Type) string {
	if typ == nil {
		return ""
	}
	return typ.Text
}

func referenceName(reference *manifest.Reference) string {
	if reference == nil {
		return ""
	}
	return reference.Name
}

func isPrivate(privacy manifest.Privacy) bool {
	return privacy == manifest.Private
}

func trimModulePrefix(modulePath string) string {
	return strings.TrimPrefix(modulePath, "./")
}
