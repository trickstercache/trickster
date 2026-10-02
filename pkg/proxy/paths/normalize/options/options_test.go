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

func TestResolved(t *testing.T) {
	var nilOptions *Options
	if got := nilOptions.Resolved(); got != *New() {
		t.Fatalf("nil options resolved to %+v", got)
	}
	o := &Options{MergeSlashes: true, EscapedSlashes: EscapedSlashesReject}
	want := Options{DotSegments: DefaultDotSegments, MergeSlashes: true, EscapedSlashes: EscapedSlashesReject}
	if got := o.Resolved(); got != want {
		t.Fatalf("got %+v; want %+v", got, want)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		o   *Options
		err error
	}{
		{nil, nil},
		{&Options{}, nil},
		{&Options{DotSegments: DotSegmentsReject, EscapedSlashes: EscapedSlashesUnescape}, nil},
		{&Options{DotSegments: DotSegmentsOff, MergeSlashes: true}, nil},
		{&Options{DotSegments: "clean"}, ErrInvalidDotSegments},
		{&Options{EscapedSlashes: "decode"}, ErrInvalidEscapedSlashes},
	}
	for _, test := range tests {
		if err := test.o.Validate(); !errors.Is(err, test.err) {
			t.Errorf("%+v: got %v; want %v", test.o, err, test.err)
		}
	}
}

func TestCloneEqual(t *testing.T) {
	var nilOptions *Options
	if nilOptions.Clone() != nil {
		t.Fatal("nil options should clone to nil")
	}
	o := &Options{MergeSlashes: true}
	c := o.Clone()
	if c == o || !c.Equal(o) {
		t.Fatal("clone should be an equal copy")
	}
	if !nilOptions.Equal(New()) || !(&Options{}).Equal(nilOptions) {
		t.Fatal("unset options should equal the defaults")
	}
	c.EscapedSlashes = EscapedSlashesReject
	if c.Equal(o) {
		t.Fatal("options differing in escaped_slashes should not be equal")
	}
}

func TestUnmarshalOverlaysDefaults(t *testing.T) {
	o := New()
	if err := yaml.Unmarshal([]byte("merge_slashes: true"), o); err != nil {
		t.Fatal(err)
	}
	want := Options{DotSegments: DefaultDotSegments, MergeSlashes: true, EscapedSlashes: DefaultEscapedSlashes}
	if *o != want {
		t.Fatalf("got %+v; want %+v", *o, want)
	}
}
