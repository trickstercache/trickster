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

// Package connhandler defines the ConnectionHandler interface used by
// protocol listeners and backends to handle raw TCP connections for non-HTTP
// protocols.
package connhandler

import (
	"context"
	"net"
)

// ConnectionHandler handles raw TCP connections for non-HTTP protocols
// such as the ClickHouse native binary protocol or MySQL wire protocol.
type ConnectionHandler interface {
	HandleConnection(ctx context.Context, conn net.Conn) error
}
