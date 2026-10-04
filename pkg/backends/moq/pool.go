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

package moq

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
)

const maxBlamed = 4096

var errNoMember = errors.New("no origin in the backend's alb pool is available")

type pickerRef struct {
	p lb.Picker
}

type poolUpstream struct {
	alb    string
	picker atomic.Pointer[pickerRef]

	mu     sync.Mutex
	failed map[uint64]*lb.Member
}

var _ relay.Upstream = (*poolUpstream)(nil)

func newPoolUpstream(alb string) *poolUpstream {
	return &poolUpstream{alb: alb, failed: make(map[uint64]*lb.Member)}
}

func (p *poolUpstream) bind(picker lb.Picker) {
	p.picker.Store(&pickerRef{p: picker})
}

func (p *poolUpstream) Session(ctx context.Context, _ relay.Scope, track message.FullTrackName) (*session.Session,
	func(error), error,
) {
	// an origin is picked from the alb's healthy members: a strategy that keys on the flow places each track by its
	// full name, and a retry after a failure is offered the other members first
	ref := p.picker.Load()
	if ref == nil || ref.p == nil {
		return nil, nil, errNoMember
	}
	key := trackKey(track)
	flow := func(int, lb.Picker, *lb.Member) lb.Flow { return lb.Flow{Key: key, HasKey: true} }
	p.mu.Lock()
	avoid := p.failed[key]
	p.mu.Unlock()
	var pk lb.LeafPick
	ok := false
	if avoid != nil {
		pk, ok = lb.RepickLeafFunc(ref.p, flow, avoid)
	}
	if !ok {
		if pk, ok = lb.PickLeafFunc(ref.p, flow); !ok {
			return nil, nil, errNoMember
		}
	}
	member := pk.Member()
	c := memberClient(member)
	if c == nil || c.sh == nil || c.sh.dialer == nil {
		pk.Done(lb.OutcomeCanceled)
		return nil, nil, errNoMember
	}
	sess, err := c.sh.dialer.session(ctx)
	if err != nil {
		p.blame(key, member)
		pk.Done(lb.OutcomeConnectFailed)
		return nil, nil, err
	}
	var once sync.Once
	return sess, func(err error) {
		once.Do(func() {
			switch {
			case err == nil:
				p.forgive(key)
				pk.Done(lb.OutcomeOK)
			case errors.Is(err, context.Canceled):
				pk.Done(lb.OutcomeCanceled)
			default:
				p.blame(key, member)
				pk.Done(lb.OutcomeFailed)
			}
		})
	}, nil
}

func (p *poolUpstream) blame(key uint64, m *lb.Member) {
	p.mu.Lock()
	if len(p.failed) >= maxBlamed {
		clear(p.failed)
	}
	p.failed[key] = m
	p.mu.Unlock()
}

func (p *poolUpstream) forgive(key uint64) {
	p.mu.Lock()
	delete(p.failed, key)
	p.mu.Unlock()
}

func memberClient(m *lb.Member) *Client {
	if m == nil {
		return nil
	}
	t, ok := m.Value.(*pool.Target)
	if !ok {
		return nil
	}
	c, _ := t.Backend().(*Client)
	return c
}

func trackKey(t message.FullTrackName) uint64 {
	// each field is length-prefixed, since fields may hold any bytes
	n := binary.MaxVarintLen64 * (len(t.Namespace) + 1)
	for _, f := range t.Namespace {
		n += len(f)
	}
	b := make([]byte, 0, n+len(t.Name))
	for _, f := range t.Namespace {
		b = append(binary.AppendUvarint(b, uint64(len(f))), f...)
	}
	b = append(binary.AppendUvarint(b, uint64(len(t.Name))), t.Name...)
	return lb.HashBytes(b)
}
