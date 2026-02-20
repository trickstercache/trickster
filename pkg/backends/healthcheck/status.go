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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const (
	StatusInitializing int32 = -2
	StatusFailing      int32 = -1
	StatusUnchecked    int32 = 0
	StatusPassing      int32 = 1
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

func NewStatus(
	name string,
	description,
	detail string,
	status int32,
	failingSince time.Time,
	prober func(http.ResponseWriter),
) *Status {
	s := &Status{
		name:         name,
		description:  description,
		detail:       detail,
		prober:       prober,
		failingSince: failingSince,
	}
	s.status.Store(status)
	return s
}

func (s *Status) String() string {
	var sb strings.Builder
	st := s.status.Load()
	fmt.Fprintf(&sb, "target: %s\nstatus: %d\n", s.name, st)
	if st < StatusPassing {
		fmt.Fprintf(&sb, "detail: %s\n", s.Detail())
	}
	if st == StatusFailing {
		fmt.Fprintf(&sb, "since: %d", s.failingSince.Unix())
	}
	return sb.String()
}

// Headers returns a header set indicating the Status
func (s *Status) Headers() http.Header {
	h := http.Header{}
	st := s.status.Load()
	h.Set(headers.NameTrkHCStatus, strconv.Itoa(int(st)))
	if st < StatusPassing {
		h.Set(headers.NameTrkHCDetail, s.Detail())
	}
	return h
}

// Set updates the status
func (s *Status) Set(i int32) {
	s.status.Store(i)
	s.mtx.Lock()
	subs := slices.Clone(s.subscribers)
	s.mtx.Unlock()
	for _, ch := range subs {
		ch <- true
	}
}

// Prober returns the Prober func
func (s *Status) Prober() func(http.ResponseWriter) {
	return s.prober
}

// Get provides the current status
func (s *Status) Get() int32 {
	return s.status.Load()
}

// Detail provides the current detail
func (s *Status) Detail() string {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.detail
}

// SetDetail sets the current detail
func (s *Status) SetDetail(d string) {
	s.mtx.Lock()
	s.detail = d
	s.mtx.Unlock()
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
