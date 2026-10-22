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

package message

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func loc(g, o uint64) wire.Location {
	return wire.Location{Group: g, Object: o}
}

func TestLocationFilterBounds(t *testing.T) {
	largest := loc(10, 4)
	tests := []struct {
		name      string
		f         LocationFilter
		largest   *wire.Location
		fetch     bool
		start     wire.Location
		end       wire.Location
		open, bad bool
	}{
		{"none subscribe", LocationFilter{}, &largest, false, loc(0, 0), loc(0, 0), true, false},
		{"none fetch", LocationFilter{}, &largest, true, loc(0, 0), largest, false, false},
		{"none fetch empty track", LocationFilter{}, nil, true, loc(0, 0), loc(0, 0), false, false},
		{"next object", LocationFilter{Type: FilterNextObject}, &largest, false, loc(10, 5), loc(0, 0), true, false},
		{"next object empty track", LocationFilter{Type: FilterNextObject}, nil, false, loc(0, 0), loc(0, 0), true, false},
		{"relative next group", LocationFilter{Type: FilterRelativeStart}, &largest, false, loc(11, 0), loc(0, 0), true, false},
		{
			"relative current group",
			LocationFilter{Type: FilterRelativeStart, StartGroup: 1},
			&largest, false,
			loc(10, 0), loc(0, 0), true, false,
		},
		{
			"relative back three",
			LocationFilter{Type: FilterRelativeStart, StartGroup: 4},
			&largest, false,
			loc(7, 0), loc(0, 0), true, false,
		},
		{
			"relative clamps at zero",
			LocationFilter{Type: FilterRelativeStart, StartGroup: 50},
			&largest, false,
			loc(0, 0), loc(0, 0), true, false,
		},
		{
			"relative empty track",
			LocationFilter{Type: FilterRelativeStart, StartGroup: 1},
			nil, false,
			loc(0, 0), loc(0, 0), true, false,
		},
		{
			"absolute start fetch",
			LocationFilter{Type: FilterAbsoluteStart, StartGroup: 3, StartObject: 2},
			&largest, true,
			loc(3, 2), largest, false, false,
		},
		{
			"group end",
			LocationFilter{Type: FilterAbsoluteStartGroupEnd, StartGroup: 3, EndGroupDelta: 2},
			&largest, false,
			loc(3, 0), loc(5, math.MaxUint64), false, false,
		},
		{
			"range",
			LocationFilter{Type: FilterAbsoluteRange, StartGroup: 3, StartObject: 1, EndGroupDelta: 2, EndObject: 9},
			&largest, false, loc(3, 1), loc(5, 9), false, false,
		},
		{
			"group end overflow",
			LocationFilter{Type: FilterAbsoluteStartGroupEnd, StartGroup: math.MaxUint64, EndGroupDelta: 1},
			&largest, false, loc(math.MaxUint64, 0), loc(0, 0), false, true,
		},
		{
			"range overflow",
			LocationFilter{Type: FilterAbsoluteRange, StartGroup: math.MaxUint64, EndGroupDelta: 1},
			&largest, false, loc(math.MaxUint64, 0), loc(0, 0), false, true,
		},
	}
	for _, tc := range tests {
		start, end, open, ok := tc.f.Bounds(tc.largest, tc.fetch)
		if start != tc.start || end != tc.end || open != tc.open || ok == tc.bad {
			t.Errorf("%s: got %+v %+v open=%v ok=%v", tc.name, start, end, open, ok)
		}
	}
	top := loc(math.MaxUint64, 0)
	if start, _, _, _ := (LocationFilter{Type: FilterRelativeStart}).Bounds(&top, false); start.Group != math.MaxUint64 {
		t.Error("the next group after the largest group ID clamps")
	}
}

