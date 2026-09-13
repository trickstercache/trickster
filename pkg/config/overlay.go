/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"

	"go.yaml.in/yaml/v3"
)

// Overlay is an in-memory YAML configuration fragment that an in-process
// producer applies on top of all file-sourced configuration.
type Overlay struct {
	// Data is a YAML document whose root mapping may only contain named-object
	// sections (backends, caches, listeners, ...) whose names start with Prefix.
	Data []byte
	// Prefix is the reserved name prefix this overlay's producer owns.
	Prefix string
	// Version identifies the overlay content; a changed Version makes the
	// running configuration stale even when no file source changed.
	Version string
}

// OverlayProvider supplies the current overlay to every configuration (re)load.
// Overlay must be cheap and non-blocking; a nil result means no overlay.
type OverlayProvider interface {
	Overlay() *Overlay
}

var (
	// ErrOverlaySection indicates an overlay set a top-level key that is not
	// a named-object section.
	ErrOverlaySection = errors.New("overlay may only define named-object sections")
	// ErrOverlayPrefix indicates an overlay's Prefix is not a reserved name prefix.
	ErrOverlayPrefix = errors.New("overlay prefix is not a reserved name prefix")
	// ErrOverlayNamePrefix indicates an overlay object name lacks the overlay's prefix.
	ErrOverlayNamePrefix = errors.New("overlay object name lacks the overlay prefix")
	// ErrReservedNamePrefix indicates a file-sourced object name uses a reserved prefix.
	ErrReservedNamePrefix = errors.New("object name uses a reserved prefix")
)

// overlaySections lists the top-level named-object sections an overlay may define.
var overlaySections = map[string]struct{}{
	"authenticators":    {},
	"backends":          {},
	"caches":            {},
	"discovery":         {},
	"listeners":         {},
	"negative_caches":   {},
	"request_rewriters": {},
	"rules":             {},
	"tracing":           {},
}

// IsEmpty reports whether the overlay carries no configuration data.
func (o *Overlay) IsEmpty() bool {
	return o == nil || len(o.Data) == 0
}

// VersionString returns the overlay version, or an empty string for a nil overlay.
func (o *Overlay) VersionString() string {
	if o == nil {
		return ""
	}
	return o.Version
}

func validateOverlayDocument(root *yaml.Node, prefix string) error {
	if !reserved.IsNamePrefix(prefix) {
		return fmt.Errorf("%w: %q", ErrOverlayPrefix, prefix)
	}
	for index := 0; index < len(root.Content); index += 2 {
		section := root.Content[index]
		if _, ok := overlaySections[section.Value]; !ok {
			return fmt.Errorf("%w: %q at line %d", ErrOverlaySection, section.Value, section.Line)
		}
		mapping := root.Content[index+1]
		if mapping.Kind != yaml.MappingNode {
			return fmt.Errorf("overlay section %q at line %d must be a mapping", section.Value, section.Line)
		}
		for nameIndex := 0; nameIndex < len(mapping.Content); nameIndex += 2 {
			name := mapping.Content[nameIndex]
			if !strings.HasPrefix(name.Value, prefix) {
				return fmt.Errorf("%w %q: %s %q at line %d",
					ErrOverlayNamePrefix, prefix, section.Value, name.Value, name.Line)
			}
		}
	}
	return nil
}

func validateReservedNames(root *yaml.Node) error {
	for index := 0; index < len(root.Content); index += 2 {
		section := root.Content[index]
		mapping := root.Content[index+1]
		if _, ok := overlaySections[section.Value]; !ok || mapping.Kind != yaml.MappingNode {
			continue
		}
		for nameIndex := 0; nameIndex < len(mapping.Content); nameIndex += 2 {
			name := mapping.Content[nameIndex]
			if p := reserved.MatchNamePrefix(name.Value); p != "" {
				return fmt.Errorf("%w %q: %s %q at line %d",
					ErrReservedNamePrefix, p, section.Value, name.Value, name.Line)
			}
		}
	}
	return nil
}
