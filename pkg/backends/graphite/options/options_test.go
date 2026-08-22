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

	"go.yaml.in/yaml/v3"
)

func TestNewAndClone(t *testing.T) {
	o := New()
	if o == nil {
		t.Fatal("expected non-nil options")
	}
	if o.TimeZone != DefaultTimeZone || o.PassthroughMaxDataPoints {
		t.Errorf("unexpected defaults %+v", o)
	}
	o.PassthroughMaxDataPoints = true
	o2 := o.Clone()
	if o2 == nil || o2 == o || o2.TimeZone != DefaultTimeZone || !o2.PassthroughMaxDataPoints {
		t.Error("expected a distinct, equal clone")
	}
	if o2.StaticRetentions != nil {
		t.Error("nil static retentions must clone as nil")
	}
	o.StaticRetentions = []StaticRetention{{Pattern: "a", Retentions: "10s:6h"}}
	o3 := o.Clone()
	o3.StaticRetentions[0].Pattern = "b"
	if o.StaticRetentions[0].Pattern != "a" || len(o3.StaticRetentions) != 1 {
		t.Error("static retentions must be deep-copied")
	}
	if o.ResolutionRegistry.TTL == 0 || !o.ResolutionRegistry.Persist || o.FindCacheTTL == 0 {
		t.Error("registry defaults")
	}
}

func TestUnmarshalYAML(t *testing.T) {
	var o Options
	if err := yaml.Unmarshal([]byte("{}"), &o); err != nil {
		t.Error(err)
	}
	if o.TimeZone != DefaultTimeZone {
		t.Errorf("expected the default time zone to apply, got %q", o.TimeZone)
	}
	if err := yaml.Unmarshal([]byte("time_zone: Europe/Berlin\npassthrough_max_data_points: true"), &o); err != nil {
		t.Error(err)
	}
	if o.TimeZone != "Europe/Berlin" || !o.PassthroughMaxDataPoints {
		t.Errorf("unexpected options %+v", o)
	}
	if err := yaml.Unmarshal([]byte("- not a mapping"), &o); err == nil {
		t.Error("expected a decode error for a sequence node")
	}
}
