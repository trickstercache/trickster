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
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"go.yaml.in/yaml/v3"
)

const (
	envRefOpen     = "${"
	envRefClose    = '}'
	envRefDefault  = ":-"
	envRefRequired = ":?"
	// rewriterEnvPrefix is the only name prefix expanded inside request rewriter instructions,
	// where ${0}, ${1} and ${name} are request-time capture tokens
	rewriterEnvPrefix = "TRK_"

	configKeyRequestRewriters = "request_rewriters"
	configKeyInstructions     = "instructions"

	yamlStrTag = "!!str"
)

type envRefOp uint8

const (
	envRefPlain envRefOp = iota
	envRefWithDefault
	envRefRequiredOp
)

type envRef struct {
	name, arg string
	op        envRefOp
}

// envScope tracks where a node sits, so that rewriter instructions expand only TRK_ names
type envScope uint8

const (
	envScopeRoot envScope = iota
	envScopeRewriters
	envScopeRewriter
	envScopeInstructions
	envScopeOther
)

func (s envScope) child(key string) envScope {
	switch {
	case s == envScopeRoot && key == configKeyRequestRewriters:
		return envScopeRewriters
	case s == envScopeRewriters:
		return envScopeRewriter
	case s == envScopeRewriter && key == configKeyInstructions,
		s == envScopeInstructions:
		return envScopeInstructions
	}
	return envScopeOther
}

func (s envScope) prefix() string {
	if s == envScopeInstructions {
		return rewriterEnvPrefix
	}
	return ""
}

// envExpander expands ${NAME}, ${NAME:-default} and ${NAME:?message} references in the scalar
// values of a config source, before any typed decoding; $${ yields a literal ${.
type envExpander struct {
	lookup func(string) (string, bool)
	unset  sets.Set[string]
}

func newEnvExpander() *envExpander {
	return &envExpander{lookup: os.LookupEnv, unset: sets.NewStringSet()}
}

// warnings names each referenced variable that was unset and had no default, in sorted order.
func (e *envExpander) warnings() []string {
	if len(e.unset) == 0 {
		return nil
	}
	names := e.unset.Sorted(strings.Compare)
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = fmt.Sprintf("config references environment variable %s, which is not set", name)
	}
	return out
}

// expandDocument expands the references in a parsed config document in place. A mapping value
// that is a single reference to an unset variable is removed, so the field keeps its default.
func (e *envExpander) expandDocument(doc *yaml.Node) (bool, error) {
	if doc == nil {
		return false, nil
	}
	if doc.Kind == yaml.DocumentNode {
		changed := false
		for _, n := range doc.Content {
			c, err := e.walk(n, envScopeRoot)
			if err != nil {
				return changed, err
			}
			changed = changed || c
		}
		return changed, nil
	}
	return e.walk(doc, envScopeRoot)
}

func (e *envExpander) walk(n *yaml.Node, scope envScope) (bool, error) {
	changed := false
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); {
			value, childScope := n.Content[i+1], scope.child(n.Content[i].Value)
			if value.Kind != yaml.ScalarNode {
				c, err := e.walk(value, childScope)
				if err != nil {
					return changed, err
				}
				changed = changed || c
				i += 2
				continue
			}
			c, unset, err := e.scalar(value, childScope.prefix())
			if err != nil {
				return changed, err
			}
			if unset {
				n.Content = slices.Delete(n.Content, i, i+2)
				changed = true
				continue
			}
			changed = changed || c
			i += 2
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			var c bool
			var err error
			if item.Kind == yaml.ScalarNode {
				// a position in a sequence can't be left out, so an unset reference there is ""
				c, _, err = e.scalar(item, scope.prefix())
			} else {
				c, err = e.walk(item, scope)
			}
			if err != nil {
				return changed, err
			}
			changed = changed || c
		}
	case yaml.ScalarNode:
		c, _, err := e.scalar(n, scope.prefix())
		return c, err
	}
	return changed, nil
}

