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
	"testing"
)

func TestEqual(t *testing.T) {

	sl := SeriesList{testSeries()}
	if sl.Equal(nil) {
		t.Error("expected false")
	}

	s := testSeries()
	sl2 := SeriesList{s}

	sl2[0].Header.Name = "test2"

	if sl.Equal(sl2) {
		t.Error("expected false")
	}

}

func TestListMerge(t *testing.T) {

	sl := SeriesList{testSeries()}
	if sl.Equal(nil) {
		t.Error("expected false")
	}

	sl2 := SeriesList{testSeries(), testSeries()}
	sl2[1].Header.Name = "test2"

	sl = sl.merge(sl2)

	if len(sl) != 2 {
		t.Error("expected 2 got", len(sl))
	}

}
