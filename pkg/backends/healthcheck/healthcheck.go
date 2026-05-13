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
	"context"
	"maps"
	"net/http"
	"slices"
	"sync"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
)

// HealthChecker defines the Health Checker interface
type HealthChecker interface {
	// Register a health check Target
	Register(name string, description string, options *ho.Options, client *http.Client) (*Status, error)
	// Remove a health check Target
	Unregister(name string)
	// Resolve status of named Target
	Status(name string) *Status
	// Retrieve all Target statuses
	Statuses() StatusLookup
	// Shutdown the health checker
	Shutdown()
	// Listen to be notified that status updates or shutdown has occurred
	Subscribe(chan bool)
}

// Lookup is a map of named Target references
type Lookup map[string]*target

// StatusLookup is a map of named Status references
type StatusLookup map[string]*Status

type healthChecker struct {
	// guards targets, statuses, subscribers
	mtx         sync.RWMutex
	targets     Lookup
	statuses    StatusLookup
	subscribers []chan bool
}

// New returns a new HealthChecker
func New() HealthChecker {
	return &healthChecker{
		targets:     make(Lookup),
		statuses:    make(StatusLookup),
		subscribers: make([]chan bool, 0, 1),
	}
}

func (hc *healthChecker) Subscribe(ch chan bool) {
	hc.mtx.Lock()
	hc.subscribers = append(hc.subscribers, ch)
	hc.mtx.Unlock()
}

func (hc *healthChecker) Shutdown() {
	hc.mtx.RLock()
	targets := slices.Collect(maps.Values(hc.targets))
	subs := slices.Clone(hc.subscribers)
	hc.mtx.RUnlock()
	for _, t := range targets {
		t.Stop()
	}
	for _, ch := range subs {
		ch <- true
	}
}

func (hc *healthChecker) Register(name, description string, o *ho.Options,
	client *http.Client,
) (*Status, error) {
	if o == nil {
		return nil, ho.ErrNoOptionsProvided
	}
	t, err := newTarget(
		context.Background(),
		name,
		description,
		o,
		client,
	)
	if err != nil {
		return nil, err
	}
	hc.mtx.Lock()
	if t2, ok := hc.targets[name]; ok && t2 != nil {
		// synchronous stop so the old probe loop exits before the new one starts
		t2.Stop()
	}
	hc.targets[t.name] = t
	hc.statuses[t.name] = t.status
	hc.mtx.Unlock()
	if t.interval > 0 {
		t.Start(context.Background())
	}
	return t.status, nil
}

func (hc *healthChecker) Unregister(name string) {
	if name == "" {
		return
	}
	hc.mtx.Lock()
	t, ok := hc.targets[name]
	if ok && t != nil {
		delete(hc.targets, t.name)
		delete(hc.statuses, t.name)
	}
	hc.mtx.Unlock()
	if ok && t != nil {
		t.Stop()
	}
}

func (hc *healthChecker) Status(name string) *Status {
	if name == "" {
		return nil
	}
	hc.mtx.RLock()
	defer hc.mtx.RUnlock()
	if t, ok := hc.targets[name]; ok && t != nil {
		return t.status
	}
	return nil
}

func (hc *healthChecker) Statuses() StatusLookup {
	hc.mtx.RLock()
	defer hc.mtx.RUnlock()
	return maps.Clone(hc.statuses)
}
