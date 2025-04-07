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

package strings

import (
	"strings"
	"testing"
)

func TestSubstring(t *testing.T) {
	str := "hello world"
	if got := Substring(str, 0, 2); got != "he" {
		t.Errorf("Expected 'he', got %s", got)
	}
	if got := Substring(str, 4, 3); got != "o w" {
		t.Errorf("Expected 'o w', got %s", got)
	}
	if got := Substring(str, 9, 3); got != "" {
		t.Errorf("Expected Substring(str, 9, 3) to overflow and return '', got %s", got)
	}
}

func TestStringMap(t *testing.T) {

	sm := StringMap(map[string]string{"test": "value"})
	s := sm.String()
	const expected = `{"test":"value"}`

	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

}

func TestUnique(t *testing.T) {
	initial := []string{"test", "test", "test1", "test2", "test2", "test", "test3"}
	expected := "test,test1,test2,test3"
	after := strings.Join(Unique(initial), ",")
	if expected != after {
		t.Errorf("expected %s got %s", expected, after)
	}

	empty := Unique(nil)
	if len(empty) != 0 {
		t.Error("expected empty list")
	}
}

func BenchmarkUnique(b *testing.B) {
	initial := []string{"test", "test", "test1", "test2", "test2", "test", "test3"}
	for i := 0; i < b.N; i++ {
		Unique(initial)
	}
}

func TestGetInt(t *testing.T) {

	m := StringMap{"trickster": "proxy", "test": "1"}

	if _, err := m.GetInt("invalid"); err != ErrKeyNotInMap {
		t.Error("expected err for Key Not In Map", err)
	}

	if _, err := m.GetInt("trickster"); err == nil {
		t.Error("expected err for invalid conversion", err)
	}
	i, err := m.GetInt("test")
	if err != nil {
		t.Error(err)
	}
	if i != 1 {
		t.Errorf("expected %d got %d", 1, i)
	}
}
