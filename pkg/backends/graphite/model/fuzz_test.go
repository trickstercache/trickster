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

package model

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func FuzzUnmarshalRaw(f *testing.F) {
	// an origin response is untrusted: a malformed or truncated body must fail
	// closed with an error, never panic, never yield a self-contradictory series
	for _, s := range []string{
		sampleRaw, sampleJSON, sampleNulls,
		"a.b,100,130,10|None,1.5,None\n",
		"a,b,c,100,110,10|\n",
		// the malformed shapes TestUnmarshalRaw asserts on, so the corpus
		// starts at the error paths rather than having to find them
		"nopipe", "a,1,2|1", "a,x,2,3|1", "a,1,x,3|1", "a,1,2,x|1",
		"a,1,2,0|1", "a,1,2,10|x", "a,1,2,10|1,2",
		// boundary and overflow bait for the header arithmetic
		"a,0,0,1|", "a,-1,-2,1|1", "a,9223372036854775807,1,1|1",
		"a,1,9223372036854775807,1|1", "a,1,2,9223372036854775807|1",
		// JSON that decodes but is semantically empty or hostile
		"[]", "[{}]", `[{"target":"a","datapoints":[]}]`,
		`[{"target":"a","datapoints":[[1,2],[3]]}]`,
		`[{"target":"a","datapoints":[[null,null]]}]`,
		`[{"target":"a","tags":{"n":"v"},"datapoints":[[1.5,1787349960]]}]`,
		"", " ", "\n", "|", ",", "[", "{",
	} {
		f.Add(s)
	}
	steps := []time.Duration{0, 10 * time.Second, time.Minute}
	f.Fuzz(func(t *testing.T, body string) {
		for _, step := range steps {
			// a fresh query per attempt: unmarshaling adopts the observed
			// step into the query when it has none
			q := trq(step)
			ts, err := UnmarshalTimeseries([]byte(body), q)
			if err != nil {
				// fail closed: an error must not also hand back data
				if ts != nil {
					t.Fatalf("step %v: error %v returned a non-nil timeseries", step, err)
				}
				continue
			}
			if ts == nil {
				t.Fatalf("step %v: nil timeseries with no error", step)
			}
			ds, ok := ts.(*dataset.DataSet)
			if !ok {
				t.Fatalf("step %v: unexpected timeseries type %T", step, ts)
			}
			for _, res := range ds.Results {
				for _, sr := range res.SeriesList {
					if sr == nil {
						t.Fatalf("step %v: nil series accepted", step)
					}
					// the marshalers index Values[0] for every point, so a
					// series carrying any other shape would panic at render
					if len(sr.Header.ValueFieldsList) != 1 {
						t.Fatalf("step %v: series %q has %d value fields, want 1",
							step, sr.Header.Name, len(sr.Header.ValueFieldsList))
					}
					for _, pt := range sr.Points {
						if len(pt.Values) != 1 {
							t.Fatalf("step %v: series %q has a point with %d values, want 1",
								step, sr.Header.Name, len(pt.Values))
						}
					}
					// a series that survived decoding must never carry a
					// negative step: extents are computed from it
					if seriesStep(&sr.Header) < 0 {
						t.Fatalf("step %v: series %q decoded a negative step",
							step, sr.Header.Name)
					}
				}
			}
			// anything that decodes must render in every client format,
			// since that is exactly what the delta lane will do with it
			for _, format := range []string{FormatJSON, FormatRaw, FormatCSV, FormatMsgPack} {
				if _, err := MarshalTimeseries(ds,
					&timeseries.RequestOptions{ProviderRequest: RenderOptions{Format: format}},
					200); err != nil {
					t.Fatalf("step %v: decoded body failed to render as %s: %v",
						step, format, err)
				}
			}
		}
	})
}
