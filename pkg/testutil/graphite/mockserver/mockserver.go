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

// Package mockserver is an in-process mock of graphite-web's Whisper fetch
// semantics; targets match as path expressions only, functions are not evaluated.
package mockserver

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/model"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// Metric is one Whisper file
type Metric struct {
	Path   string
	Ladder *resolution.Ladder
	// Created, when set, makes points older than it null (a young metric)
	Created time.Time
}

// Server is the mock origin
type Server struct {
	*httptest.Server
	mu      sync.RWMutex
	metrics map[string]*Metric
	// Now is the wall clock when a request carries no `now` parameter
	Now func() time.Time
	// Renders and Finds count requests by endpoint
	Renders, Finds atomic.Int64
	// Fail, when non-zero, makes every request return that status
	Fail atomic.Int32
	// Delay, when non-zero, holds each response before writing, widening the
	// window in which concurrent requests collapse into one upstream fetch
	Delay atomic.Int64
	// StartSkew, when non-zero, shifts the reported start of responses carrying
	// maxDataPoints, simulating an origin whose consolidation aligns differently
	StartSkew atomic.Int64
	// Log keeps the query string of every request, oldest first
	log []string
}

// New starts a mock origin with no metrics
func New() *Server {
	s := &Server{metrics: make(map[string]*Metric), Now: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("/render", s.render)
	mux.HandleFunc("/metrics/find", s.find)
	mux.HandleFunc("/metrics/expand", s.expand)
	s.Server = httptest.NewServer(mux)
	return s
}

// Add registers a metric with a storage-schemas.conf retention list
func (s *Server) Add(metric, retentions string) *Metric {
	l, err := resolution.ParseRetentions(retentions)
	if err != nil {
		panic(err)
	}
	m := &Metric{Path: metric, Ladder: l}
	s.mu.Lock()
	s.metrics[metric] = m
	s.mu.Unlock()
	return m
}

// Remove deletes a metric
func (s *Server) Remove(metric string) {
	s.mu.Lock()
	delete(s.metrics, metric)
	s.mu.Unlock()
}

// Requests returns the logged request query strings
func (s *Server) Requests() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.log...)
}

// ResetCounters clears the counters and log
func (s *Server) ResetCounters() {
	s.Renders.Store(0)
	s.Finds.Store(0)
	s.mu.Lock()
	s.log = nil
	s.mu.Unlock()
}

// returns the metrics matching a path expression, segment by segment,
// with * ? [..] and {a,b}
func (s *Server) matches(expr string) []*Metric {
	qs := strings.Split(expr, ".")
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Metric
	for p, m := range s.metrics {
		segs := strings.Split(p, ".")
		if len(segs) == len(qs) && segmentsMatch(qs, segs) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b *Metric) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func segmentsMatch(qs, segs []string) bool {
	for i, q := range qs {
		if !segmentMatch(q, segs[i]) {
			return false
		}
	}
	return true
}

func segmentMatch(q, seg string) bool {
	for _, alt := range expandBraces(q) {
		if ok, _ := path.Match(alt, seg); ok {
			return true
		}
	}
	return false
}

func expandBraces(q string) []string {
	i := strings.IndexByte(q, '{')
	if i < 0 {
		return []string{q}
	}
	j := strings.IndexByte(q[i:], '}')
	if j < 0 {
		return []string{q}
	}
	var out []string
	for alt := range strings.SplitSeq(q[i+1:i+j], ",") {
		out = append(out, expandBraces(q[:i]+alt+q[i+j+1:])...)
	}
	return out
}

func (s *Server) now(v url.Values) (time.Time, error) {
	now := s.Now().Truncate(time.Second)
	if n := v.Get("now"); n != "" {
		return parsing.ParseATTime(n, time.UTC, now)
	}
	return now, nil
}

type series struct {
	target     string
	start, end time.Time
	step       time.Duration
	values     []*float64
}

// serves /render for format=raw and format=json
func (s *Server) render(w http.ResponseWriter, r *http.Request) {
	s.Renders.Add(1)
	if d := s.Delay.Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
	if f := s.Fail.Load(); f != 0 {
		http.Error(w, "mock origin failure", int(f))
		return
	}
	s.mu.Lock()
	s.log = append(s.log, r.URL.Path+"?"+r.URL.RawQuery)
	s.mu.Unlock()
	v := r.URL.Query()
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		maps.Copy(v, r.PostForm)
	}
	now, err := s.now(v)
	if err != nil {
		http.Error(w, "Invalid parameters ("+err.Error()+")", http.StatusBadRequest)
		return
	}
	ext, err := parsing.ParseTimeRange(v.Get("from"), v.Get("until"), time.UTC, now)
	if err != nil {
		http.Error(w, "Invalid parameters ("+err.Error()+")", http.StatusBadRequest)
		return
	}
	ro := model.RenderOptions{
		Format: v.Get("format"), Pretty: v.Get("pretty") != "", JSONP: v.Get("jsonp"),
		Location: time.UTC,
	}
	if ro.Format == "" {
		ro.Format = model.FormatJSON
	}
	if m := v.Get("maxDataPoints"); m != "" {
		ro.MaxDataPoints, _ = strconv.Atoi(m)
	}
	_, ro.NoNullPoints = v["noNullPoints"]
	if x := v.Get("xFilesFactor"); x != "" {
		ro.XFilesFactor, _ = strconv.ParseFloat(x, 64)
	}
	if tz := v.Get("tz"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			ro.Location = loc
		}
	}
	ds := &dataset.DataSet{TimeRangeQuery: &timeseries.TimeRangeQuery{}, Results: []*dataset.Result{{}}}
	for _, target := range v["target"] {
		for _, m := range s.matches(target) {
			if sr, ok := fetch(m, ext.Start, ext.End, now); ok {
				if skew := s.StartSkew.Load(); skew != 0 && ro.MaxDataPoints > 0 {
					// simulate an origin whose consolidation aligns differently
					sr.start = sr.start.Add(time.Duration(skew))
				}
				ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, sr.toSeries())
				ro.PathExpressions = append(ro.PathExpressions, target)
			}
		}
	}
	if err := model.MarshalTimeseriesWriter(ds, &timeseries.RequestOptions{ProviderRequest: ro}, http.StatusOK, w); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// converts a fetched series to the DataSet form the model renders
