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

package flowkey

import (
	"net/netip"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Session reads the key source from a native protocol session once it has authenticated: the
// name it authenticated as, or the address it arrived from, never its port. A source a session
// cannot carry is never present.
func Session(ks KeySource, v6Prefix int, user string, client netip.Addr) Value {
	switch ks.Kind {
	case KeyUser:
		if user == "" {
			return Value{}
		}
		return Value{Hash: lb.HashString(user), OK: true}
	case KeyClientIP:
		if !client.IsValid() {
			return Value{}
		}
		return Value{Hash: lb.HashAddr(client, v6Prefix), OK: true}
	}
	return Value{}
}
