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

package timeseries

import "testing"

func TestFieldDefinitionsClone(t *testing.T) {
	fds := FieldDefinitions{
		{Name: "time", DataType: Int64},
		{Name: "value", DataType: Float64},
	}
	c := fds.Clone()
	if len(c) != len(fds) {
		t.Fatalf("expected %d got %d", len(fds), len(c))
	}
	c[0].Name = "mutated"
	if fds[0].Name == "mutated" {
		t.Error("clone mutation affected original")
	}
}

func TestFieldDefinitionsToLookup(t *testing.T) {
	t.Run("basic lookup", func(t *testing.T) {
		fds := FieldDefinitions{
			{Name: "time", DataType: Int64},
			{Name: "value", DataType: Float64},
		}
		lookup := fds.ToLookup()
		if len(lookup) != 2 {
			t.Fatalf("expected 2 got %d", len(lookup))
		}
		if lookup["time"].DataType != Int64 {
			t.Error("expected Int64 for time")
		}
		if lookup["value"].DataType != Float64 {
			t.Error("expected Float64 for value")
		}
	})

	t.Run("empty list", func(t *testing.T) {
		lookup := FieldDefinitions{}.ToLookup()
		if len(lookup) != 0 {
			t.Errorf("expected 0 got %d", len(lookup))
		}
	})

	t.Run("duplicate names last wins", func(t *testing.T) {
		fds := FieldDefinitions{
			{Name: "val", DataType: Int64},
			{Name: "val", DataType: Float64},
		}
		lookup := fds.ToLookup()
		if len(lookup) != 1 {
			t.Fatalf("expected 1 got %d", len(lookup))
		}
		if lookup["val"].DataType != Float64 {
			t.Error("expected last definition to win")
		}
	})
}

func TestFieldDefinitionSize(t *testing.T) {
	fd := FieldDefinition{
		Name:      "temperature",
		DataType:  Float64,
		SDataType: "float",
	}
	// formula: 32 + len(Name) + len(SDataType) + 1 + 24
	// = 32 + 11 + 5 + 1 + 24 = 73
	if s := fd.Size(); s != 73 {
		t.Errorf("expected 73 got %d", s)
	}
}

func TestFieldDefinitionStringSingle(t *testing.T) {
	fd := FieldDefinition{
		Name:     "test",
		DataType: Int64,
	}
	const expected = `{"name":"test","type":1}`
	if s := fd.String(); s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}
}

func TestFieldDefinitionsString(t *testing.T) {
	t.Run("non-empty", func(t *testing.T) {
		fd := FieldDefinitions{
			{Name: "test", DataType: Int64},
		}
		const expected = `[{"name":"test","type":1}]`
		if fd.String() != expected {
			t.Errorf("expected %s got %s", expected, fd.String())
		}
	})

	t.Run("empty list", func(t *testing.T) {
		fd := FieldDefinitions{}
		if s := fd.String(); s != "[]" {
			t.Errorf("expected [] got %s", s)
		}
	})
}
