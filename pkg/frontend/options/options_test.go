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

package options

import "testing"

func TestFrontendOptions(t *testing.T) {

	f1 := New()
	f2 := New()

	b := f1.Equal(f2)
	if !b {
		t.Errorf("expected %t got %t", true, b)
	}

	f1.ListenAddress = "trickster"
	f2 = f1.Clone()
	if !b {
		t.Errorf("expected %t got %t", true, b)
	}
}
