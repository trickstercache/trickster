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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"go.yaml.in/yaml/v3"
)

func TestOptionsDefaultsCloneAndValidate(t *testing.T) {
	o := New()
	if o.MaxResultRows != DefaultMaxResultRows || o.MaxResultSizeBytes != DefaultMaxResultSizeBytes {
		t.Fatalf("New() = %+v", o)
	}
	clone := o.Clone()
	clone.MaxResultRows++
	if clone == o || clone.MaxResultRows == o.MaxResultRows {
		t.Fatal("Clone did not produce an independent copy")
	}
	var nilOptions *Options
	if nilOptions.Clone() != nil || nilOptions.Validate() != nil {
		t.Fatal("nil options should clone and validate as nil")
	}
	for _, invalid := range []*Options{
		{MaxResultRows: 0, MaxResultSizeBytes: 1},
		{MaxResultRows: 1, MaxResultSizeBytes: 0},
	} {
		if invalid.Validate() == nil {
			t.Errorf("Validate(%+v) succeeded", invalid)
		}
	}
	if err := o.Validate(); err != nil {
		t.Errorf("default options failed validation: %v", err)
	}
}

func TestOptionsUnmarshalYAML(t *testing.T) {
	var o Options
	if err := yaml.Unmarshal([]byte("max_result_rows: 12"), &o); err != nil {
		t.Fatal(err)
	}
	if o.MaxResultRows != 12 || o.MaxResultSizeBytes != DefaultMaxResultSizeBytes {
		t.Fatalf("partial options = %+v", o)
	}
	if err := yaml.Unmarshal([]byte("- invalid"), &o); err == nil {
		t.Fatal("invalid options YAML succeeded")
	}
}

func TestListenerOptionsDefaultsCloneAndValidate(t *testing.T) {
	o := NewListener()
	if o.HandshakeTimeout != timeconv.Duration(DefaultHandshakeTimeout) ||
		o.ReadTimeout != timeconv.Duration(DefaultReadTimeout) ||
		o.WriteTimeout != timeconv.Duration(DefaultWriteTimeout) ||
		o.IdleTimeout != timeconv.Duration(DefaultIdleTimeout) ||
		o.MaxPacketSizeBytes != DefaultMaxPacketSizeBytes ||
		o.MaxQuerySizeBytes != DefaultMaxQuerySizeBytes {
		t.Fatalf("NewListener() = %+v", o)
	}
	clone := o.Clone()
	clone.MaxQuerySizeBytes++
	if clone == o || clone.MaxQuerySizeBytes == o.MaxQuerySizeBytes {
		t.Fatal("Clone did not produce an independent listener copy")
	}
	var nilOptions *ListenerOptions
	if nilOptions.Clone() != nil || nilOptions.Validate() != nil {
		t.Fatal("nil listener options should clone and validate as nil")
	}
	for _, invalid := range []*ListenerOptions{
		{HandshakeTimeout: 0, ReadTimeout: 1, WriteTimeout: 1, IdleTimeout: 1,
			MaxPacketSizeBytes: DefaultMaxPacketSizeBytes, MaxQuerySizeBytes: 1},
		{HandshakeTimeout: 1, ReadTimeout: 1, WriteTimeout: 1, IdleTimeout: 1,
			MaxPacketSizeBytes: 1, MaxQuerySizeBytes: 1},
		{HandshakeTimeout: 1, ReadTimeout: 1, WriteTimeout: 1, IdleTimeout: 1,
			MaxPacketSizeBytes: DefaultMaxPacketSizeBytes, MaxQuerySizeBytes: 0},
		{HandshakeTimeout: 1, ReadTimeout: 1, WriteTimeout: 1, IdleTimeout: 1,
			MaxPacketSizeBytes: DefaultMaxPacketSizeBytes, MaxQuerySizeBytes: DefaultMaxPacketSizeBytes + 1},
	} {
		if invalid.Validate() == nil {
			t.Errorf("Validate(%+v) succeeded", invalid)
		}
	}
	if err := o.Validate(); err != nil {
		t.Errorf("default listener options failed validation: %v", err)
	}
}

func TestListenerOptionsUnmarshalYAML(t *testing.T) {
	var o ListenerOptions
	if err := yaml.Unmarshal([]byte("read_timeout: 2s"), &o); err != nil {
		t.Fatal(err)
	}
	if o.ReadTimeout != timeconv.Duration(2*time.Second) ||
		o.HandshakeTimeout != timeconv.Duration(DefaultHandshakeTimeout) ||
		o.MaxQuerySizeBytes != DefaultMaxQuerySizeBytes {
		t.Fatalf("partial listener options = %+v", o)
	}
	if err := yaml.Unmarshal([]byte("- invalid"), &o); err == nil {
		t.Fatal("invalid listener options YAML succeeded")
	}
}
