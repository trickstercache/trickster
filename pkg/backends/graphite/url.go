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

package graphite

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/model"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

var errNoRenderQuery = errors.New("graphite: time range query carries no render query")

// SetExtent rewrites an upstream render request to fetch exactly the given
// extent's buckets, pinning `now` so now-from equals the age the client's
// query resolved at: a mid-window gap fetch would otherwise have a younger
// from, be served from a finer rung, and not stitch with the rest.
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent,
) error {
	if r == nil || trq == nil || extent == nil || trq.Step <= 0 {
		return errNoRenderQuery
	}
	rq, ok := trq.ParsedQuery.(*RenderQuery)
	if !ok || rq == nil || len(rq.Targets) == 0 {
		return errNoRenderQuery
	}
	from, until := resolution.RequestWindow(extent.Start, extent.End, trq.Step)
	if !until.After(from) {
		return errNoRenderQuery
	}
	if until.Sub(from) == trq.Step {
		from = from.Add(-trq.Step)
	}
	v, _, _ := params.GetRequestValues(r)
	for _, p := range parsing.UpstreamStripParams {
		v.Del(p)
	}
	v.Del("target[]")
	v.Del("tz")
	v.Del("template")
	v[upTarget] = []string{rq.Targets[0].Source}
	v.Set("from", strconv.FormatInt(from.Unix(), 10))
	v.Set("until", strconv.FormatInt(until.Unix(), 10))
	v.Set("now", strconv.FormatInt(from.Add(rq.EffectiveAge).Unix(), 10))
	v.Set("format", model.FormatJSON)
	params.SetRequestValues(r, v)
	return nil
}

func unmarshalFetch(r io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	ts, err := model.UnmarshalTimeseriesReader(r, trq)
	if ds, ok := ts.(*dataset.DataSet); ok && ds != nil && err == nil {
		trimToExtent(ds, trq.Extent)
	}
	return ts, err
}

func trimToExtent(ds *dataset.DataSet, e timeseries.Extent) {
	if e.Start.IsZero() || e.End.IsZero() {
		return
	}
	start, end := epoch.Epoch(e.Start.UnixNano()), epoch.Epoch(e.End.UnixNano())
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			// points are ascending, so only each end is examined
			i, j := 0, len(s.Points)
			for i < j && s.Points[i].Epoch < start {
				s.PointSize -= int64(s.Points[i].Size)
				i++
			}
			for j > i && s.Points[j-1].Epoch > end {
				s.PointSize -= int64(s.Points[j-1].Size)
				j--
			}
			if i > 0 || j < len(s.Points) {
				s.Points = s.Points[i:j]
			}
		}
	}
}
