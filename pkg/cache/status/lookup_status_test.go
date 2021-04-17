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

package status

import "testing"

func TestLookupStatusString(t *testing.T) {

	t1 := LookupStatusHit
	t2 := LookupStatusKeyMiss

	var t3 LookupStatus = 99

	if t1.String() != "hit" {
		t.Errorf("expected %s got %s", "hit", t1.String())
	}

	if t2.String() != "kmiss" {
		t.Errorf("expected %s got %s", "kmiss", t2.String())
	}

	if t3.String() != "99" {
		t.Errorf("expected %s got %s", "99", t3.String())
	}
}
