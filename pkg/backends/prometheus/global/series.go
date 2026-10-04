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

package global

import (
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
)

// sample is a float or float-histogram sample of a series read as samples; histograms are held as
// float histograms, which the engine reads whatever their original kind
type sample struct {
	t  int64
	f  float64
	fh *histogram.FloatHistogram
}

func (s sample) T() int64                      { return s.t }
func (s sample) ST() int64                     { return 0 }
func (s sample) F() float64                    { return s.f }
func (s sample) H() *histogram.Histogram       { return nil }
func (s sample) FH() *histogram.FloatHistogram { return s.fh }

func (s sample) Type() chunkenc.ValueType {
	if s.fh != nil {
		return chunkenc.ValFloatHistogram
	}
	return chunkenc.ValFloat
}

func (s sample) Copy() chunks.Sample {
	if s.fh != nil {
		s.fh = s.fh.Copy()
	}
	return s
}

// chunkSeries is a series read as encoded chunks; chunks are in start-time order and may overlap
type chunkSeries struct {
	lset   labels.Labels
	chunks []chunkenc.Iterable
}

func (s *chunkSeries) Labels() labels.Labels { return s.lset }

func (s *chunkSeries) Iterator(it chunkenc.Iterator) chunkenc.Iterator {
	if len(s.chunks) == 1 {
		return s.chunks[0].Iterator(it)
	}
	return storage.ChainSampleIteratorFromIterables(it, s.chunks)
}
