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
	t4 := PathMatchTypeRegex

	var t3 PathMatchType = 27

	if t1.String() != string(PathMatchNameExact) {
		t.Errorf("expected %s got %s", PathMatchNameExact, t1.String())
	}

	if t2.String() != string(PathMatchNamePrefix) {
		t.Errorf("expected %s got %s", PathMatchNamePrefix, t2.String())
	}

	if t4.String() != string(PathMatchNameRegex) {
		t.Errorf("expected %s got %s", PathMatchNameRegex, t4.String())
	}

	if t3.String() != "27" {
		t.Errorf("expected %s got %s", "27", t3.String())
	}
}
