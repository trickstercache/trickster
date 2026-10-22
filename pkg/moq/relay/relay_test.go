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
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func TestFanOut(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	const subscribers = 3
	recs := make([]*rec, subscribers)
	for i := range recs {
		req, r := subscribe(t, h.client(), "video", message.Params{})
		subscribed(t, req)
		recs[i] = r
	}
	if n := h.o.subscribes.Load(); n != 1 {
		t.Fatalf("%d subscribers made %d upstream subscriptions", subscribers, n)
	}
	var want []got
	for g := range uint64(5) {
		payloads := []string{fmt.Sprintf("key-%d", g), fmt.Sprintf("delta-%d", g), fmt.Sprintf("delta2-%d", g)}
		h.o.publish(t, "video", g, objs(payloads...)...)
		for i, p := range payloads {
			want = append(want, got{group: g, id: uint64(i), payload: p})
		}
	}
	for i, r := range recs {
		eventually(t, fmt.Sprintf("subscriber %d's objects", i), func() bool { return r.count() == len(want) })
		if gotObjs := r.received(); !slices.Equal(gotObjs, want) {
			t.Errorf("subscriber %d got %+v", i, gotObjs)
		}
	}
}

func TestFilters(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	var window message.Params
	window.SetLocationFilter(message.LocationFilter{
		Type: message.FilterAbsoluteRange, StartGroup: 2, StartObject: 1,
		EndGroupDelta: 1, EndObject: 0,
	})
	wreq, wrec := subscribe(t, h.client(), "video", window)
	subscribed(t, wreq)
	var odd message.Params
	odd.RangeFilters = []message.RangeFilter{{Type: message.ParamObjectIDFilter, Ranges: []wire.Range{{Start: 1, End: 1}}}}
	oreq, orec := subscribe(t, h.client(), "video", odd)
	subscribed(t, oreq)
	for g := range uint64(5) {
		h.o.publish(t, "video", g, objs("a", "b", "c")...)
	}
	eventually(t, "the windowed subscriber's objects", func() bool { return wrec.count() == 3 })
	want := []got{{2, 1, "b"}, {2, 2, "c"}, {3, 0, "a"}}
	if g := wrec.received(); !slices.Equal(g, want) {
		t.Errorf("windowed subscriber got %+v", g)
	}
	eventually(t, "the filtered subscriber's objects", func() bool { return orec.count() == 5 })
	for _, o := range orec.received() {
		if o.id != 1 {
			t.Errorf("object filter passed %+v", o)
		}
	}
}

