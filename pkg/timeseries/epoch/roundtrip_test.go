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

package epoch

import (
	"testing"
)

func TestEpochsRoundTrip(t *testing.T) {
	v := Epochs{Epoch(1000000), Epoch(2000000)}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Epochs
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(v2) != 2 {
		t.Fatal("expected 2 epochs")
	}
	if v2[0] != v[0] {
		t.Fatal("first epoch mismatch")
	}
	if v2[1] != v[1] {
		t.Fatal("second epoch mismatch")
	}
}
