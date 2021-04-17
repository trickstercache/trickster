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

package engines

import "testing"

func TestRevalidationStatusString(t *testing.T) {

	t1 := RevalStatusNone
	t2 := RevalStatusInProgress
	var t3 RevalidationStatus = 10

	if t1.String() != "none" {
		t.Errorf("expected %s got %s", "none", t1.String())
	}

	if t2.String() != "revalidating" {
		t.Errorf("expected %s got %s", "revalidating", t2.String())
	}

	if t3.String() != "10" {
		t.Errorf("expected %s got %s", "10", t3.String())
	}

}
