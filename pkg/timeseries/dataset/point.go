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

//go:generate msgp

package dataset

import (
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// Point represents a timeseries data point
type Point struct {
	Epoch  epoch.Epoch   `msg:"epoch"`
	Size   int           `msg:"size"`
	Values []interface{} `msg:"values"`
}

// Points is a slice of type *Point
type Points []Point

// Clone returns a perfect copy of the Point
func (p *Point) Clone() Point {
	clone := Point{
		Epoch: p.Epoch,
		Size:  p.Size,
	}
	if p.Values != nil {
		clone.Values = make([]interface{}, len(p.Values))
		copy(clone.Values, p.Values)
	}
	return clone
}

// Size returns the memory utilization of the Points in bytes
func (p Points) Size() int64 {
	var c atomic.Int64
	c.Store(16)
	var wg sync.WaitGroup
	for i, pt := range p {
		wg.Add(1)
		go func(s, e int64, j int) {
			c.Add(s)
			wg.Done()
		}(int64(pt.Size), int64(pt.Epoch), i)
	}
	wg.Wait()
	return c.Load()
}

// Clone returns a perfect copy of the Points
func (p Points) Clone() Points {
	clone := make(Points, len(p))
	for i, pt := range p {
		clone[i] = pt.Clone()
	}
	return clone
}

// CloneRange returns a perfect copy of the Points, cloning only the
// points in the provided index range (upper-bound exclusive)
func (p Points) CloneRange(start, end int) Points {
	if end < start {
		return nil
	}
	size := end - start
	if size > len(p) {
		return nil
	}
	clone := make(Points, size, size+10)
	j := start
	for i := 0; i < size; i++ {
		clone[i] = p[j].Clone()
		j++
	}
	return clone
}

// Len returns the length of a slice of time series data points
func (p Points) Len() int {
	return len(p)
}

// Less returns true if i comes before j
func (p Points) Less(i, j int) bool {
	return p[i].Epoch < (p[j].Epoch)
}

// Swap modifies a slice of time series data points by swapping the values in indexes i and j
func (p Points) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

// onOrJustAfter returns the index of the element having a value of ts. if the value of ts
// is not in p, the index of the first element whose value is greater than ts is returned.
// onOrJustafter requires p to be sorted chronologically from earliest to latest epoch.
// This is a variation of justGreater found @ https://stackoverflow.com/a/56815151
func (p Points) onOrJustAfter(ts epoch.Epoch, s, e int) int {
	if s >= e {
		if p[s].Epoch < ts {
			return s + 1
		}
		return s
	}
	mid := (s + e) >> 1
	if p[mid].Epoch < ts {
		return p.onOrJustAfter(ts, mid+1, e)
	}
	return p.onOrJustAfter(ts, s, mid)
}

// onOrJustBefore returns the index of the element having a value of ts. if the value of ts
// is not in p, the index of the last element whose value is less than ts is returned.
// onOrJustBefore requires p to be sorted chronologically from earliest to latest epoch.
// This is a variation of justGreater found @ https://stackoverflow.com/a/56815151
func (p Points) onOrJustBefore(ts epoch.Epoch, s, e int) int {
	if s >= e {
		if p[s].Epoch > ts {
			return s - 1
		}
		return s
	}
	mid := (s + e) >> 1
	if p[mid].Epoch < ts {
		return p.onOrJustBefore(ts, mid+1, e)
	}
	return p.onOrJustBefore(ts, s, mid)
}
