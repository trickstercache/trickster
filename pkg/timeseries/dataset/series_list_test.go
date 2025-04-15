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

package dataset

import (
	"fmt"
	"slices"
	"testing"
)

func TestEqualHeader(t *testing.T) {

	sl := SeriesList{testSeries()}
	if sl.EqualHeader(nil) {
		t.Error("expected false")
	}

	s := testSeries()
	sl2 := SeriesList{s}

	sl2[0].Header.Name = "test2"

	if sl.EqualHeader(sl2) {
		t.Error("expected false")
	}

}

func TestListMerge(t *testing.T) {

	tests := []struct {
		sl1, sl2 SeriesList
		expected []string
	}{
		{
			sl1:      SeriesList{testSeries()},
			sl2:      SeriesList{testSeries2()},
			expected: []string{"test", "test2"},
		},
		{
			sl1:      SeriesList{testSeries(), testSeries3()},
			sl2:      SeriesList{testSeries(), testSeries2()},
			expected: []string{"test", "test3", "test2"},
		},
		{
			sl1:      SeriesList{testSeries3(), testSeries2(), testSeries(), testSeries3()},
			sl2:      SeriesList{testSeries(), testSeries2(), testSeries3(), testSeries3(), testSeries()},
			expected: []string{"test", "test2", "test3"},
		},
	}

	namesFromList := func(sl SeriesList) []string {
		out := make([]string, len(sl))
		for i, s := range sl {
			out[i] = s.Header.Name
		}
		return out
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			if test.sl1.EqualHeader(nil) || test.sl2.EqualHeader(nil) {
				t.Error("expected false")
			}
			out := test.sl1.Merge(test.sl2, true)
			if len(out) != len(test.expected) {
				t.Errorf("expected %d got %d", len(test.expected), len(out))
			} else {
				names := namesFromList(out)
				if !slices.Equal(names, test.expected) {
					t.Errorf("expected %v got %v", test.expected, names)
				}
			}
		})
	}

}