func TestParamsHelpers(t *testing.T) {
	var p Params
	if p.Has(ParamForward) || !p.ForwardOr(true) || p.SubscriberPriorityOr(DefaultSubscriberPriority) != 128 ||
		!p.IncludePropertiesOr(true) {
		t.Error("absent parameters take their defaults")
	}
	p.SetForward(false)
	p.SetSubscriberPriority(3)
	p.SetIncludeProperties(false)
	p.SetGroupOrder(GroupOrderAscending)
	p.SetExpires(1)
	p.SetLargestObject(loc(1, 1))
	p.SetObjectDeliveryTimeout(1)
	p.SetSubgroupDeliveryTimeout(1)
	p.SetRendezvousTimeout(1)
	p.SetFillTimeout(1)
	p.SetNewGroupRequest(1)
	p.SetTrackNamespacePrefix(wire.Namespace{[]byte("a")})
	p.SetLocationFilter(LocationFilter{Type: FilterNextObject})
	p.SetFill(&Params{})
	if p.ForwardOr(true) || p.SubscriberPriorityOr(128) != 3 || p.IncludePropertiesOr(true) {
		t.Error("present parameters override defaults")
	}
	for _, typ := range []ParamType{
		ParamForward, ParamSubscriberPriority, ParamIncludeProperties, ParamGroupOrder,
		ParamExpires, ParamLargestObject, ParamObjectDeliveryTimeout, ParamSubgroupDeliveryTimeout,
		ParamRendezvousTimeout, ParamFillTimeout, ParamNewGroupRequest, ParamTrackNamespacePrefix,
		ParamLocationFilter, ParamFillParameters,
	} {
		if !p.Has(typ) || !p.Present().Has(typ) {
			t.Errorf("%#x should be present", uint64(typ))
		}
	}
	if p.Has(ParamAuthorizationToken) || p.Has(ParamSubgroupFilter) {
		t.Error("repeatable parameters are absent until added")
	}
	p.AuthTokens = []wire.Token{{}}
	p.RangeFilters = []RangeFilter{{Type: ParamPriorityFilter}}
	if !p.Has(ParamAuthorizationToken) || !p.Has(ParamPriorityFilter) || p.Has(ParamSubgroupFilter) ||
		!p.Present().Has(ParamPriorityFilter) || !p.Present().Has(ParamAuthorizationToken) {
		t.Error("repeatable parameters are present when added")
	}
	if Bit(0x99) != 0 || Of(ParamForward, ParamExpires).Has(ParamGroupOrder) || ParamSet(0).Has(0x99) {
		t.Error("parameter sets")
	}
	for _, typ := range ParamTypes {
		if Bit(typ) == 0 {
			t.Errorf("%#x has no bit", uint64(typ))
		}
	}
	if !Repeatable(ParamTrackPropertyFilter) || Repeatable(ParamForward) || !IsRangeFilter(ParamPriorityFilter) ||
		IsRangeFilter(ParamGroupOrder) {
		t.Error("repeatable and range filter classes")
	}
	if GroupOrderUnset.Valid() || !GroupOrderDescending.Valid() {
		t.Error("group order validity")
	}
	kinds := []Kind{
		KindSubscribe, KindSubscribeOK, KindPublish, KindRequestOK, KindPublishOK, KindRequestUpdate,
		KindPublishStateNotify, KindFetch, KindTrackStatus, KindPublishNamespace, KindSubscribeNamespace,
		KindSubscribeTracks,
	}
	for _, k := range kinds {
		if AllowedParams(k) == 0 {
			t.Errorf("%s allows no parameters", k)
		}
	}
	if AllowedParams(KindNamespace) != 0 || AllowedParams(KindFetchOK) != 0 {
		t.Error("messages without parameters allow none")
	}
	if !AllowedParams(KindSubscribeTracks).Has(ParamTrackPropertyFilter) || AllowedParams(KindSubscribe).Has(ParamExpires) {
		t.Error("per-message parameter sets")
	}
}

