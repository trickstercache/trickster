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
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func obj(id uint64, payload string, props ...wire.KV) *object {
	o := &object{id: id, payload: []byte(payload)}
	if len(props) > 0 {
		o.props, _ = wire.AppendKVs(nil, props)
	}
	o.filled.Store(int64(len(o.payload)))
	return o
}

func TestCovered(t *testing.T) {
	gap := wire.KV{Type: message.PropPriorObjectIDGap, Uint: 2}
	tests := []struct {
		name   string
		objs   []*object
		lo, hi uint64
		hasEnd bool
		end    uint64
		want   bool
	}{
		{"contiguous to hi", []*object{obj(0, ""), obj(1, ""), obj(2, "")}, 0, 2, false, 0, true},
		{"hole", []*object{obj(0, ""), obj(2, "")}, 0, 2, false, 0, false},
		{"declared gap", []*object{obj(0, ""), obj(3, "", gap)}, 0, 3, false, 0, true},
		{"open end unknown", []*object{obj(0, "")}, 0, math.MaxUint64, false, 0, false},
		{"open end known", []*object{obj(0, ""), obj(1, "")}, 0, math.MaxUint64, true, 1, true},
		{"from the middle", []*object{obj(0, ""), obj(1, ""), obj(2, "")}, 1, 2, false, 0, true},
		{"short of hi", []*object{obj(0, "")}, 0, 3, false, 0, false},
		{"empty range past end", nil, 4, 6, true, 3, true},
	}
	for _, tc := range tests {
		if got := covered(tc.objs, tc.lo, tc.hi, tc.hasEnd, tc.end); got != tc.want {
			t.Errorf("%s: covered = %v", tc.name, got)
		}
	}
}

func TestRanges(t *testing.T) {
	o := obj(5, "x", wire.KV{Type: 0x40, Uint: 9})
	o.subgroup, o.priority = 2, 7
	f := func(typ message.ParamType, set uint8, lo, hi uint64) message.RangeFilter {
		return message.RangeFilter{Type: typ, SetID: set, PropertyType: 0x40, Ranges: []wire.Range{{Start: lo, End: hi}}}
	}
	tests := []struct {
		name string
		rs   []message.RangeFilter
		want bool
	}{
		{"none", nil, true},
		{"object id", []message.RangeFilter{f(message.ParamObjectIDFilter, 0, 5, 5)}, true},
		{"object id miss", []message.RangeFilter{f(message.ParamObjectIDFilter, 0, 6, 9)}, false},
		{"subgroup", []message.RangeFilter{f(message.ParamSubgroupFilter, 0, 2, 2)}, true},
		{"priority", []message.RangeFilter{f(message.ParamPriorityFilter, 0, 0, 7)}, true},
		{"property", []message.RangeFilter{f(message.ParamObjectPropertyFilter, 0, 9, 9)}, true},
		{"property miss", []message.RangeFilter{f(message.ParamObjectPropertyFilter, 0, 1, 2)}, false},
		{"and within a set", []message.RangeFilter{
			f(message.ParamObjectIDFilter, 1, 5, 5),
			f(message.ParamSubgroupFilter, 1, 3, 3),
		}, false},
		{"or across sets", []message.RangeFilter{
			f(message.ParamObjectIDFilter, 1, 9, 9),
			f(message.ParamSubgroupFilter, 200, 2, 2),
		}, true},
		{"track filters ignored", []message.RangeFilter{f(message.ParamTrackPropertyFilter, 0, 1, 1)}, true},
		{"a failed set skips its other filters", []message.RangeFilter{
			f(message.ParamSubgroupFilter, 1, 3, 3),
			f(message.ParamObjectIDFilter, 1, 5, 5),
		}, false},
		{"unknown filter type admits", []message.RangeFilter{f(0x7e, 0, 1, 1)}, true},
	}
	for _, tc := range tests {
		if got := newRanges(tc.rs).pass(o, 128); got != tc.want {
			t.Errorf("%s: pass = %v", tc.name, got)
		}
	}
	d := obj(1, "")
	d.datagram, d.defaultPrio = true, true
	if newRanges([]message.RangeFilter{f(message.ParamSubgroupFilter, 0, 0, 9)}).pass(d, 128) {
		t.Error("a datagram has no subgroup to filter on")
	}
	if !newRanges([]message.RangeFilter{f(message.ParamPriorityFilter, 0, 128, 128)}).pass(d, 128) {
		t.Error("a default priority filters as the track's")
	}
	bytesProp := message.RangeFilter{
		Type: message.ParamObjectPropertyFilter, PropertyType: 0x41,
		Ranges: []wire.Range{{Start: 0, End: 9}},
	}
	if newRanges([]message.RangeFilter{bytesProp}).pass(o, 128) {
		t.Error("a bytes-valued property never matches a range")
	}
	rs := newRanges([]message.RangeFilter{f(message.ParamObjectIDFilter, 0, 0, 1), f(message.ParamSubgroupFilter, 0, 0, 1)})
	rs = rs.update([]message.RangeFilter{
		f(message.ParamObjectIDFilter, 0, 5, 5),
		{Type: message.ParamSubgroupFilter, Remove: true, PropertyType: 0x40},
	})
	if len(rs) != 1 || rs[0].Ranges[0].Start != 5 {
		t.Errorf("updated ranges %+v", rs)
	}
}

