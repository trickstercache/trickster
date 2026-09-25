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
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/flow"
)

// Stream returns an extractor that reads the key source from tcp, tls and udp flows.
func Stream(ks KeySource, v6Prefix int) func(flow.Flow) Value {
	return func(f flow.Flow) Value { return StreamValue(ks, v6Prefix, f) }
}

// StreamValue reads the key source from one flow, for a caller whose source varies by flow:
// the client address, never its port; the server name a tls client offered; or a PROXY
// protocol TLV. A source a flow cannot carry is never present.
func StreamValue(ks KeySource, v6Prefix int, f flow.Flow) Value {
	switch ks.Kind {
	case KeyClientIP:
		if !f.Client.IsValid() {
			return Value{}
		}
		return Value{Hash: lb.HashAddr(f.Client.Addr(), v6Prefix), OK: true}
	case KeySNI:
		if f.ServerName == "" {
			return Value{}
		}
		return Value{Hash: lb.HashFold(f.ServerName), OK: true}
	case KeyProxyTLV:
		if f.Proxy == nil {
			return Value{}
		}
		v, ok := f.Proxy.ProxyTLV(ks.TLV)
		if !ok || len(v) == 0 {
			return Value{}
		}
		return Value{Hash: lb.HashBytes(v), OK: true}
	}
	return Value{}
}

// StreamComposite returns an extractor that folds the key sources, in order, into one key. A
// source the flow lacks folds as zero, so flows lacking the same sources share a key.
func StreamComposite(sources []KeySource, v6Prefix int) func(flow.Flow) Composite {
	// the extractor owns its sources: a caller that reuses its slice cannot move keys
	sources = slices.Clone(sources)
	return func(f flow.Flow) Composite {
		var c Composite
		for _, ks := range sources {
			c.fold(StreamValue(ks, v6Prefix, f))
		}
		c.OK = len(sources) > 0 && c.Present == len(sources)
		return c
	}
}