func TestProperties(t *testing.T) {
	inner, _ := wire.AppendKVs(nil, []wire.KV{{Type: PropMaxCacheDuration, Uint: 5000}, {Type: PropDynamicGroups, Uint: 1}})
	p, _ := wire.AppendKVs(nil, []wire.KV{
		{Type: PropImmutable, Bytes: inner},
		{Type: PropDefaultPublisherPriority, Uint: 9},
		{Type: PropDefaultGroupOrder, Uint: uint64(GroupOrderDescending)},
		{Type: 0x78, Uint: 3},
	})
	props := Properties(p)
	if err := props.Validate(); err != nil {
		t.Fatal(err)
	}
	if d, ok := props.MaxCacheDuration(); !ok || d != 5000 {
		t.Error("a property inside Immutable Properties is found")
	}
	if !props.DynamicGroups() || props.PublisherPriority() != 9 || props.GroupOrder() != GroupOrderDescending {
		t.Error("track property accessors")
	}
	if _, ok := props.Find(PropPriorGroupIDGap); ok {
		t.Error("absent property found")
	}
	if _, ok := props.Find(PropImmutable); !ok {
		t.Error("Immutable Properties are found directly")
	}
	if _, ok := props.Mandatory(); ok {
		t.Error("no mandatory property present")
	}
	var empty Properties
	if empty.PublisherPriority() != DefaultPublisherPriority || empty.GroupOrder() != GroupOrderAscending ||
		empty.DynamicGroups() || empty.Validate() != nil {
		t.Error("defaults for an empty block")
	}
	if _, ok := empty.Find(PropImmutable); ok {
		t.Error("nothing is found in an empty block")
	}

	mand, _ := wire.AppendKVs(nil, []wire.KV{{Type: PropMandatoryMin + 2, Uint: 1}})
	if typ, ok := Properties(mand).Mandatory(); !ok || typ != PropMandatoryMin+2 {
		t.Error("a mandatory property is reported")
	}
	immMand, _ := wire.AppendKVs(nil, []wire.KV{{Type: PropImmutable, Bytes: mand}})
	if _, ok := Properties(immMand).Mandatory(); !ok {
		t.Error("a mandatory property inside Immutable Properties is reported")
	}

	nested, _ := wire.AppendKVs(nil, []wire.KV{{Type: PropImmutable, Bytes: immMand}})
	bad := map[string]Properties{
		"nested immutable": nested,
		"priority range":   mustKVs(t, wire.KV{Type: PropDefaultPublisherPriority, Uint: 256}),
		"group order":      mustKVs(t, wire.KV{Type: PropDefaultGroupOrder, Uint: 3}),
		"dynamic groups":   mustKVs(t, wire.KV{Type: PropDynamicGroups, Uint: 2}),
		"framing":          {0x02},
		"inner framing":    mustKVs(t, wire.KV{Type: PropImmutable, Bytes: []byte{0x02}}),
	}
	for name, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: should not validate", name)
		}
	}
	if !errors.Is(Properties(nested).Validate(), ErrNestedImmutable) {
		t.Error("nested immutable error")
	}
}

func mustKVs(t *testing.T, kvs ...wire.KV) Properties {
	t.Helper()
	b, err := wire.AppendKVs(nil, kvs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKindsAndMessages(t *testing.T) {
	msgs := []Message{
		&Setup{}, &GoAway{}, &RequestOK{}, &RequestError{}, &RequestUpdate{RequestID: 9},
		&Subscribe{RequestID: 1}, &SubscribeOK{}, &Publish{RequestID: 2}, &PublishOK{}, &PublishDone{},
		&PublishStateNotify{}, &Fetch{RequestID: 3}, &FetchOK{}, &TrackStatus{RequestID: 4},
		&PublishNamespace{RequestID: 5}, &SubscribeNamespace{RequestID: 6}, &Namespace{}, &NamespaceDone{},
		&SubscribeTracks{RequestID: 7}, &PublishSkipped{},
	}
	seen := map[Kind]bool{}
	for _, m := range msgs {
		k := m.Kind()
		if seen[k] || k.String() == "UNKNOWN" {
			t.Errorf("kind %d (%s) repeated or unnamed", k, k)
		}
		seen[k] = true
		id, ok := RequestIDOf(m)
		if ok != (k.IsRequest() || k == KindRequestUpdate) {
			t.Errorf("%s: request ID presence %v", k, ok)
		}
		if ok && id == 0 {
			t.Errorf("%s: request ID not returned", k)
		}
	}
	if Kind(0).String() != "UNKNOWN" || Kind(200).String() != "UNKNOWN" {
		t.Error("unknown kinds")
	}
	a := FullTrackName{Namespace: wire.Namespace{[]byte("live")}, Name: []byte("v")}
	b := a.Clone()
	if !a.Equal(b) {
		t.Error("a clone is equal")
	}
	b.Name[0] = 'x'
	if a.Equal(b) || a.Name[0] != 'v' {
		t.Error("a clone does not share bytes")
	}
	for _, s := range []ObjectStatus{StatusNormal, StatusEndOfGroup, StatusEndOfTrack} {
		if !s.Valid() {
			t.Errorf("%d should be valid", s)
		}
	}
	if ObjectStatus(1).Valid() {
		t.Error("status 1 is undefined")
	}
}

func TestErrors(t *testing.T) {
	se := Violation("bad")
	if se.Code != CodeProtocolViolation || !strings.Contains(se.Error(), "0x3") || !strings.Contains(se.Error(), "bad") {
		t.Error(se.Error())
	}
	rf := &RequestFailure{Code: RequestDoesNotExist, Reason: "gone"}
	if !strings.Contains(rf.Error(), "0x10") {
		t.Error(rf.Error())
	}
}
