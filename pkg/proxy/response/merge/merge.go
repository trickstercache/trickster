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

package merge

import (
	"io"
	"net/http"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// MergeFunc is a function type that merges unmarshaled data into an accumulator
// It takes the accumulator, unmarshaled data (either timeseries.Timeseries or a type
// conforming to Mergeable[T]), and index, and returns an error
type MergeFunc func(*Accumulator, any, int) error

// RespondFunc is a function type that writes the merged result to the response writer
// It takes the response writer, request, accumulator, and status code
type RespondFunc func(http.ResponseWriter, *http.Request, *Accumulator, int)

type MergeFuncPair struct {
	Merge   MergeFunc
	Respond RespondFunc
}

type MergeFuncLookup map[string]*MergeFuncPair

// Mergeable represents types that can merge with other instances of the same type
type Mergeable[T any] interface {
	*T
	Merge(...*T)
}

// MarshallerPtr represents pointer types that can start marshaling with an envelope
type MarshallerPtr[T any] interface {
	*T
	StartMarshal(w io.Writer, httpStatus int)
}

// Accumulator is a thread-safe accumulator for merging data
// It can hold either timeseries.Timeseries or any other mergeable type
type Accumulator struct {
	mu      sync.Mutex
	tsdata  timeseries.Timeseries
	generic any // For non-timeseries mergeable types
}

// NewAccumulator returns a new Accumulator
func NewAccumulator() *Accumulator {
	return &Accumulator{}
}

// GetTSData returns the accumulated timeseries data (thread-safe)
func (a *Accumulator) GetTSData() timeseries.Timeseries {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tsdata
}

// SetTSData sets the accumulated timeseries data (thread-safe)
func (a *Accumulator) SetTSData(data timeseries.Timeseries) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tsdata = data
}

// GetGeneric returns the accumulated generic data (thread-safe)
func (a *Accumulator) GetGeneric() any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.generic
}

// SetGeneric sets the accumulated generic data (thread-safe)
func (a *Accumulator) SetGeneric(data any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.generic = data
}

// Lock locks the accumulator's mutex (for use by merge functions)
func (a *Accumulator) Lock() {
	a.mu.Lock()
}

// Unlock unlocks the accumulator's mutex (for use by merge functions)
func (a *Accumulator) Unlock() {
	a.mu.Unlock()
}

// GetGenericUnsafe returns the generic data without locking (caller must hold lock)
func (a *Accumulator) GetGenericUnsafe() any {
	return a.generic
}

// SetGenericUnsafe sets the generic data without locking (caller must hold lock)
func (a *Accumulator) SetGenericUnsafe(data any) {
	a.generic = data
}