// scalar expands n in place and reports whether its value is a single unset reference.
func (e *envExpander) scalar(n *yaml.Node, prefix string) (changed, unset bool, err error) {
	if !strings.Contains(n.Value, envRefOpen) {
		return false, false, nil
	}
	out, unset, err := e.expand(n.Value, prefix)
	if err != nil {
		return false, false, fmt.Errorf("line %d: %w", n.Line, err)
	}
	if unset {
		out = ""
	}
	if out == n.Value {
		return false, unset, nil
	}
	n.Value = out
	// an untagged plain scalar resolves its type again (so ${PORT} can be an int), except when
	// the result reads as null, which would otherwise erase a string value
	if n.Style == 0 {
		if isYAMLNull(out) {
			n.Tag = yamlStrTag
		} else {
			n.Tag = ""
		}
	}
	return true, unset, nil
}

// expand returns s with its references replaced, and whether s is exactly one plain reference
// to an unset variable. With a prefix, only names starting with it are references.
func (e *envExpander) expand(s, prefix string) (string, bool, error) {
	if ref, n, ok := parseEnvRef(s); ok && n == len(s) && ref.op == envRefPlain &&
		strings.HasPrefix(ref.name, prefix) {
		if _, set := e.lookup(ref.name); !set {
			e.unset.Set(ref.name)
			return "", true, nil
		}
	}
	start := strings.Index(s, envRefOpen)
	if start < 0 {
		return s, false, nil
	}
	var out strings.Builder
	out.Grow(len(s))
	for start >= 0 {
		if start > 0 && s[start-1] == '$' {
			// the escape: drop its first $ and keep the ${ literally
			out.WriteString(s[:start-1])
			out.WriteString(envRefOpen)
			s = s[start+len(envRefOpen):]
			start = strings.Index(s, envRefOpen)
			continue
		}
		out.WriteString(s[:start])
		s = s[start:]
		ref, n, ok := parseEnvRef(s)
		if !ok || !strings.HasPrefix(ref.name, prefix) {
			out.WriteString(envRefOpen)
			s = s[len(envRefOpen):]
			start = strings.Index(s, envRefOpen)
			continue
		}
		value, err := e.resolve(ref)
		if err != nil {
			return "", false, err
		}
		out.WriteString(value)
		s = s[n:]
		start = strings.Index(s, envRefOpen)
	}
	out.WriteString(s)
	return out.String(), false, nil
}

func (e *envExpander) resolve(ref envRef) (string, error) {
	value, set := e.lookup(ref.name)
	switch ref.op {
	case envRefWithDefault:
		if value == "" {
			return ref.arg, nil
		}
	case envRefRequiredOp:
		if value == "" {
			if ref.arg != "" {
				return "", fmt.Errorf("environment variable %s is required: %s", ref.name, ref.arg)
			}
			return "", fmt.Errorf("environment variable %s is required", ref.name)
		}
	default:
		if !set {
			e.unset.Set(ref.name)
		}
	}
	return value, nil
}

// parseEnvRef parses the reference that s begins with, returning its length; a default or
// message is literal text and cannot hold ${ or }.
func parseEnvRef(s string) (envRef, int, bool) {
	if !strings.HasPrefix(s, envRefOpen) {
		return envRef{}, 0, false
	}
	end := strings.IndexByte(s, envRefClose)
	if end < 0 {
		return envRef{}, 0, false
	}
	inner := s[len(envRefOpen):end]
	if strings.Contains(inner, envRefOpen) {
		return envRef{}, 0, false
	}
	nameLen := envNameLen(inner)
	if nameLen == 0 {
		return envRef{}, 0, false
	}
	ref := envRef{name: inner[:nameLen]}
	switch rest := inner[nameLen:]; {
	case rest == "":
	case strings.HasPrefix(rest, envRefDefault):
		ref.op, ref.arg = envRefWithDefault, rest[len(envRefDefault):]
	case strings.HasPrefix(rest, envRefRequired):
		ref.op, ref.arg = envRefRequiredOp, rest[len(envRefRequired):]
	default:
		return envRef{}, 0, false
	}
	return ref, end + 1, true
}

// envNameLen returns the length of the POSIX shell variable name that s begins with.
func envNameLen(s string) int {
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return i
		}
	}
	return len(s)
}

func isYAMLNull(s string) bool {
	switch s {
	case "", "~", "null", "Null", "NULL":
		return true
	}
	return false
}
