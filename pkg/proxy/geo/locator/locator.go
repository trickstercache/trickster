/*
 * Copyright 2026 The Trickster Authors
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

// Package locator defines the interface that geo locator providers implement.
package locator

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
)

// Locator places client addresses. It is safe for concurrent use and answers from memory.
type Locator interface {
	// Locate returns where addr is. The zero Location and a nil error mean the locator has no answer;
	// an error means the lookup failed.
	Locate(addr netip.Addr) (geo.Location, error)
	// Serves reports the location fields this locator can fill
	Serves() geo.Fields
	Close() error
}

// RequestLocator is a Locator that places an HTTP request from the request itself, not its address
type RequestLocator interface {
	LocateRequest(r *http.Request) (geo.Location, error)
}

// BuildTimer is a Locator whose data says when it was built, which a stale database is noticed by
type BuildTimer interface {
	// BuildTime returns when the loaded data was built, or the zero time when it does not say
	BuildTime() time.Time
}