func TestMillis(t *testing.T) {
	if millis(1500) != 1500*time.Millisecond || millis(math.MaxUint64) != math.MaxInt64 {
		t.Error("a wire millisecond count converts, and a hostile one clamps")
	}
}

func TestWindow(t *testing.T) {
	largest := wire.Location{Group: 4, Object: 2}
	w, ok := newWindow(message.LocationFilter{Type: message.FilterNextObject}, &largest)
	if !ok || w.contains(largest) || !w.contains(wire.Location{Group: 4, Object: 3}) || w.admitsGroup(3) {
		t.Errorf("next object window %+v", w)
	}
	w, _ = newWindow(message.LocationFilter{
		Type: message.FilterAbsoluteRange, StartGroup: 1, EndGroupDelta: 1,
		EndObject: 0,
	}, &largest)
	if w.contains(wire.Location{Group: 2, Object: 1}) || !w.admitsGroup(2) || w.admitsGroup(3) {
		t.Errorf("range window %+v", w)
	}
	if _, ok = newWindow(message.LocationFilter{
		Type: message.FilterAbsoluteRange, StartGroup: math.MaxUint64,
		EndGroupDelta: 1,
	}, nil); ok {
		t.Error("an overflowing end is refused")
	}
}

func TestSpillIndex(t *testing.T) {
	var x spillIndex
	for _, id := range []uint64{5, 1, 3, 3} {
		x.add(id, 0)
	}
	if !slices.Equal(x.ids, []uint64{1, 3, 5}) || !x.has(3) || x.has(2) {
		t.Errorf("index %v", x.ids)
	}
	if got := x.between(2, 5); !slices.Equal(got, []uint64{3, 5}) {
		t.Errorf("between %v", got)
	}
	x.add(5, 8)
	if last, ok := x.last(5); !ok || last != 7 {
		t.Errorf("a group stored through its end knows its last object: %d %t", last, ok)
	}
	if _, ok := x.last(1); ok {
		t.Error("a group stored short of its end has no known last object")
	}
	x.remove(3)
	x.remove(4)
	if x.has(3) {
		t.Error("removed")
	}
	if last, ok := x.last(5); !ok || last != 7 {
		t.Errorf("a removal keeps the other ends aligned: %d %t", last, ok)
	}
	for i := range uint64(spillIndexMax + 10) {
		x.add(100+i, i+1)
	}
	if len(x.ids) != spillIndexMax || len(x.ends) != spillIndexMax || x.has(1) {
		t.Errorf("the index keeps the newest %d, has %d", spillIndexMax, len(x.ids))
	}
	if last, ok := x.last(100 + spillIndexMax + 9); !ok || last != spillIndexMax+9 {
		t.Errorf("trimming keeps the ends aligned: %d %t", last, ok)
	}
}

func TestCachedGroupEncoding(t *testing.T) {
	o1 := obj(0, "abc", wire.KV{Type: 0x40, Uint: 1})
	o1.subgroup, o1.priority, o1.arrived = 2, 9, time.UnixMilli(1700000000000)
	o2 := obj(1, "")
	o2.datagram, o2.defaultPrio, o2.endOfGroup, o2.status = true, true, true, message.StatusEndOfGroup
	cg := &cachedGroup{key: "track", group: 7, from: 0, to: 1, toEnd: true, objects: []*object{o1, o2}}
	got, err := decodeGroup(cg.encode(), "track")
	if err != nil {
		t.Fatal(err)
	}
	if got.group != 7 || !got.toEnd || len(got.objects) != 2 || string(got.objects[0].payload) != "abc" ||
		got.objects[0].subgroup != 2 || got.objects[0].priority != 9 || !got.objects[1].datagram ||
		!got.objects[1].defaultPrio || !got.objects[1].endOfGroup || got.objects[1].status != message.StatusEndOfGroup ||
		!got.objects[0].arrived.Equal(o1.arrived) || !got.objects[0].complete() {
		t.Errorf("decoded %+v", got)
	}
	if _, err = decodeGroup(cg.encode(), "other"); !errors.Is(err, errSpillFormat) {
		t.Error("a group stored for another track is refused")
	}
	if _, err = decodeGroup([]byte{9}, "track"); err == nil {
		t.Error("an unknown format is refused")
	}
	if _, err = decodeGroup(cg.encode()[:20], "track"); err == nil {
		t.Error("a truncated group is refused")
	}
	if _, err = decodeGroup(cg.encode()[:11], "track"); !errors.Is(err, errSpillFormat) {
		t.Error("a group cut off before its object count is refused")
	}
	if cg.Size() <= 3 || !cg.covers(0, math.MaxUint64) {
		t.Error("size and coverage")
	}
	part := &cachedGroup{from: 2, to: 5}
	if part.covers(1, 3) || !part.covers(2, 5) || part.covers(2, 6) || part.covers(2, math.MaxUint64) {
		t.Error("partial coverage")
	}
}

