/*
 * Copyright 2026 The Trickster Authors
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
	"errors"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestOptions(t *testing.T) {
	o := New()
	if o.GraphitePath != "" || o.SearchDisableCache {
		t.Error("unexpected defaults")
	}
	var nilOptions *Options
	if nilOptions.Validate() != nil || nilOptions.Clone() != nil {
		t.Error("nil options")
	}
	if err := yaml.Unmarshal([]byte("graphite_path: /select/1/graphite\nsearch_disable_cache: true\n"), o); err != nil {
		t.Fatal(err)
	}
	c := o.Clone()
	c.GraphitePath = "/changed"
	if o.GraphitePath != "/select/1/graphite" || !o.SearchDisableCache || !c.SearchDisableCache {
		t.Errorf("clone shares state or lost fields: %+v %+v", o, c)
	}
	for path, want := range map[string]error{
		"":                   nil,
		"/select/1/graphite": nil,
		"select/1/graphite":  ErrInvalidGraphitePath,
		"/graphite?x=1":      ErrInvalidGraphitePath,
		"/graphite#frag":     ErrInvalidGraphitePath,
	} {
		o.GraphitePath = path
		if err := o.Validate(); !errors.Is(err, want) {
			t.Errorf("%q: got %v want %v", path, err, want)
		}
	}
}
