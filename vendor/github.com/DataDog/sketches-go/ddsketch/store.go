// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2018 Datadog, Inc.

package ddsketch

import (
	"bytes"
	"fmt"
	"reflect"
)

const (
	initialNumBins = 128
	growLeftBy     = 128
)

// Store is a dynamically growing contiguous (non-sparse) implementation of
// the buckets of DogSketch
type Store struct {
	bins       []int64
	count      int64
	minKey     int
	maxKey     int
	maxNumBins int
}

func NewStore(maxNumBins int) *Store {
	// Start with a small number of bins that will grow as needed
	// up to maxNumBins
	return &Store{
		bins:       make([]int64, initialNumBins),
		count:      0,
		minKey:     0,
		maxKey:     0,
		maxNumBins: maxNumBins,
	}
}

func (s *Store) Length() int {
	return len(s.bins)
}

func (s *Store) Add(key int) {
	if s.count == 0 {
		s.maxKey = key
		s.minKey = key - len(s.bins) + 1
	}
	if key < s.minKey {
		s.growLeft(key)
	} else if key > s.maxKey {
		s.growRight(key)
	}
	idx := key - s.minKey
	if idx < 0 {
		idx = 0
	}
	s.bins[idx]++
	s.count++
}

// Return the key for the value at rank
func (s *Store) KeyAtRank(rank int) int {
	var n int
	for i, b := range s.bins {
		n += int(b)
		if n >= rank {
			return i + s.minKey
		}
	}
	return s.maxKey
}

func (s *Store) growLeft(key int) {
	if s.minKey < key || len(s.bins) >= s.maxNumBins {
		return
	}

	var minKey int
	if s.maxKey-key >= s.maxNumBins {
		minKey = s.maxKey - s.maxNumBins + 1
	} else {
		// Expand bins to the left in chunks of growLeftBy bins.
		minKey = s.minKey
		for minKey > key {
			minKey -= growLeftBy
		}
	}
	tmpBins := make([]int64, s.maxKey-minKey+1)
	copy(tmpBins[s.minKey-minKey:], s.bins)
	s.bins = tmpBins
	s.minKey = minKey
}

func (s *Store) growRight(key int) {
	if s.maxKey > key {
		return
	}
	if key-s.maxKey >= s.maxNumBins {
		s.bins = make([]int64, s.maxNumBins)
		s.maxKey = key
		s.minKey = key - s.maxNumBins + 1
		s.bins[0] = int64(s.count)
	} else if key-s.minKey >= s.maxNumBins {
		minKey := key - s.maxNumBins + 1
		var n int64
		for i := s.minKey; i < minKey && i <= s.maxKey; i++ {
			n += s.bins[i-s.minKey]
		}
		if len(s.bins) < s.maxNumBins {
			tmpBins := make([]int64, s.maxNumBins)
			copy(tmpBins, s.bins[minKey-s.minKey:])
			s.bins = tmpBins
		} else {
			copy(s.bins, s.bins[minKey-s.minKey:])
			for i := s.maxKey - minKey + 1; i < s.maxNumBins; i++ {
				s.bins[i] = 0.0
			}
		}
		s.maxKey = key
		s.minKey = minKey
		s.bins[0] += n
	} else {
		tmpBins := make([]int64, key-s.minKey+1)
		copy(tmpBins, s.bins)
		s.bins = tmpBins
		s.maxKey = key
	}
}

func (s *Store) Merge(o *Store) {
	if o.count == 0 {
		return
	}
	if s.count == 0 {
		s.Copy(o)
		return
	}

	if s.maxKey > o.maxKey {
		if o.minKey < s.minKey {
			s.growLeft(o.minKey)
		}
		for i := max(o.minKey, s.minKey); i <= o.maxKey; i++ {
			s.bins[i-s.minKey] += o.bins[i-o.minKey]
		}
		var n int64
		for i := o.minKey; i < s.minKey; i++ {
			n += o.bins[i-o.minKey]
		}
		s.bins[0] += n
	} else {
		if o.minKey < s.minKey {
			tmpBins := make([]int64, len(o.bins))
			copy(tmpBins, o.bins)
			for i := s.minKey; i <= s.maxKey; i++ {
				tmpBins[i-o.minKey] += s.bins[i-s.minKey]
			}
			s.bins = tmpBins
			s.maxKey = o.maxKey
			s.minKey = o.minKey
		} else {
			s.growRight(o.maxKey)
			for i := o.minKey; i <= o.maxKey; i++ {
				s.bins[i-s.minKey] += o.bins[i-o.minKey]
			}
		}
	}
	s.count += o.count
}

func max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

func (s *Store) Copy(o *Store) {
	s.bins = make([]int64, len(o.bins))
	copy(s.bins, o.bins)
	s.minKey = o.minKey
	s.maxKey = o.maxKey
	s.count = o.count
}

func (s *Store) MakeCopy() *Store {
	bins := make([]int64, len(s.bins))
	copy(bins, s.bins)
	return &Store{
		bins:       bins,
		count:      s.count,
		minKey:     s.minKey,
		maxKey:     s.maxKey,
		maxNumBins: s.maxNumBins,
	}
}

func (s *Store) String() string {
	var buffer bytes.Buffer
	buffer.WriteString("{")
	for i := 0; i < len(s.bins); i++ {
		key := i + s.minKey
		buffer.WriteString(fmt.Sprintf("%d: %d, ", key, s.bins[i]))
	}
	buffer.WriteString(fmt.Sprintf(", minKey: %d, maxKey: %d}", s.minKey, s.maxKey))
	return buffer.String()
}

func (s *Store) Size() int {
	return int(reflect.TypeOf(*s).Size()) + cap(s.bins)*int(reflect.TypeOf(s.bins).Elem().Size())
}
