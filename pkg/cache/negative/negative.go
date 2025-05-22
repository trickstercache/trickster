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

// Package negative defines the Negative Cache
// which is a simple lookup map of httpStatus to TTL
package negative

import (
	"fmt"
	"maps"
	"strconv"
	"time"
)

// ConfigLookup defines a Lookup map for a collection of Named Negative Cache Configs
type ConfigLookup map[string]Config

// New returns an empty Config
func New() Config {
	return Config{}
}

// Config is a collection of response codes and their TTLs
// While the status code is numeric, it's deserialized here as a string for
// maximum compatibility with templating in Helm
type Config map[string]time.Duration

// Lookup is a collection of response codes and their TTLs as Durations
type Lookup map[int]time.Duration

// Lookups is a collection of Lookup maps
type Lookups map[string]Lookup

// ErrInvalidConfig is an error type for invalid config
type ErrInvalidConfig struct {
	error
}

// NewErrInvalidConfig returns a new invalid config error
func NewErrInvalidConfig(negativeCacheName, code string) error {
	return &ErrInvalidConfig{
		error: fmt.Errorf(`invalid negative_cache config in %s: `+
			`%s is not a valid HTTP status code >= 400 and < 600`,
			negativeCacheName, code),
	}
}

// Clone returns an exact copy of a Config
func (nc Config) Clone() Config {
	return maps.Clone(nc)
}

// Get returns the named Lookup from the Lookups collection if it exists
func (l Lookups) Get(name string) Lookup {
	if v, ok := l[name]; ok {
		return v
	}
	return nil
}

// Validate verifies the Negative Cache Config
func (l ConfigLookup) ValidateAndCompile() (Lookups, error) {
	ml := make(Lookups)
	if len(l) == 0 {
		return ml, nil
	}
	for k, n := range l {
		lk := make(Lookup)
		for c, t := range n {
			ci, err := strconv.Atoi(c)
			if err != nil || ci < 400 || ci >= 600 {
				return nil, NewErrInvalidConfig(k, c)
			}
			lk[ci] = t
		}
		ml[k] = lk
	}
	return ml, nil
}
