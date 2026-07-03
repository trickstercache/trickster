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

func TestValidate(t *testing.T) {
	t.Parallel()

	o := &Options{Name: ""}
	if ok, err := o.Validate(); ok || err != ErrInvalidName {
		t.Fatalf("Validate() = (%v, %v)", ok, err)
	}

	o = &Options{Name: "rewrite-host"}
	if ok, err := o.Validate(); !ok || err != nil {
		t.Fatalf("Validate() = (%v, %v)", ok, err)
	}
}

func TestLookupValidate(t *testing.T) {
	t.Parallel()

	l := Lookup{
		"rewrite-host": {Instructions: RewriteList{{"set", "Host", "example.com"}}},
		"skip-nil":     nil,
	}
	if err := l.Validate(); err != nil {
		t.Fatalf("Lookup.Validate: %v", err)
	}
	if l["rewrite-host"].Name != "rewrite-host" {
		t.Fatalf("Name = %q", l["rewrite-host"].Name)
	}

	l = Lookup{"": &Options{}}
	if err := l.Validate(); err != ErrInvalidName {
		t.Fatalf("Lookup.Validate() = %v", err)
	}
}

func TestCloneAndInitialize(t *testing.T) {
	t.Parallel()

	o := &Options{
		Instructions: RewriteList{
			{"set", "Host", "example.com"},
			{"set", "X-Test", "1"},
		},
	}
	cl := o.Clone()
	if len(cl.Instructions) != 2 || cl.Instructions[0][2] != "example.com" {
		t.Fatalf("clone instructions = %+v", cl.Instructions)
	}
	cl.Instructions[0][2] = "changed"
	if o.Instructions[0][2] != "example.com" {
		t.Fatal("clone should not share instruction slice")
	}

	if err := o.Initialize("ignored"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

func TestUnmarshalYAML(t *testing.T) {
	t.Parallel()

	const raw = `
request_rewriters:
  rewrite-host:
    instructions:
      - [set, Host, example.com]
`
	type doc struct {
		Rewriters Lookup `yaml:"request_rewriters"`
	}
	var d doc
	if err := yaml.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	o := d.Rewriters["rewrite-host"]
	if o == nil || len(o.Instructions) != 1 || o.Instructions[0][2] != "example.com" {
		t.Fatalf("unexpected rewriter: %+v", o)
	}
}
