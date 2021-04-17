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

package rule

import (
	"strconv"
	"testing"
)

func TestDecodingFuncs(t *testing.T) {

	tests := []struct {
		encoding, input, expected string
		idx                       int
	}{
		{"base64", "", "", -1},
		{"base64", "dHJpY2tzdGVy", "trickster", -1},
		{"base64", "", "", 1},
		{"base64", "a", "", -1},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			if f, ok := decodingFuncs[encoding(test.encoding)]; ok {
				got := f(test.input, " ", test.idx)
				if got != test.expected {
					t.Errorf("\ngot      %s\nexpected %s", got, test.expected)
				}
			} else {
				t.Errorf("unknown encoding %v", test.encoding)
			}
		})
	}

}
