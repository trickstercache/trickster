/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package strings

import "testing"

func TestIndexOfString(t *testing.T) {

	arr := []string{"string0", "string1", "string2"}

	i := IndexOfString(arr, "string0")
	if i != 0 {
		t.Errorf(`expected 0. got %d`, i)
	}

	i = IndexOfString(arr, "string3")
	if i != -1 {
		t.Errorf(`expected -1. got %d`, i)
	}

}

func TestCloneMap(t *testing.T) {

	const expected = "pass"

	m := map[string]string{"test": expected}
	m2 := CloneMap(m)

	v, ok := m2["test"]
	if !ok {
		t.Errorf("expected true got %t", ok)
	}

	if v != expected {
		t.Errorf("expected %s got %s", expected, v)
	}

}

func TestEqual(t *testing.T) {

	l1 := []string{"test1", "test2"}
	l2 := []string{"test1", "test2"}
	l3 := []string{"test3", "test4"}
	l4 := []string{}

	if !Equal(l1, l2) {
		t.Error("expected true got false")
	}

	if Equal(l1, l3) {
		t.Error("expected false got true")
	}

	if Equal(l1, l4) {
		t.Error("expected false got true")
	}
}
