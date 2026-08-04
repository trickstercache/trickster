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

package options

import (
	"testing"

	"gopkg.in/yaml.v2"
)

func TestNew(t *testing.T) {
	t.Parallel()

	o := New()
	if o.InputDelimiter != " " {
		t.Fatalf("InputDelimiter = %q, want %q", o.InputDelimiter, " ")
	}
	if o.InputIndex != -1 {
		t.Fatalf("InputIndex = %d, want -1", o.InputIndex)
	}
	if o.MaxRuleExecutions != DefaultMaxRuleExecutions {
		t.Fatalf("MaxRuleExecutions = %d, want %d", o.MaxRuleExecutions, DefaultMaxRuleExecutions)
	}
	if o.InputType != "string" {
		t.Fatalf("InputType = %q, want string", o.InputType)
	}
}

func TestInitialize(t *testing.T) {
	t.Parallel()

	o := &Options{
		MaxRuleExecutions: 0,
		InputDelimiter:    "",
		InputType:         "STRING",
		InputEncoding:     "BASE64",
		Operation:         "EQ",
	}
	if err := o.Initialize(""); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if o.MaxRuleExecutions != DefaultMaxRuleExecutions {
		t.Fatalf("MaxRuleExecutions = %d, want default", o.MaxRuleExecutions)
	}
	if o.InputDelimiter != " " {
		t.Fatalf("InputDelimiter = %q, want space", o.InputDelimiter)
	}
	if o.InputType != "string" || o.InputEncoding != "base64" || o.Operation != "eq" {
		t.Fatalf("expected lowercase normalization, got type=%q encoding=%q op=%q",
			o.InputType, o.InputEncoding, o.Operation)
	}
}

func TestClone(t *testing.T) {
	t.Parallel()

	o := New()
	o.NextRoute = "next"
	o.CaseOptions = CaseOptionsList{
		{Matches: []string{"a", "b"}, NextRoute: "case-a"},
		nil,
	}
	cl := o.Clone()
	if cl == o {
		t.Fatal("Clone returned same pointer")
	}
	if cl.NextRoute != "next" {
		t.Fatalf("NextRoute = %q", cl.NextRoute)
	}
	if len(cl.CaseOptions) != 2 {
		t.Fatalf("CaseOptions len = %d", len(cl.CaseOptions))
	}
	if cl.CaseOptions[0] == o.CaseOptions[0] {
		t.Fatal("expected cloned case options")
	}
	cl.CaseOptions[0].Matches[0] = "changed"
	if o.CaseOptions[0].Matches[0] != "a" {
		t.Fatal("clone should not share slice backing")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "none"} {
		o := New()
		o.Name = name
		ok, err := o.Validate()
		if ok || err != ErrInvalidName {
			t.Fatalf("Validate(%q) = (%v, %v), want (false, ErrInvalidName)", name, ok, err)
		}
	}

	o := New()
	o.Name = "valid-rule"
	ok, err := o.Validate()
	if !ok || err != nil {
		t.Fatalf("Validate(valid-rule) = (%v, %v)", ok, err)
	}
}

func TestLookupInitializeAndValidate(t *testing.T) {
	t.Parallel()

	l := Lookup{
		"rule-a": {
			InputSource: "path",
			Operation:   "EQ",
		},
	}
	if err := l.Initialize(); err != nil {
		t.Fatalf("Lookup.Initialize: %v", err)
	}
	if l["rule-a"].Name != "rule-a" {
		t.Fatalf("Name = %q, want rule-a", l["rule-a"].Name)
	}
	if err := l.Validate(); err != nil {
		t.Fatalf("Lookup.Validate: %v", err)
	}

	l["bad"] = nil
	if err := l.Initialize(); err == nil {
		t.Fatal("expected empty rule error from Initialize")
	}
	if err := l.Validate(); err == nil {
		t.Fatal("expected empty rule error from Validate")
	}

	l = Lookup{"": New()}
	if err := l.Validate(); err != ErrInvalidName {
		t.Fatalf("Validate empty name = %v, want ErrInvalidName", err)
	}
}

func TestUnmarshalYAML(t *testing.T) {
	t.Parallel()

	const raw = `
rules:
  route-by-path:
    next_route: default
    input_source: path
    input_type: STRING
    input_encoding: BASE64
    operation: EQ
    cases:
      - matches: ["/api"]
        next_route: api-backend
`

	type rulesDoc struct {
		Rules Lookup `yaml:"rules"`
	}
	var doc rulesDoc
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	o := doc.Rules["route-by-path"]
	if o == nil {
		t.Fatal("expected route-by-path rule")
	}
	if err := o.Initialize(""); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if o.NextRoute != "default" || o.InputSource != "path" {
		t.Fatalf("unexpected rule options: %+v", o)
	}
	if o.InputType != "string" || o.InputEncoding != "base64" || o.Operation != "eq" {
		t.Fatalf("expected normalized values, got type=%q encoding=%q op=%q",
			o.InputType, o.InputEncoding, o.Operation)
	}
	if len(o.CaseOptions) != 1 || o.CaseOptions[0].NextRoute != "api-backend" {
		t.Fatalf("unexpected cases: %+v", o.CaseOptions)
	}
}
