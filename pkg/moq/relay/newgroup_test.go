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

package relay_test

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func dynamicGroups(t *testing.T, h *harness) {
	t.Helper()
	props, err := wire.AppendKVs(nil, []wire.KV{{Type: message.PropDynamicGroups, Uint: 1}})
	if err != nil {
		t.Fatal(err)
	}
	h.o.props = props
}

func askNewGroup(t *testing.T, req *session.Request, v uint64) {
	t.Helper()
	var p message.Params
	p.SetNewGroupRequest(v)
	if _, err := req.Update(p); err != nil {
		t.Fatal(err)
	}
}

func upstreamNewGroup(t *testing.T, h *harness, want bool) (uint64, bool) {
	t.Helper()
	// waits for an upstream REQUEST_UPDATE carrying NEW_GROUP_REQUEST, skipping FORWARD updates; with want
	// false, it reports one that arrives within a short while
	limit := wait
	if !want {
		limit = 50 * time.Millisecond
	}
	deadline := time.After(limit)
	for {
		select {
		case u := <-h.o.updates:
			if u.Params.Has(message.ParamNewGroupRequest) {
				return u.Params.NewGroupRequest, true
			}
		case <-deadline:
			if want {
				t.Fatal("no NEW_GROUP_REQUEST reached the origin")
			}
			return 0, false
		}
	}
}

func TestNewGroupRequestWithSubscribe(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	dynamicGroups(t, h)
	h.o.subscribeAsks = make(chan *message.Subscribe, 4)
	var p message.Params
	p.SetNewGroupRequest(0)
	req, r := subscribe(t, h.client(), "video", p)
	subscribed(t, req)
	// §9.20.19: without an Established subscription, the request goes upstream with the SUBSCRIBE
	m := recv(t, h.o.subscribeAsks, "the upstream SUBSCRIBE")
	if !m.Params.Has(message.ParamNewGroupRequest) || m.Params.NewGroupRequest != 0 {
		t.Errorf("upstream SUBSCRIBE params %+v", m.Params)
	}
	// and is outstanding until the Largest Group increases
	askNewGroup(t, req, 0)
	if v, ok := upstreamNewGroup(t, h, false); ok {
		t.Errorf("an outstanding request was sent again: %d", v)
	}
	publishGroups(t, h, r, 0)
	askNewGroup(t, req, 0)
	if v, _ := upstreamNewGroup(t, h, true); v != 0 {
		t.Errorf("forwarded %d", v)
	}
}

func TestNewGroupRequestForwarded(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	dynamicGroups(t, h)
	req, r := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0, 1)
	askNewGroup(t, req, 2)
	if v, _ := upstreamNewGroup(t, h, true); v != 2 {
		t.Errorf("forwarded %d", v)
	}
	// one at least as large is outstanding, and one at or below the Largest Group is not sent
	askNewGroup(t, req, 2)
	askNewGroup(t, req, 1)
	other, _ := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, other)
	askNewGroup(t, other, 0)
	if v, ok := upstreamNewGroup(t, h, false); ok {
		t.Errorf("forwarded %d while one was outstanding", v)
	}
	// a new group ends the outstanding request
	publishGroups(t, h, r, 2)
	askNewGroup(t, req, 3)
	if v, _ := upstreamNewGroup(t, h, true); v != 3 {
		t.Errorf("forwarded %d", v)
	}
}

func TestNewGroupRequestNeedsDynamicGroups(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	req, r := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0)
	askNewGroup(t, req, 5)
	if v, ok := upstreamNewGroup(t, h, false); ok {
		t.Errorf("forwarded %d for a track without DYNAMIC_GROUPS", v)
	}
}

func TestUpstreamSubscriptionRenewed(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.o.subExpires = 40
	req, _ := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	// §9.20.16: the relay asks to extend before each EXPIRES elapses, with an empty REQUEST_UPDATE
	renewals := 0
	for renewals < 2 {
		u := recv(t, h.o.updates, "a renewal")
		if !u.Params.Has(message.ParamForward) {
			renewals++
		}
	}
	// FORWARD updates still follow the subscribers among the renewals
	var p message.Params
	p.SetForward(false)
	if _, err := req.Update(p); err != nil {
		t.Fatal(err)
	}
	for {
		u := recv(t, h.o.updates, "the pausing update")
		if u.Params.Has(message.ParamForward) {
			if u.Params.Forward {
				t.Fatal("expected a pause")
			}
			break
		}
	}
}

func TestNewGroupRequestsWhileSubscribing(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	dynamicGroups(t, h)
	h.o.hold = make(chan struct{})
	h.o.subscribeAsks = make(chan *message.Subscribe, 4)
	var five, zero message.Params
	five.SetNewGroupRequest(5)
	zero.SetNewGroupRequest(0)
	first, _ := subscribe(t, h.client(), "video", five)
	if m := recv(t, h.o.subscribeAsks, "the upstream SUBSCRIBE"); m.Params.NewGroupRequest != 5 {
		t.Fatalf("subscribed upstream with %d", m.Params.NewGroupRequest)
	}
	// a request arriving after the SUBSCRIBE went out is sent once the subscription is Established
	second, _ := subscribe(t, h.client(), "video", zero)
	time.Sleep(20 * time.Millisecond)
	close(h.o.hold)
	subscribed(t, first)
	subscribed(t, second)
	if v, _ := upstreamNewGroup(t, h, true); v != 0 {
		t.Errorf("forwarded %d", v)
	}
}
