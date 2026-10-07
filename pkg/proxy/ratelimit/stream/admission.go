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

// Package stream enforces a rate limiter on tcp, tls and udp listeners.
package stream

import (
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
)

const planeStream = "stream"

type stage uint8

const (
	stagePeer stage = iota
	stageFlow
	stageDatagram
)

// New returns the listener limiter's admission, or nil when it has none. flowACL means an access
// list still judges tcp or tls at the flow stage, so a connection is counted there instead.
// A connection that ends before the chosen stage is not counted, including a tls name with no route.
func New(o *options.Options, protocol string, flowACL bool) l4.Admission {
	if o == nil {
		return nil
	}
	unit := o.Unit
	if unit == "" {
		unit = options.UnitConnections
		if protocol == l4.ProtocolUDP {
			unit = options.UnitSessions
		}
	}
	a := &admission{
		lim: ratelimit.Lookup(o.Name, ratelimit.Shape{
			Keys: ratelimit.EncodeKeys(o.Keys), IPv6Prefix: o.IPv6Prefix,
			Window: time.Duration(o.Window), Limit: uint32(o.Limit), MaxKeys: o.MaxKeys,
		}, ratelimit.Policy{Missing: missingOf(o.MissingKey), OnFull: onFullOf(o.MaxKeysAction)}),
		extract: extractor(o), stage: stageOf(o.KeySources, unit, flowACL),
		count: o.Action == options.ActionCount, close: o.Action == options.ActionClose,
		decisions: metrics.NewRateLimitDecision(o.Name, planeStream),
	}
	if unit == options.UnitSessions {
		return sessionAdmission{a}
	}
	return a
}

type admission struct {
	lim       *ratelimit.Limiter
	extract   func(l4.Flow) (uint64, bool)
	stage     stage
	count     bool
	close     bool
	decisions *metrics.RateLimitDecision
}

type sessionAdmission struct{ *admission }

func stageOf(keys []flowkey.KeySource, unit string, flowACL bool) stage {
	if unit == options.UnitDatagrams {
		return stageDatagram
	}
	if unit == options.UnitSessions {
		return stagePeer
	}
	// sni is known only after the handshake, and a flow-stage access list must be able to refuse first
	if flowACL || needsFlow(keys) {
		return stageFlow
	}
	return stagePeer
}

func needsFlow(keys []flowkey.KeySource) bool {
	for _, k := range keys {
		if k.Kind != flowkey.KeyClientIP && k.Kind != flowkey.KeyProxyTLV {
			return true
		}
	}
	return false
}

func (a *admission) Peer(f l4.Flow) l4.Verdict {
	if a.stage != stagePeer {
		return l4.Allow
	}
	return a.judge(f)
}

func (a *admission) Flow(f l4.Flow) l4.Verdict {
	if a.stage != stageFlow {
		return l4.Allow
	}
	return a.judge(f)
}

func (a *admission) Datagram(f l4.Flow, _ int) l4.Verdict {
	if a.stage != stageDatagram {
		return l4.Allow
	}
	return a.judge(f)
}

func (a *admission) Datagrams() bool { return a.stage == stageDatagram }

func (s sessionAdmission) Hold(f l4.Flow) time.Duration {
	key, ok := s.extract(f)
	return s.lim.Wait(key, ok, ratelimit.Now(), 1)
}

func (a *admission) judge(f l4.Flow) l4.Verdict {
	key, ok := a.extract(f)
	now := ratelimit.Now()
	var d ratelimit.Decision
	if a.count {
		d = a.lim.Count(key, ok, now, 1)
	} else {
		d = a.lim.Take(key, ok, now, 1)
	}
	if a.decisions != nil {
		a.decisions.Observe(d.Result)
	}
	note(d)
	if a.count || d.Allowed {
		return l4.Allow
	}
	if a.close {
		return l4.Drop
	}
	return l4.Reject
}

func note(d ratelimit.Decision) {
	switch d.Result {
	case ratelimit.ResultLimited, ratelimit.ResultCounted, ratelimit.ResultFull:
	default:
		return
	}
	if logger.DebugEnabled() {
		logger.Debug("rate limit decision", logging.Pairs{keys.Result: d.Result.String()})
	}
}

func extractor(o *options.Options) func(l4.Flow) (uint64, bool) {
	if len(o.KeySources) == 0 {
		return func(l4.Flow) (uint64, bool) { return 0, true }
	}
	comp := flowkey.StreamComposite(o.KeySources, o.IPv6Prefix)
	return func(f l4.Flow) (uint64, bool) {
		v := comp(f)
		return v.Hash, v.OK
	}
}

func missingOf(v string) ratelimit.MissingKey {
	if v == options.MissingShared {
		return ratelimit.MissingShared
	}
	return ratelimit.MissingExempt
}

func onFullOf(v string) ratelimit.MaxKeysAction {
	if v == options.OnFullReject {
		return ratelimit.MaxKeysReject
	}
	return ratelimit.MaxKeysAllow
}
