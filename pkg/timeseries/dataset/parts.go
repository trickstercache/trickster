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

import "github.com/trickstercache/trickster/v2/pkg/timeseries"

// rows a response adds before or after a series' own, like partial buckets, are held as more
// Segments rather than copied in; everything that reads a series reads all its Segments

// HasParts reports whether any series holds its rows in more than one Segment
func (ds *DataSet) HasParts() bool {
	if ds == nil {
		return false
	}
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s != nil && s.HasParts() {
				return true
			}
		}
	}
	return false
}

// Flat returns ds or, when a series holds its rows in more than one Segment, a view of it whose series
// hold theirs in one each
func (ds *DataSet) Flat() *DataSet {
	if !ds.HasParts() {
		return ds
	}
	out := ds.FullView()
	for _, r := range out.Results {
		for _, s := range r.SeriesList {
			if s != nil {
				s.segs = s.segs.Compact()
			}
		}
	}
	// the view's envelope leaves out what a response carries, which a reader of it still needs
	out.Status, out.ErrorType, out.Warnings = ds.Status, ds.ErrorType, ds.Warnings
	return out
}

// MergeParts is Merge, but new rows wholly before or after a series' own become Segments beside
// them; compacted, the result is Merge's. ds must be a view or the caller's own
func (ds *DataSet) MergeParts(sortPoints bool, collection ...timeseries.Timeseries) {
	if ds.Merger != nil {
		ds.Merger(sortPoints, collection...)
		return
	}
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	ds.inheritValueOperations(collection)
	ds.mergeDataSets(collection, MergeOpts{SortPoints: sortPoints, parts: true}, true)
}
