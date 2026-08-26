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

package manager

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestNewOptions(t *testing.T) {
	o := NewOptions()
	if o.MaxSizeBytes != DefaultMaxSizeBytes ||
		o.RetentionCount != DefaultRetentionCount ||
		o.RetentionAge != DefaultRetentionAge ||
		o.Compress != DefaultCompress ||
		o.FileMode != DefaultFileMode {
		t.Errorf("unexpected defaults: %+v", o)
	}
}

func TestOptionsClone(t *testing.T) {
	o := NewOptions()
	o.Filename = "/tmp/x.log"
	c := o.Clone()
	if c == o || *c != *o {
		t.Errorf("unexpected clone: %+v", c)
	}
}

func TestInstanceFilename(t *testing.T) {
	tests := []struct {
		name     string
		id       int
		expected string
	}{
		{"/var/log/trickster.log", 0, "/var/log/trickster.log"},
		{"/var/log/trickster.log", -1, "/var/log/trickster.log"},
		{"/var/log/trickster.log", 2, "/var/log/trickster.2.log"},
		{"/var/log/trickster.txt", 2, "/var/log/trickster.2.txt"},
		{"/var/log/trickster", 2, "/var/log/trickster.2"},
		{"/var/log/a.log.d/trickster.log", 3, "/var/log/a.log.d/trickster.3.log"},
	}
	for _, test := range tests {
		if s := InstanceFilename(test.name, test.id); s != test.expected {
			t.Errorf("expected %s, got %s", test.expected, s)
		}
	}
}

func TestValidateOptionsConflicts(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.log")
	a := NewOptions()
	a.Filename = name
	b := a.Clone()
	if err := ValidateOptions(a, b); err != nil {
		t.Fatalf("matching options failed validation: %v", err)
	}
	b.RetentionCount++
	if err := ValidateOptions(a, b); !errors.Is(err, ErrConflictingOptions) {
		t.Fatalf("expected conflicting options error, got %v", err)
	}
}
