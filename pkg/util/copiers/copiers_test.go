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

package copiers

import "testing"

func TestCopyBytes(t *testing.T) {

	m1 := CopyBytes(nil)
	if m1 != nil {
		t.Error("expected nil map")
	}

	m := []byte("test")

	m2 := CopyBytes(m)
	if len(m2) != 4 {
		t.Errorf("expected %d got %d", 4, len(m2))
	}
	if m2[0] != 't' {
		t.Errorf("expected %c got %c", 't', m2[0])
	}
}

func TestCopyInterfaces(t *testing.T) {

	m1 := CopyInterfaces(nil)
	if m1 != nil {
		t.Error("expected nil map")
	}

	m := []interface{}{1}

	m2 := CopyInterfaces(m)
	if len(m2) != 1 {
		t.Errorf("expected %d got %d", 4, len(m2))
	}
	if m2[0].(int) != 1 {
		t.Errorf("expected %d got %d", 1, m2[0])
	}

}

func TestCopyStrings(t *testing.T) {

	m1 := CopyStrings(nil)
	if m1 != nil {
		t.Error("expected nil map")
	}

	m := []string{"test"}

	m2 := CopyStrings(m)
	if len(m2) != 1 {
		t.Errorf("expected %d got %d", 1, len(m2))
	}
	if m2[0] != "test" {
		t.Errorf("expected %s got %s", "test", m2[0])
	}

}

func TestCopyStringLookup(t *testing.T) {

	m1 := CopyStringLookup(nil)
	if m1 != nil {
		t.Error("expected nil map")
	}

	const expected = "pass"

	m := map[string]string{"test": expected}
	m2 := CopyStringLookup(m)

	v, ok := m2["test"]
	if !ok {
		t.Errorf("expected true got %t", ok)
	}

	if v != expected {
		t.Errorf("expected %s got %s", expected, v)
	}

}

func TestCopyLookup(t *testing.T) {

	m1 := CopyLookup(nil)
	if m1 != nil {
		t.Error("expected nil map")
	}

	m := map[string]interface{}{"test": nil}
	m2 := CopyLookup(m)

	v, ok := m2["test"]
	if !ok {
		t.Errorf("expected true got %t", ok)
	}

	if v != nil {
		t.Errorf("expected nil got %v", v)
	}

}

func TestLookupFromStrings(t *testing.T) {

	m1 := LookupFromStrings(nil)
	if m1 != nil {
		t.Error("expected nil map")
	}

	m := []string{"test", "trickster"}
	m2 := LookupFromStrings(m)

	_, ok := m2["test"]
	if !ok {
		t.Errorf("expected true got %t", ok)
	}

}
