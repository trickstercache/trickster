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
	"net/http"
	"time"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
)

// HealthChecker defines the Health Checker interface
type HealthChecker interface {
	Register(string, string, *ho.Options, *http.Client, interface{}) (*Status, error)
	Unregister(string)
	Status(string) *Status
	Statuses() StatusLookup
	Shutdown()
	Subscribe(chan bool)
}

// Lookup is a map of named Target references
type Lookup map[string]*target

type healthChecker struct {
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
	hc.subscribers = append(hc.subscribers, ch)
}

func (hc *healthChecker) Shutdown() {
	for _, t := range hc.targets {
		t.Stop()
	}
	for _, ch := range hc.subscribers {
		ch <- true
	}
}

func (hc *healthChecker) Register(name, description string, o *ho.Options,
	client *http.Client, logger interface{}) (*Status, error) {
	if o == nil {
		return nil, ho.ErrNoOptionsProvided
	}
	if t2, ok := hc.targets[name]; ok && t2 != nil {
		t2.Stop()
	}
	t, err := newTarget(
		context.Background(),
		name,
		description,
		o,
		client,
		logger,
	)
	if err != nil {
		return nil, err
	}
	hc.targets[t.name] = t
	if t.interval > 0 {
		t.Start()
		// wait for the health check to be fully registered
		for !t.isInLoop {
			time.Sleep(1 * time.Millisecond)
		}
	}
	hc.statuses[t.name] = t.status
	return t.status, nil
}

func (hc *healthChecker) Unregister(name string) {
	if name == "" {
		return
	}
	if t, ok := hc.targets[name]; ok && t != nil {
		t.Stop()
		delete(hc.targets, t.name)
		delete(hc.statuses, t.name)
	}
}

func (hc *healthChecker) Status(name string) *Status {
	if name == "" {
		return nil
	}
	if t, ok := hc.targets[name]; ok && t != nil {
		return t.status
	}
	return nil
}

func (hc *healthChecker) Probe(name string) *Status {
	if name == "" {
		return nil
	}
	if t, ok := hc.targets[name]; ok && t != nil {
		t.probe()
		return t.status
	}
	return nil
}

func (hc *healthChecker) Statuses() StatusLookup {
	return hc.statuses
}
