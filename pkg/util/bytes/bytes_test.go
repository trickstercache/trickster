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

package bytes

import "testing"

func TestMergeSlices(t *testing.T) {
	cases := []struct {
		name string
		a, b []byte
		want []byte
	}{
		{"both empty", nil, nil, []byte{}},
		{"empty a", nil, []byte("test"), []byte("test")},
		{"empty b", []byte("test"), nil, []byte("test")},
		{"merge", []byte("hello"), []byte(" world"), []byte("hello world")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MergeSlices(c.a, c.b)
			if string(got) != string(c.want) {
				t.Errorf("wanted %q got %q", c.want, got)
			}
		})
	}

	a := []byte("hello")
	b := []byte(" world")
	got := MergeSlices(a, b)
	a[0] = 'j'
	if string(got) != "hello world" {
		t.Errorf("expected merged slice to be independent of inputs, got %q", got)
	}
}
