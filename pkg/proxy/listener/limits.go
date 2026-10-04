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

package listener

import (
	"net/http"
	"time"
)

// NoIdleTimeout, as ServerLimits.IdleTimeout, keeps idle keep-alive connections open indefinitely.
const NoIdleTimeout time.Duration = -1

// ServerLimits bounds how long an HTTP server waits on a client and how large a request's
// headers may be. Zero values select the net/http defaults, and a negative IdleTimeout sets none.
type ServerLimits struct {
	// ReadHeaderTimeout bounds reading a request's line and headers.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds reading a whole request, body included.
	ReadTimeout time.Duration
	// IdleTimeout bounds the wait for the next request on a keep-alive connection.
	IdleTimeout time.Duration
	// MaxHeaderBytes caps the bytes read for a request's line and headers.
	MaxHeaderBytes int
}

func (l ServerLimits) apply(svr *http.Server) {
	svr.ReadHeaderTimeout = l.ReadHeaderTimeout
	svr.ReadTimeout = l.ReadTimeout
	svr.IdleTimeout = l.IdleTimeout
	svr.MaxHeaderBytes = l.MaxHeaderBytes
}
