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
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"golang.org/x/sync/errgroup"
)

//go:generate go tool msgp

// SeriesList is an ordered list of Series
type SeriesList []*Series

// Merge merges sl2 into the subject SeriesList, using sl2's authoritative order
// to adaptively reorder the existing+merged list such that it best emulates
// the fully constituted series order as it would be served by the origin.
// Merge assumes that a *Series in both lists, having the identical header hash,
// are the same series and will merge sl2[i].Points into sl.Points
func (sl SeriesList) Merge(sl2 SeriesList, sortPoints bool) SeriesList {
	if len(sl2) == 0 {
		return sl.Clone()
	}
	if len(sl) == 0 {
		return sl2.Clone()
	}
	m := make(map[Hash]*Series, len(sl)+len(sl2))
	out := make(SeriesList, len(sl)+len(sl2))
	var k int
	for _, s := range sl {
		if s == nil {
			continue
		}
		h := s.Header.CalculateHash()
		if _, ok := m[h]; ok {
			continue
		}
		out[k] = s
		m[h] = s
		k++
	}
	seen := make(sets.Set[Hash], len(sl2))
	var wg sync.WaitGroup
	for _, s := range sl2 {
		if s == nil {
			continue
		}
		h := s.Header.CalculateHash()
		if seen.Contains(h) {
			continue
		}
		seen.Set(h)
		if cs, ok := m[h]; !ok {
			// this series does not exist in sl1; add it into out
			out[k] = s
			m[h] = s
			k++
		} else {
			// series is in both sl1 and sl2; merge their points
			wg.Go(func() {
				cs.Points = MergePoints(cs.Points, s.Points, sortPoints)
				cs.PointSize = cs.Points.Size()
			})
		}
	}
	wg.Wait()
	out = out[:k]
	out.SortByTags()
	return out
}

// EqualHeader returns true if the slice elements contain identical header
// values in the identical order.
func (sl SeriesList) EqualHeader(sl2 SeriesList) bool {
	if sl2 == nil || len(sl) != len(sl2) {
		return false
	}
	for i, v := range sl {
		if v == nil && sl2[i] == nil {
			continue
		}
		if v == nil || sl2[i] == nil {
			return false
		}
		if v.Header.CalculateHash() != sl2[i].Header.CalculateHash() {
			return false
		}
	}
	return true
}

func (sl SeriesList) String() string {
	hashes := make([]string, len(sl))
	for i, v := range sl {
		hashes[i] = fmt.Sprintf("%d", v.Header.CalculateHash())
	}
	return "[" + strings.Join(hashes, ",") + "]"
}

func (sl SeriesList) Clone() SeriesList {
	out := make(SeriesList, len(sl))
	var k int
	for _, s := range sl {
		if s == nil {
			continue
		}
		out[k] = s.Clone()
		k++
	}
	return out[:k]
}

// MergeWithStrategy merges sl2 into the subject SeriesList using the specified
// MergeStrategy to combine values from series with identical headers.
// For MergeStrategyDedup, this behaves identically to Merge.
func (sl SeriesList) MergeWithStrategy(sl2 SeriesList, sortPoints bool, strategy merge.Strategy) SeriesList {
	return sl.MergeWithOpts(sl2, MergeOpts{SortPoints: sortPoints, Strategy: strategy})
}

// MergeWithOpts is the MergeOpts-aware variant of MergeWithStrategy. The
// dedup path with opts.ToleranceNanos > 0 collapses sub-step duplicates
// produced by independent shards sampling the same metric at slightly
// different timestamps.
func (sl SeriesList) MergeWithOpts(sl2 SeriesList, opts MergeOpts) SeriesList {
	if opts.Strategy == merge.StrategyDedup && opts.ToleranceNanos == 0 {
		// fast path: legacy exact-match dedup
		return sl.Merge(sl2, opts.SortPoints)
	}
	if len(sl2) == 0 {
		return sl.Clone()
	}
	if len(sl) == 0 {
		return sl2.Clone()
	}
	m := make(map[Hash]*Series, len(sl)+len(sl2))
	out := make(SeriesList, len(sl)+len(sl2))
	var k int
	for _, s := range sl {
		if s == nil {
			continue
		}
		h := s.Header.CalculateHash()
		if _, ok := m[h]; ok {
			continue
		}
		out[k] = s
		m[h] = s
		k++
	}
	seen := make(sets.Set[Hash], len(sl2))
	var wg sync.WaitGroup
	for _, s := range sl2 {
		if s == nil {
			continue
		}
		h := s.Header.CalculateHash()
		if seen.Contains(h) {
			continue
		}
		seen.Set(h)
		if cs, ok := m[h]; !ok {
			out[k] = s
			m[h] = s
			k++
		} else {
			wg.Go(func() {
				cs.Points = MergePointsWithOpts(cs.Points, s.Points, opts)
				cs.PointSize = cs.Points.Size()
			})
		}
	}
	wg.Wait()
	out = out[:k]
	out.SortByTags()
	return out
}

