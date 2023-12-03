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

package healthcheck

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// Status maintains the Status of a Target
type Status struct {
	name         string
	description  string
	status       atomic.Int32
	detail       string
	failingSince time.Time
	subscribers  []chan bool
	mtx          sync.Mutex
	prober       func(http.ResponseWriter)
}

// StatusLookup is a map of named Status references
type StatusLookup map[string]*Status

func (s *Status) String() string {
	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintf("target: %s\nstatus: %d\n", s.name, s.status.Load()))
	if s.status.Load() < 1 {
		sb.WriteString(fmt.Sprintf("detail: %s\n", s.detail))
	}
	if s.status.Load() < 0 {
		sb.WriteString(fmt.Sprintf("since: %d", s.failingSince.Unix()))
	}
	return sb.String()
}

// Headers returns a header set indicating the Status
func (s *Status) Headers() http.Header {
	h := http.Header{}
	h.Set(headers.NameTrkHCStatus, strconv.Itoa(int(s.status.Load())))
	if s.status.Load() < 1 {
		h.Set(headers.NameTrkHCDetail, s.detail)
	}
	return h
}

// Set updates the status
func (s *Status) Set(i int32) {
	s.status.Store(i)
	for _, ch := range s.subscribers {
		ch <- i == i
	}
}

// Prober returns the Prober func
func (s *Status) Prober() func(http.ResponseWriter) {
	return s.prober
}

// Get provides the current status
func (s *Status) Get() int {
	return int(s.status.Load())
}

// Detail provides the current detail
func (s *Status) Detail() string {
	return s.detail
}

// Description provides the current detail
func (s *Status) Description() string {
	return s.description
}

// FailingSince provides the failing since time
func (s *Status) FailingSince() time.Time {
	return s.failingSince
}

// RegisterSubscriber registers a subscriber with the Status
func (s *Status) RegisterSubscriber(ch chan bool) {
	s.mtx.Lock()
	if s.subscribers == nil {
		s.subscribers = make([]chan bool, 0, 16)
	}
	s.subscribers = append(s.subscribers, ch)
	s.mtx.Unlock()
}
