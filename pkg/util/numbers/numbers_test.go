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

package numbers

import (
	"math"
	"testing"
)

func TestSafeAdd(t *testing.T) {
	const i1 = math.MaxInt
	const i2 = 500
	const i3 = 1
	i, ok := SafeAdd(i1, i3)
	if i != math.MaxInt {
		t.Errorf("expected %d got %d", 0, i)
	}
	if ok {
		t.Error("expected false")
	}

	i, ok = SafeAdd(i2, i3)
	if i != 501 {
		t.Errorf("expected %d got %d", 501, i)
	}
	if !ok {
		t.Error("expected true")
	}
}