// mergeCollection merges several member lists while preserving the same
// member order as repeated MergeWithOpts calls. Building the series lookup and
// sorting once avoids repeating both operations for every fanout member.
func (sl SeriesList) mergeCollection(collection []SeriesList, opts MergeOpts) SeriesList {
	nonEmpty := make([]SeriesList, 0, len(collection))
	for _, next := range collection {
		if len(next) > 0 {
			nonEmpty = append(nonEmpty, next)
		}
	}
	if len(nonEmpty) == 0 {
		return sl.Clone()
	}
	if len(nonEmpty) == 1 {
		return sl.MergeWithOpts(nonEmpty[0], opts)
	}

	// Repeated merges clone the first incoming list when the receiver is
	// empty. Preserve that ownership rule before processing the remainder.
	if len(sl) == 0 {
		sl = nonEmpty[0].Clone()
		nonEmpty = nonEmpty[1:]
		if len(nonEmpty) == 0 {
			return sl
		}
	}

	total := len(sl)
	for _, next := range nonEmpty {
		total += len(next)
	}
	out := make(SeriesList, total)
	seriesByHash := make(map[Hash]*Series, total)
	var k int
	for _, s := range sl {
		if s == nil {
			continue
		}
		h := s.Header.CalculateHash()
		if _, ok := seriesByHash[h]; ok {
			continue
		}
		out[k] = s
		seriesByHash[h] = s
		k++
	}

	type mergeJob struct {
		target *Series
		series []*Series
	}
	jobs := make([]mergeJob, 0)
	jobByHash := make(map[Hash]int)
	seen := make(sets.Set[Hash])
	for _, next := range nonEmpty {
		clear(seen)
		for _, s := range next {
			if s == nil {
				continue
			}
			h := s.Header.CalculateHash()
			if seen.Contains(h) {
				continue
			}
			seen.Set(h)
			target, ok := seriesByHash[h]
			if !ok {
				out[k] = s
				seriesByHash[h] = s
				k++
				continue
			}
			jobIndex, ok := jobByHash[h]
			if !ok {
				jobIndex = len(jobs)
				jobByHash[h] = jobIndex
				jobs = append(jobs, mergeJob{target: target})
			}
			jobs[jobIndex].series = append(jobs[jobIndex].series, s)
		}
	}

	eg := errgroup.Group{}
	eg.SetLimit(runtime.GOMAXPROCS(0))
	for _, job := range jobs {
		eg.Go(func() error {
			points := job.target.Points
			for _, next := range job.series {
				points = MergePointsWithOpts(points, next.Points, opts)
				job.target.Points = points
			}
			job.target.PointSize = points.Size()
			return nil
		})
	}
	_ = eg.Wait()

	out = out[:k]
	out.SortByTags()
	return out
}

func (sl SeriesList) SortByTags() {
	if len(sl) < 2 {
		return
	}
	tagsJSON := make(map[*Series]string, len(sl))
	for _, series := range sl {
		if series != nil {
			tagsJSON[series] = series.Header.Tags.JSON()
		}
	}
	slices.SortFunc(sl, func(a, b *Series) int {
		if a == nil && b == nil {
			return 0
		}
		if a == nil {
			return 1
		}
		if b == nil {
			return -1
		}
		if c := strings.Compare(tagsJSON[a], tagsJSON[b]); c != 0 {
			return c
		}
		return strings.Compare(a.Header.Name, b.Header.Name)
	})
}

func (sl SeriesList) SortPoints() {
	eg := errgroup.Group{}
	eg.SetLimit(runtime.GOMAXPROCS(0))
	for _, s := range sl {
		eg.Go(func() error {
			slices.SortFunc(s.Points, pointCmp)
			return nil
		})
	}
	eg.Wait()
}
