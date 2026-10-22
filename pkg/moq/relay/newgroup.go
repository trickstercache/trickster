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

package relay

import (
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
)

type updateKind uint8

const (
	updateNone updateKind = iota
	updateForward
	updateNewGroup
	updateRenew
)

type newGroupState struct {
	wanted      bool
	want        uint64
	seq         uint64
	outstanding bool
	value       uint64
	hadLargest  bool
	base        uint64
}

func (t *track) newGroupRequest(v uint64) {
	// §9.20.19: a request reaches upstream with the subscription when none is Established (held in want), else as
	// a REQUEST_UPDATE past the Largest Group, unless one at least as large is outstanding until that Group passes
	t.mu.Lock()
	if t.upState != upEstablished {
		ng := &t.newGroup
		switch {
		case !ng.wanted:
			ng.wanted, ng.want = true, v
		case v == 0 || ng.want == 0:
			ng.want = 0
		default:
			ng.want = max(ng.want, v)
		}
		ng.seq++
		t.mu.Unlock()
		return
	}
	sources := t.newGroupSourcesLocked(v)
	t.mu.Unlock()
	t.sendNewGroup(&newGroupSend{sources: sources, value: v})
}

type newGroupSend struct {
	sources []*upstreamSub
	value   uint64
}

func (t *track) newGroupSourcesLocked(v uint64) []*upstreamSub {
	ng := &t.newGroup
	if v != 0 && t.hasLargest && v <= t.largest.Group {
		return nil
	}
	if !t.props.DynamicGroups() {
		return nil
	}
	passed := t.hasLargest && (!ng.hadLargest || t.largest.Group > ng.base)
	if ng.outstanding && !passed && ng.value >= v {
		return nil
	}
	t.markNewGroupLocked(v)
	var out []*upstreamSub
	for _, u := range t.ups {
		if u.live && u.request() != nil {
			out = append(out, u)
		}
	}
	return out
}

func (t *track) markNewGroupLocked(v uint64) {
	ng := &t.newGroup
	ng.outstanding, ng.value, ng.hadLargest, ng.base = true, v, t.hasLargest, t.largest.Group
}

func (t *track) newGroupEstablishedLocked(u *upstreamSub) *newGroupSend {
	// a request sent with the subscription is outstanding from now on; one that arrived after it was sent is
	// handled as one for an Established subscription
	ng := &t.newGroup
	if !ng.wanted {
		return nil
	}
	v, late := ng.want, u.sentNG < ng.seq
	ng.wanted, ng.want = false, 0
	if !late {
		t.markNewGroupLocked(v)
		return nil
	}
	return &newGroupSend{sources: t.newGroupSourcesLocked(v), value: v}
}

func (t *track) sendNewGroup(s *newGroupSend) {
	for _, u := range s.sources {
		var p message.Params
		p.SetNewGroupRequest(s.value)
		_ = u.sendUpdate(updateNewGroup, p)
	}
}

func (u *upstreamSub) sendUpdate(kind updateKind, p message.Params) error {
	// updates to one source are queued in the order they are sent, since their answers carry no Request ID
	req := u.request()
	if req == nil {
		return errUpstream
	}
	u.updMu.Lock()
	defer u.updMu.Unlock()
	t := u.t
	t.mu.Lock()
	u.updates = append(u.updates, kind)
	t.mu.Unlock()
	if _, err := req.Update(p); err != nil {
		t.mu.Lock()
		if n := len(u.updates); n > 0 {
			u.updates = u.updates[:n-1]
		}
		t.mu.Unlock()
		return err
	}
	return nil
}

func (u *upstreamSub) extend() {
	// an empty REQUEST_UPDATE asks the upstream to extend the subscription before its EXPIRES (§9.20.16)
	_ = u.sendUpdate(updateRenew, message.Params{})
}
