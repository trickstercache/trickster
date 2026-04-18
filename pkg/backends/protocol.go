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

package backends

import "github.com/trickstercache/trickster/v2/pkg/proxy/connhandler"

// ConnectionHandlerProvider is an optional interface that backends may
// implement to accept raw TCP connections for non-HTTP protocols (e.g.
// ClickHouse native binary protocol, MySQL wire protocol).
type ConnectionHandlerProvider interface {
	// ConnectionHandler returns a ConnectionHandler for the given protocol
	// name, or nil if the backend does not support the requested protocol.
	ConnectionHandler(protocol string) connhandler.ConnectionHandler
}