func TestErrorMapping(t *testing.T) {
	pd := &publishDone{code: message.DoneTrackEnded, reason: "end"}
	re := &requestError{code: message.RequestUnauthorized, reason: "no"}
	if pd.Error() == "" || re.Error() == "" {
		t.Error("errors describe themselves")
	}
	if c, _ := doneFor(pd); c != message.DoneTrackEnded {
		t.Error("a PUBLISH_DONE passes through")
	}
	if c, _ := doneFor(re); c != message.DoneUnauthorized {
		t.Error("an authorization failure ends as unauthorized")
	}
	if c, _ := doneFor(&publishDone{code: message.DoneUpdateFailed}); c != message.DoneInternalError {
		t.Error("an upstream update failure, the relay's own, ends subscribers as internal")
	}
	if c, _ := doneFor(errMalformed); c != message.DoneMalformedTrack {
		t.Error("malformed")
	}
	if c, _ := refusalFor(errMalformed); c != message.RequestMalformedTrack {
		t.Error("malformed refusal")
	}
	if c, _ := refusalFor(context.Canceled); c != message.RequestInternalError {
		t.Error("anything else refuses as internal")
	}
	for cause, want := range map[error]bool{
		pd: false, re: false, errMalformed: false, context.Canceled: false, errors.New("reset"): true,
		&publishDone{code: message.DoneGoingAway}:         true,
		&publishDone{code: message.DoneUpdateFailed}:      true,
		&requestError{code: message.RequestInternalError}: true,
	} {
		if retryable(cause) != want {
			t.Errorf("retryable(%v) != %v", cause, want)
		}
	}
	if minNonzero(0, 3) != 3 || minNonzero(3, 0) != 3 || minNonzero(2, 3) != 2 {
		t.Error("minNonzero")
	}
	var ms *meters
	ms.object(directionIn, deliveryStream, 1)
	ms.request(kindFetch, resultOK)
	ms.subscriptions(directionUpstream, 1)
	ms.drop(dropOversize)
	ms.fetched(sourceCache, 1)
	ms.liveEdge(1)
	ms.fail(errClassCache)
	if deliveryIndex(deliveryFetch) != 2 || deliveryIndex(deliveryDatagram) != 1 || deliveryIndex("x") != 0 {
		t.Error("delivery index")
	}
}

func TestSignal(t *testing.T) {
	var s signal
	s.notify()
	ch := s.wait()
	if s.wait() != ch {
		t.Error("waiters share one channel until it fires")
	}
	s.notify()
	select {
	case <-ch:
	default:
		t.Error("notify wakes waiters")
	}
}

func TestNamespaceCounting(t *testing.T) {
	r := New(Options{}, nil, nil)
	defer r.Close()
	live := wire.Namespace{[]byte("live")}
	a := wire.Namespace{[]byte("live"), []byte("a")}
	// a listener marked running queues its events without sending them, so they can be read here
	l := &nsListener{prefix: live, running: true}
	r.addNamespaceListener(l)
	p1 := &nsPublisher{prefix: a, key: namespaceKey(a)}
	p2 := &nsPublisher{prefix: a, key: namespaceKey(a)}
	r.addNamespacePublisher(p1)
	r.addNamespacePublisher(p2)
	r.removeNamespacePublisher(p1)
	if len(l.events) != 1 || l.events[0].done {
		t.Fatalf("a namespace two sessions publish is reported once and stays: %+v", l.events)
	}
	r.removeNamespacePublisher(p2)
	r.removeNamespacePublisher(p2)
	if len(l.events) != 2 || !l.events[1].done || !l.events[1].suffix.Equal(a[1:]) {
		t.Fatalf("the last withdrawal withdraws it, once: %+v", l.events)
	}
	late := &nsListener{prefix: live, running: true}
	r.addNamespacePublisher(p1)
	r.addNamespacePublisher(p2)
	r.addNamespaceListener(late)
	if len(late.events) != 1 {
		t.Errorf("a listener added later is told each known namespace once: %+v", late.events)
	}
	r.removeNamespaceListener(late)
	r.removeNamespacePublisher(p1)
	if len(late.events) != 0 {
		t.Error("a removed listener hears nothing more")
	}
}

func TestHeldDatagramsBounded(t *testing.T) {
	s := &subscriber{t: &track{r: &Relay{}}}
	o := &object{}
	for range maxHeldDatagrams + 1 {
		if !s.holdDatagram(o, nil, 0) {
			t.Fatal("a pending subscription holds its datagrams")
		}
	}
	if len(s.dgHeld) != maxHeldDatagrams {
		t.Errorf("held %d datagrams", len(s.dgHeld))
	}
	s.state.Store(uint32(subDone))
	if s.holdDatagram(o, nil, 0) {
		t.Error("an ended subscription holds a datagram")
	}
}
