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

package matching

import "testing"

func TestPMTString(t *testing.T) {

	t1 := PathMatchTypeExact
	t2 := PathMatchTypePrefix

	var t3 PathMatchType = 3

	if t1.String() != PathMatchNameExact {
		t.Errorf("expected %s got %s", PathMatchNameExact, t1.String())
	}

	if t2.String() != PathMatchNamePrefix {
		t.Errorf("expected %s got %s", PathMatchNamePrefix, t2.String())
	}

	if t3.String() != "3" {
		t.Errorf("expected %s got %s", "3", t3.String())
	}
}
