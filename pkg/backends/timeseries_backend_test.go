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

package backends

import (
	"testing"

	"github.com/gorilla/mux"
	bo "github.com/tricksterproxy/trickster/pkg/backends/options"
)

func TestNewTimeseriesBackend(t *testing.T) {
	tb, _ := NewTimeseriesBackend("test1", bo.New(), nil, mux.NewRouter(), nil, nil)
	if tb.Name() != "test1" {
		t.Error("expected test1 got", tb.Name())
	}
}

func TestFastForwardRequest(t *testing.T) {
	tb, _ := NewTimeseriesBackend("test1", nil, nil, nil, nil, nil)
	// should always return nil for the base Timeseries Backend
	r, err := tb.FastForwardRequest(nil)
	if r != nil || err != nil {
		t.Error("expected nil")
	}
}

func TestParseTimeRangeQuery(t *testing.T) {
	tb, _ := NewTimeseriesBackend("test1", nil, nil, nil, nil, nil)
	// should always return nil for the base Timeseries Backend
	r, _, _, err := tb.ParseTimeRangeQuery(nil)
	if r != nil || err != nil {
		t.Error("expected nil")
	}
}

func TestSetExtent(t *testing.T) {
	tb, _ := NewTimeseriesBackend("test1", nil, nil, nil, nil, nil)
	// this literally does nothing for the base Timeseries Backend but provide coverage
	tb.SetExtent(nil, nil, nil)
	if tb.Name() != "test1" {
		t.Error("name mismatch")
	}
}

func TestModeler(t *testing.T) {
	tb, _ := NewTimeseriesBackend("test1", nil, nil, nil, nil, nil)
	// should always return nil for the base Timeseries Backend
	if tb.Modeler() != nil {
		t.Error("name mismatch")
	}
}