func TestJoinMidGroup(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	first, frec := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, first)
	outs := h.o.stream("video", 7)
	if len(outs) != 1 {
		t.Fatalf("%d upstream streams", len(outs))
	}
	if err := writeObject(outs[0], pubObj{id: 0, payload: []byte("before")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first object", func() bool { return frec.count() == 1 })
	var next message.Params
	next.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	late, lrec := subscribe(t, h.client(), "video", next)
	if ok := subscribed(t, late); !ok.Params.Has(message.ParamLargestObject) || ok.Params.LargestObject != (wire.Location{Group: 7}) {
		t.Errorf("SUBSCRIBE_OK carries Largest Object: %+v", ok.Params)
	}
	for i, p := range []string{"after1", "after2"} {
		if err := writeObject(outs[0], pubObj{id: uint64(i + 1), payload: []byte(p)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = outs[0].Close()
	eventually(t, "the late subscriber's objects", func() bool { return lrec.count() == 2 })
	if g := lrec.received(); !slices.Equal(g, []got{{7, 1, "after1"}, {7, 2, "after2"}}) {
		t.Errorf("late subscriber got %+v", g)
	}
	eventually(t, "the first subscriber's objects", func() bool { return frec.count() == 3 })
}

func TestForwardState(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	var paused message.Params
	paused.SetForward(false)
	preq, _ := subscribe(t, h.client(), "video", paused)
	subscribed(t, preq)
	if subs := h.o.subscriptions("video"); len(subs) != 0 {
		t.Fatal("a paused subscriber subscribes upstream paused")
	}
	areq, arec := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, areq)
	if u := recv(t, h.o.updates, "resume"); !u.Params.Forward {
		t.Errorf("an active subscriber resumes upstream: %+v", u.Params)
	}
	eventually(t, "forwarding upstream", func() bool { return len(h.o.subscriptions("video")) == 1 })
	h.o.publish(t, "video", 1, objs("x")...)
	eventually(t, "the active subscriber's object", func() bool { return arec.count() == 1 })
	areq.Cancel(message.ResetCancelled)
	if u := recv(t, h.o.updates, "pause"); u.Params.Forward {
		t.Errorf("only paused subscribers left pauses upstream: %+v", u.Params)
	}

	// the paused subscriber resumes, and later pauses again
	var resume message.Params
	resume.SetForward(true)
	if _, err := preq.Update(resume); err != nil {
		t.Fatal(err)
	}
	if u := recv(t, h.o.updates, "resume again"); !u.Params.Forward {
		t.Errorf("a resumed subscriber resumes upstream: %+v", u.Params)
	}
}

func TestPrewarm(t *testing.T) {
	h := newHarness(t, relay.Options{Prewarm: true}, nil)
	var paused message.Params
	paused.SetForward(false)
	req, _ := subscribe(t, h.client(), "video", paused)
	subscribed(t, req)
	if subs := h.o.subscriptions("video"); len(subs) != 1 {
		t.Error("prewarm forwards upstream with only paused subscribers")
	}
}

func TestLinger(t *testing.T) {
	h := newHarness(t, relay.Options{Linger: 50 * time.Millisecond}, nil)
	req, _ := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	req.Cancel(message.ResetCancelled)
	again, _ := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, again)
	if n := h.o.subscribes.Load(); n != 1 {
		t.Errorf("a subscriber within the linger reuses the upstream subscription, but %d were made", n)
	}
	again.Cancel(message.ResetCancelled)
	recv(t, h.o.ended, "the upstream subscription to end after the linger")
	// a subscription the relay no longer wants is released as canceled, which blames no origin
	eventually(t, "the canceled release", func() bool {
		rel := h.up.releases()
		return len(rel) == 1 && errors.Is(rel[0], context.Canceled)
	})
}

func TestPublishDoneReachesSubscribers(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	req, r := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	h.o.publish(t, "video", 0, objs("a", "b")...)
	h.o.publish(t, "video", 1, objs("c")...)
	eventually(t, "the objects", func() bool { return r.count() == 3 })
	h.o.finish("video", message.DoneTrackEnded)
	m := recv(t, r.messages, "PUBLISH_DONE")
	done, ok := m.(*message.PublishDone)
	if !ok || done.Code != message.DoneTrackEnded || done.StreamCount != 2 {
		t.Fatalf("got %+v", m)
	}
	if err := recv(t, r.closes, "close"); err != nil {
		t.Errorf("the subscription ends cleanly: %v", err)
	}
	eventually(t, "the clean release", func() bool {
		rel := h.up.releases()
		return len(rel) == 1 && rel[0] == nil
	})
}

func TestUpstreamLossResubscribes(t *testing.T) {
	h := newHarness(t, relay.Options{ReconnectBackoff: 10 * time.Millisecond}, nil)
	req, r := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	h.o.publish(t, "video", 0, objs("before")...)
	eventually(t, "the first object", func() bool { return r.count() == 1 })
	h.up.kill()
	eventually(t, "a new upstream subscription", func() bool { return h.o.subscribes.Load() == 2 })
	// the lost session is held against its origin, so a pool of origins can pick another
	if rel := h.up.releases(); len(rel) == 0 || rel[0] == nil || errors.Is(rel[0], context.Canceled) {
		t.Errorf("releases %v", rel)
	}
	eventually(t, "forwarding upstream again", func() bool { return len(h.o.subscriptions("video")) == 1 })
	h.o.publish(t, "video", 1, objs("after")...)
	eventually(t, "the object after the loss", func() bool { return r.count() == 2 })
	if h.up.dials.Load() != 2 {
		t.Errorf("dials %d", h.up.dials.Load())
	}
	select {
	case m := <-r.messages:
		t.Errorf("the subscription survives an upstream loss, but got %+v", m)
	default:
	}
}

func TestUpstreamGoneEndsSubscribers(t *testing.T) {
	h := newHarness(t, relay.Options{ReconnectBackoff: time.Millisecond}, nil)
	req, r := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	h.up.down.Store(true)
	h.up.kill()
	m := recv(t, r.messages, "PUBLISH_DONE")
	if done, ok := m.(*message.PublishDone); !ok || done.Code != message.DoneInternalError {
		t.Errorf("got %+v", m)
	}
}

func TestRefusedUpstream(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.o.refuse["secret"] = message.RequestUnauthorized
	req, _ := subscribe(t, h.client(), "secret", message.Params{})
	m, err := req.Response(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if re, ok := m.(*message.RequestError); !ok || re.Code != message.RequestUnauthorized {
		t.Errorf("got %+v", m)
	}
	if rel := h.up.releases(); len(rel) != 1 || rel[0] != nil {
		t.Errorf("a refusal is the origin's answer, not its failure: %v", rel)
	}
	h.up.down.Store(true)
	req2, _ := subscribe(t, h.client(), "other", message.Params{})
	if m, _ = req2.Response(context.Background()); m.Kind() != message.KindRequestError {
		t.Errorf("an unreachable origin refuses: %+v", m)
	}
	nr := relay.New(relay.Options{}, nil, nil)
	defer nr.Close()
	h.relay = nr
	req3, _ := subscribe(t, h.client(), "video", message.Params{})
	if m, _ = req3.Response(context.Background()); m.(*message.RequestError).Code != message.RequestDoesNotExist {
		t.Errorf("a relay with no upstream has no publisher: %+v", m)
	}
}
