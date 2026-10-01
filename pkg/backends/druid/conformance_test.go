/*
 * Copyright 2026 The Trickster Authors
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

package druid

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/druid/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/stretchr/testify/require"
)

const conformanceDir = "testdata/conformance"

// splitHour returns ds as two parts, its first hour from start and the rest, as a cache entry merged from
// two fetches holds it
func splitHour(ds *dataset.DataSet, start time.Time) *dataset.DataSet {
	split := start.Add(time.Hour)
	out := ds.View(timeseries.Extent{Start: start, End: split.Add(-time.Nanosecond)})
	out.MergeParts(false, ds.View(timeseries.Extent{Start: split, End: start.Add(24 * time.Hour)}))
	return out
}

func TestWritesDruidResponses(t *testing.T) {
	// each response, Druid 37's to the request beside it, is written back byte for byte from its DataSet,
	// whole or in parts, but a topN's keys, whose order Druid's own cache varies
	requests, err := filepath.Glob(filepath.Join(conformanceDir, "*.request.json"))
	require.NoError(t, err)
	require.NotEmpty(t, requests)
	for _, file := range requests {
		name := strings.TrimSuffix(filepath.Base(file), ".request.json")
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(file)
			require.NoError(t, err)
			want, err := os.ReadFile(filepath.Join(conformanceDir, name+".response.json"))
			require.NoError(t, err)
			path := "/druid/v2"
			if strings.HasPrefix(name, "sql") {
				path = druidSQLPath
			}
			r := httptest.NewRequest(http.MethodPost, "http://trickster"+path, bytes.NewReader(body))
			r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
			trq, ro, _, err := (&Client{}).ParseTimeRangeQuery(r)
			require.NoError(t, err)
			ts, err := model.UnmarshalTimeseries(want, trq)
			require.NoError(t, err)
			for _, ds := range []*dataset.DataSet{ts.(*dataset.DataSet), splitHour(ts.(*dataset.DataSet), trq.Extent.Start)} {
				got, err := model.MarshalTimeseries(ds, ro, http.StatusOK)
				require.NoError(t, err)
				if strings.HasPrefix(name, "native_topn") {
					require.JSONEq(t, string(want), string(got))
					continue
				}
				require.Equal(t, string(want), string(got))
			}
		})
	}
}
