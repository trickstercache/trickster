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

package sizeconv

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected Size
		err      bool
	}{
		{"1024", 1024, false},
		{"64b", 64, false},
		{"1KB", 1024, false},
		{"1k", 1024, false},
		{"1KiB", 1024, false},
		{"256MB", 256 * 1024 * 1024, false},
		{"256 MB", 256 * 1024 * 1024, false},
		{"1.5GB", 1610612736, false},
		{"2TB", 2 << 40, false},
		{"0", 0, false},
		{"", 0, true},
		{"MB", 0, true},
		{"12XB", 0, true},
		{"1.2.3MB", 0, true},
		{"-5MB", 0, true},
		{"9000000000GB", 0, true},
		{"9000000000.5GB", 0, true},
	}
	for _, test := range tests {
		v, err := ParseSize(test.input)
		if test.err != (err != nil) {
			t.Errorf("input %q: unexpected error state: %v", test.input, err)
			continue
		}
		if v != test.expected {
			t.Errorf("input %q: expected %d, got %d", test.input, test.expected, v)
		}
	}
}

func TestSizeString(t *testing.T) {
	tests := []struct {
		input    Size
		expected string
	}{
		{0, "0"},
		{512, "512"},
		{1024, "1KB"},
		{256 * 1024 * 1024, "256MB"},
		{3 << 30, "3GB"},
		{2 << 40, "2TB"},
		{1536, "1536"},
	}
	for _, test := range tests {
		if s := test.input.String(); s != test.expected {
			t.Errorf("expected %q, got %q", test.expected, s)
		}
	}
}

func TestSizeYAMLRoundTrip(t *testing.T) {
	type doc struct {
		Max Size `yaml:"max"`
	}
	var d doc
	if err := yaml.Unmarshal([]byte("max: 256MB"), &d); err != nil {
		t.Fatal(err)
	}
	if d.Max != 256*1024*1024 {
		t.Errorf("unexpected value: %d", d.Max)
	}
	b, err := yaml.Marshal(&d)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "max: 256MB\n" {
		t.Errorf("unexpected marshal output: %q", string(b))
	}
	if err = yaml.Unmarshal([]byte("max: 1048576"), &d); err != nil {
		t.Fatal(err)
	}
	if d.Max != 1<<20 {
		t.Errorf("unexpected value: %d", d.Max)
	}
	if err = yaml.Unmarshal([]byte("max: nope"), &d); err == nil {
		t.Error("expected unmarshal error")
	}
	if err = yaml.Unmarshal([]byte("max: [1]"), &d); err == nil {
		t.Error("expected unmarshal error for non-scalar")
	}
}
