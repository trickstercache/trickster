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

package timeseries

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// parseExtentListString mirrors the parser at
// pkg/proxy/headers/result_header.go (the `fetched` case): split on ";",
// then split each piece on "-" to recover start/end epoch ms.
func parseExtentListString(s string) ExtentList {
	if s == "" {
		return ExtentList{}
	}
	fparts := strings.Split(s, ";")
	el := make(ExtentList, 0, len(fparts))
	for _, fpart := range fparts {
		i := strings.Index(fpart, "-")
		if i <= 0 || i >= len(fpart)-1 {
			continue
		}
		start, err := strconv.ParseInt(fpart[0:i], 10, 64)
		if err != nil {
			continue
		}
		end, err := strconv.ParseInt(fpart[i+1:], 10, 64)
		if err != nil {
			continue
		}
		el = append(el, Extent{
			Start: time.Unix(0, start*1000000),
			End:   time.Unix(0, end*1000000),
		})
	}
	return el
}

func TestExtentListStringRoundTrip(t *testing.T) {
	t.Parallel()
	in := ExtentList{
		{Start: time.Unix(0, 100*1000000), End: time.Unix(0, 200*1000000)},
		{Start: time.Unix(0, 600*1000000), End: time.Unix(0, 900*1000000)},
		{Start: time.Unix(0, 1100*1000000), End: time.Unix(0, 1300*1000000)},
	}
	got := parseExtentListString(in.String())
	if len(got) != len(in) {
		t.Fatalf("round-trip length mismatch: got %d extents, want %d (encoded=%q)",
			len(got), len(in), in.String())
	}
	for i := range in {
		if !got[i].Start.Equal(in[i].Start) || !got[i].End.Equal(in[i].End) {
			t.Errorf("extent %d mismatch: got %s want %s", i, got[i].String(), in[i].String())
		}
	}
}
