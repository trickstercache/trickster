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
	"bytes"
	"encoding/json"
	"io"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const (
	// Buffer pool constants
	maxBufferSize = 65536 // 64KB - reject buffers larger than this

	// Set pool constants
	maxStringSetSize     = 10000
	maxSeriesDataSetSize = 10000

	// Alert map pool constants
	maxAlertMapSize = 1000
)

var (
	bytesBufferPool = sync.Pool{
		New: func() any {
			return &bytes.Buffer{}
		},
	}

	stringSetPool = sync.Pool{
		New: func() any {
			return sets.NewStringSet()
		},
	}

	seriesDataSetPool = sync.Pool{
		New: func() any {
			return make(sets.Set[WFSeriesData])
		},
	}

	alertMapPool = sync.Pool{
		New: func() any {
			return make(map[uint64]WFAlert)
		},
	}

	// Decoder pool for JSON unmarshaling
	decoderPool = sync.Pool{
		New: func() any {
			return &jsonDecoder{}
		},
	}
)

// jsonDecoder wraps a json.Decoder for pooling
type jsonDecoder struct {
	dec *json.Decoder
}

// getBuffer retrieves a bytes.Buffer from the pool.
// The buffer is reset and ready for use.
// Always use defer putBuffer(buf) after getting a buffer.
func getBuffer() *bytes.Buffer {
	buf := bytesBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// putBuffer returns a bytes.Buffer to the pool.
// Buffers larger than maxBufferSize are discarded to prevent memory bloat.
func putBuffer(buf *bytes.Buffer) {
	if buf == nil {
		return
	}
	// Reject oversized buffers to prevent pool bloat
	if buf.Cap() > maxBufferSize {
		return
	}
	buf.Reset()
	bytesBufferPool.Put(buf)
}

// getStringSet retrieves a sets.Set[string] from the pool.
// The set is cleared and ready for use.
// Always use defer putStringSet(m) after getting a set.
func getStringSet() sets.Set[string] {
	m := stringSetPool.Get().(sets.Set[string])
	// Clear all entries
	for k := range m {
		delete(m, k)
	}
	return m
}

// putStringSet returns a sets.Set[string] to the pool.
// Clears all entries to prevent memory retention.
// Oversized sets are discarded to prevent pool bloat.
func putStringSet(m sets.Set[string]) {
	if m == nil {
		return
	}
	// Reject oversized sets to prevent pool bloat
	if len(m) > maxStringSetSize {
		return
	}

	// Clear all entries
	for k := range m {
		delete(m, k)
	}

	stringSetPool.Put(m)
}

// getSeriesDataSet retrieves a sets.Set[WFSeriesData] from the pool.
// The set is cleared and ready for use.
// Always use defer putSeriesDataSet(m) after getting a set.
func getSeriesDataSet() sets.Set[WFSeriesData] {
	m := seriesDataSetPool.Get().(sets.Set[WFSeriesData])
	// Clear all entries
	for k := range m {
		delete(m, k)
	}
	return m
}

// putSeriesDataSet returns a sets.Set[WFSeriesData] to the pool.
// Clears all entries to prevent memory retention.
// Oversized sets are discarded to prevent pool bloat.
func putSeriesDataSet(m sets.Set[WFSeriesData]) {
	if m == nil {
		return
	}
	// Reject oversized sets to prevent pool bloat
	if len(m) > maxSeriesDataSetSize {
		return
	}

	// Clear all entries
	for k := range m {
		delete(m, k)
	}

	seriesDataSetPool.Put(m)
}

// getAlertMap retrieves a map[uint64]WFAlert from the pool.
// The map is cleared and ready for use.
// Always use defer putAlertMap(m) after getting a map.
func getAlertMap() map[uint64]WFAlert {
	m := alertMapPool.Get().(map[uint64]WFAlert)
	// Clear all entries
	for k := range m {
		delete(m, k)
	}
	return m
}

// putAlertMap returns a map[uint64]WFAlert to the pool.
// Clears all entries to prevent memory retention.
// Oversized maps are discarded to prevent pool bloat.
func putAlertMap(m map[uint64]WFAlert) {
	if m == nil {
		return
	}
	// Reject oversized maps to prevent pool bloat
	if len(m) > maxAlertMapSize {
		return
	}

	// Clear all entries
	for k := range m {
		delete(m, k)
	}

	alertMapPool.Put(m)
}

// getDecoder retrieves a json.Decoder from the pool configured for the given reader.
// The decoder is reset and ready for use.
// Always use defer putDecoder(dec) after getting a decoder.
func getDecoder(r io.Reader) *json.Decoder {
	jd := decoderPool.Get().(*jsonDecoder)
	// Create a new decoder with the provided reader
	// json.Decoder doesn't have a Reset method, so we create fresh each time
	jd.dec = json.NewDecoder(r)
	return jd.dec
}

// putDecoder returns a json.Decoder wrapper to the pool.
func putDecoder(dec *json.Decoder) {
	if dec == nil {
		return
	}
	decoderPool.Put(&jsonDecoder{dec: dec})
}
