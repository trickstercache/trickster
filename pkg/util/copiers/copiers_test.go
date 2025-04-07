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
