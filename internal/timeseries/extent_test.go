/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package timeseries

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestCompressExtents(t *testing.T) {

	tests := []struct {
		uncompressed, compressed []Extent
	}{
		{
			[]Extent{},
			[]Extent{},
		},

		{
			[]Extent{
				Extent{Start: time.Unix(90, 0), End: time.Unix(120, 0)},
				Extent{Start: time.Unix(120, 0), End: time.Unix(180, 0)},
				Extent{Start: time.Unix(30, 0), End: time.Unix(30, 0)},
				Extent{Start: time.Unix(180, 0), End: time.Unix(210, 0)},
			},
			[]Extent{
				Extent{Start: time.Unix(30, 0), End: time.Unix(30, 0)},
				Extent{Start: time.Unix(90, 0), End: time.Unix(210, 0)},
			},
		},

		{
			[]Extent{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
			},
			[]Extent{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
			},
		},

		{
			[]Extent{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
				Extent{Start: time.Unix(90, 0), End: time.Unix(120, 0)},
				Extent{Start: time.Unix(120, 0), End: time.Unix(180, 0)},
				Extent{Start: time.Unix(270, 0), End: time.Unix(360, 0)},
				Extent{Start: time.Unix(180, 0), End: time.Unix(210, 0)},
				Extent{Start: time.Unix(420, 0), End: time.Unix(480, 0)},
			},
			[]Extent{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
				Extent{Start: time.Unix(90, 0), End: time.Unix(210, 0)},
				Extent{Start: time.Unix(270, 0), End: time.Unix(360, 0)},
				Extent{Start: time.Unix(420, 0), End: time.Unix(480, 0)},
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

			res := CompressExtents(test.uncompressed, time.Duration(30)*time.Second)

			if !reflect.DeepEqual(res, test.compressed) {
				t.Fatalf("Mismatch in CompressExtents: expected=%s actual=%s", test.compressed, res)
			}
		})
	}
}