func (sr series) toSeries() *dataset.Series {
	pts := make(dataset.Points, len(sr.values))
	for i, val := range sr.values {
		p := dataset.Point{
			Epoch:  epoch.FromSecs(sr.start.Unix() + int64(i)*int64(sr.step/time.Second)),
			Values: []any{nil}, Size: 24,
		}
		if val != nil {
			p.Values[0] = *val
		}
		pts[i] = p
	}
	return &dataset.Series{Header: dataset.SeriesHeader{
		Name: sr.target, Tags: dataset.Tags{"name": sr.target},
		TimestampField: model.StepField(sr.step),
	}, Points: pts, PointSize: int64(len(pts)) * 24}
}

// reproduces the semantics of whisper's file_fetch + __archive_fetch
func fetch(m *Metric, from, until, now time.Time) (series, bool) {
	var sr series
	maxRet := m.Ladder.MaxRetention()
	from, until, ok := resolution.Clamp(from, until, now, maxRet)
	if !ok {
		return sr, false
	}
	age := now.Sub(from)
	step, _ := m.Ladder.StepFor(age)
	start, end := resolution.AlignInterval(from, until, step)
	n := int(end.Sub(start) / step)
	sr = series{target: m.Path, start: start, end: end, step: step, values: make([]*float64, n)}
	oldest := now.Add(-maxRet)
	for i := range sr.values {
		ts := start.Add(time.Duration(i) * step)
		if ts.Before(oldest) || ts.After(now) || (!m.Created.IsZero() && ts.Before(m.Created)) {
			continue
		}
		f := Value(m.Path, ts)
		sr.values[i] = &f
	}
	return sr, true
}

// Value is the deterministic point value the mock serves for a metric at a
// timestamp, so tests can assert on content
func Value(metric string, ts time.Time) float64 {
	return float64(ts.Unix()%86400) + float64(len(metric))
}

// serves /metrics/expand, enumerating the concrete leaf paths a pattern
// matches, as one document where graphite-web may emit one per brace alternative
func (s *Server) expand(w http.ResponseWriter, r *http.Request) {
	s.Finds.Add(1)
	if f := s.Fail.Load(); f != 0 {
		http.Error(w, "mock origin failure", int(f))
		return
	}
	s.mu.Lock()
	s.log = append(s.log, r.URL.Path+"?"+r.URL.RawQuery)
	s.mu.Unlock()
	out := []string{}
	for _, m := range s.matches(r.URL.Query().Get("query")) {
		out = append(out, m.Path)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]string{"results": out})
}

// serves /metrics/find (treejson)
func (s *Server) find(w http.ResponseWriter, r *http.Request) {
	s.Finds.Add(1)
	if f := s.Fail.Load(); f != 0 {
		http.Error(w, "mock origin failure", int(f))
		return
	}
	s.mu.Lock()
	s.log = append(s.log, r.URL.Path+"?"+r.URL.RawQuery)
	s.mu.Unlock()
	q := r.URL.Query().Get("query")
	qs := strings.Split(q, ".")
	type node struct {
		Text          string `json:"text"`
		ID            string `json:"id"`
		AllowChildren int    `json:"allowChildren"`
		Expandable    int    `json:"expandable"`
		Leaf          int    `json:"leaf"`
	}
	seen := make(map[string]*node)
	s.mu.RLock()
	for p := range s.metrics {
		segs := strings.Split(p, ".")
		if len(segs) < len(qs) || !segmentsMatch(qs, segs[:len(qs)]) {
			continue
		}
		id := strings.Join(segs[:len(qs)], ".")
		n, ok := seen[id]
		if !ok {
			n = &node{Text: segs[len(qs)-1], ID: id}
			seen[id] = n
		}
		if len(segs) == len(qs) {
			n.Leaf = 1
		} else {
			n.AllowChildren, n.Expandable = 1, 1
		}
	}
	s.mu.RUnlock()
	out := make([]*node, 0, len(seen))
	for _, n := range seen {
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b *node) int { return strings.Compare(a.ID, b.ID) })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
