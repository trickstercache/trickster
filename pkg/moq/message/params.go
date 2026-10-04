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
	"math"

	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

// ParamType identifies a Message Parameter
type ParamType uint64

// Message Parameter types. Each has a fixed value encoding, and an unknown type ends the session,
// since unknown parameters cannot be skipped.
const (
	ParamObjectDeliveryTimeout   ParamType = 0x02
	ParamAuthorizationToken      ParamType = 0x03
	ParamRendezvousTimeout       ParamType = 0x04
	ParamSubgroupDeliveryTimeout ParamType = 0x06
	ParamExpires                 ParamType = 0x08
	ParamLargestObject           ParamType = 0x09
	ParamFillTimeout             ParamType = 0x0a
	ParamForward                 ParamType = 0x10
	ParamSubscriberPriority      ParamType = 0x20
	ParamLocationFilter          ParamType = 0x21
	ParamGroupOrder              ParamType = 0x22
	ParamFillParameters          ParamType = 0x23
	ParamSubgroupFilter          ParamType = 0x25
	ParamObjectIDFilter          ParamType = 0x26
	ParamPriorityFilter          ParamType = 0x27
	ParamObjectPropertyFilter    ParamType = 0x28
	ParamTrackPropertyFilter     ParamType = 0x29
	ParamNewGroupRequest         ParamType = 0x32
	ParamTrackNamespacePrefix    ParamType = 0x34
	ParamIncludeProperties       ParamType = 0x35
)

// ParamTypes lists every known parameter type in ascending order, the order parameters are written in
var ParamTypes = []ParamType{
	ParamObjectDeliveryTimeout, ParamAuthorizationToken, ParamRendezvousTimeout, ParamSubgroupDeliveryTimeout,
	ParamExpires, ParamLargestObject, ParamFillTimeout, ParamForward, ParamSubscriberPriority,
	ParamLocationFilter, ParamGroupOrder, ParamFillParameters, ParamSubgroupFilter, ParamObjectIDFilter,
	ParamPriorityFilter, ParamObjectPropertyFilter, ParamTrackPropertyFilter, ParamNewGroupRequest,
	ParamTrackNamespacePrefix, ParamIncludeProperties,
}

// ParamSet is a set of parameter types
type ParamSet uint32

// Bit returns the set holding only t, or 0 for an unknown type
func Bit(t ParamType) ParamSet {
	switch t {
	case ParamObjectDeliveryTimeout:
		return 1 << 0
	case ParamAuthorizationToken:
		return 1 << 1
	case ParamRendezvousTimeout:
		return 1 << 2
	case ParamSubgroupDeliveryTimeout:
		return 1 << 3
	case ParamExpires:
		return 1 << 4
	case ParamLargestObject:
		return 1 << 5
	case ParamFillTimeout:
		return 1 << 6
	case ParamForward:
		return 1 << 7
	case ParamSubscriberPriority:
		return 1 << 8
	case ParamLocationFilter:
		return 1 << 9
	case ParamGroupOrder:
		return 1 << 10
	case ParamFillParameters:
		return 1 << 11
	case ParamSubgroupFilter:
		return 1 << 12
	case ParamObjectIDFilter:
		return 1 << 13
	case ParamPriorityFilter:
		return 1 << 14
	case ParamObjectPropertyFilter:
		return 1 << 15
	case ParamTrackPropertyFilter:
		return 1 << 16
	case ParamNewGroupRequest:
		return 1 << 17
	case ParamTrackNamespacePrefix:
		return 1 << 18
	case ParamIncludeProperties:
		return 1 << 19
	}
	return 0
}

// Of returns the set holding the given types
func Of(types ...ParamType) ParamSet {
	var s ParamSet
	for _, t := range types {
		s |= Bit(t)
	}
	return s
}

// Has reports whether t is in the set
func (s ParamSet) Has(t ParamType) bool {
	b := Bit(t)
	return b != 0 && s&b != 0
}

// Repeatable reports whether a parameter of type t may appear more than once in one message
func Repeatable(t ParamType) bool {
	switch t {
	case ParamAuthorizationToken, ParamSubgroupFilter, ParamObjectIDFilter, ParamPriorityFilter,
		ParamObjectPropertyFilter, ParamTrackPropertyFilter:
		return true
	}
	return false
}

// IsRangeFilter reports whether t is one of the Range Filter parameters
func IsRangeFilter(t ParamType) bool {
	return t >= ParamSubgroupFilter && t <= ParamTrackPropertyFilter
}

// GroupOrder is the order in which a subscription or fetch delivers groups
type GroupOrder uint8

// Group orders; 0 means unset and defers to the track's default
const (
	GroupOrderUnset      GroupOrder = 0
	GroupOrderAscending  GroupOrder = 1
	GroupOrderDescending GroupOrder = 2
)

// Valid reports whether o is ascending or descending
func (o GroupOrder) Valid() bool {
	return o == GroupOrderAscending || o == GroupOrderDescending
}

// FilterType is a Location Filter form
type FilterType uint8

// Location Filter forms, numbered as the wire types of draft -22
const (
	FilterNone                  FilterType = 0x00
	FilterRelativeStart         FilterType = 0x01
	FilterAbsoluteStart         FilterType = 0x02
	FilterAbsoluteStartGroupEnd FilterType = 0x03
	FilterAbsoluteRange         FilterType = 0x04
	FilterNextObject            FilterType = 0x05
)

// LocationFilter selects an inclusive range of Locations; which fields apply depends on Type
type LocationFilter struct {
	Type          FilterType
	StartGroup    uint64
	StartObject   uint64
	EndGroupDelta uint64
	EndObject     uint64
}

// Bounds resolves the filter against Largest Object (nil if none); an omitted end is open for a
// subscription and Largest Object for a fetch; ok is false on end group overflow
func (f LocationFilter) Bounds(largest *wire.Location, fetch bool) (start, end wire.Location,
	unbounded, ok bool,
) {
	next := wire.Location{}
	if largest != nil {
		next = wire.Location{Group: largest.Group, Object: largest.Object + 1}
	}
	switch f.Type {
	case FilterRelativeStart:
		// {Largest.Group + 1 - StartGroup, 0}, clamped into range; before any content the next group is the first
		if largest != nil {
			g := largest.Group
			switch {
			case f.StartGroup == 0 && g == math.MaxUint64:
				start.Group = math.MaxUint64
			case f.StartGroup == 0:
				start.Group = g + 1
			case f.StartGroup-1 > g:
				start.Group = 0
			default:
				start.Group = g - (f.StartGroup - 1)
			}
		}
	case FilterAbsoluteStart, FilterAbsoluteStartGroupEnd, FilterAbsoluteRange:
		start = wire.Location{Group: f.StartGroup, Object: f.StartObject}
	case FilterNextObject:
		start = next
	}
	switch f.Type {
	case FilterAbsoluteStartGroupEnd:
		if f.EndGroupDelta > math.MaxUint64-f.StartGroup {
			return start, end, false, false
		}
		end = wire.Location{Group: f.StartGroup + f.EndGroupDelta, Object: math.MaxUint64}
	case FilterAbsoluteRange:
		if f.EndGroupDelta > math.MaxUint64-f.StartGroup {
			return start, end, false, false
		}
		end = wire.Location{Group: f.StartGroup + f.EndGroupDelta, Object: f.EndObject}
	default:
		if !fetch {
			return start, end, true, true
		}
		if largest != nil {
			end = *largest
		}
	}
	return start, end, false, true
}

// RangeFilter is one Range Filter parameter. Remove is a zero-length filter, which in REQUEST_UPDATE removes the
// filter it names; otherwise SetID groups it with filters it is AND'd with.
type RangeFilter struct {
	Type         ParamType
	Remove       bool
	SetID        uint8
	PropertyType uint64
	Ranges       []wire.Range
}

// Params carries the Message Parameters of one control message or of a FILL_PARAMETERS scope. Set records which
// single-valued parameters are present; AuthTokens and RangeFilters may repeat and are present when non-empty.
type Params struct {
	Set                     ParamSet
	ObjectDeliveryTimeout   uint64
	SubgroupDeliveryTimeout uint64
	RendezvousTimeout       uint64
	Expires                 uint64
	LargestObject           wire.Location
	FillTimeout             uint64
	Forward                 bool
	SubscriberPriority      uint8
	LocationFilter          LocationFilter
	GroupOrder              GroupOrder
	Fill                    *Params
	NewGroupRequest         uint64
	TrackNamespacePrefix    wire.Namespace
	IncludeProperties       bool
	AuthTokens              []wire.Token
	RangeFilters            []RangeFilter
	// Invalid holds a problem found while decoding that fails the request rather than the session, such as a
	// Range Filter whose bounds overflow; the session answers it with REQUEST_ERROR
	Invalid *RequestFailure
}

// Has reports whether a parameter of type t is present
func (p *Params) Has(t ParamType) bool {
	switch t {
	case ParamAuthorizationToken:
		return len(p.AuthTokens) > 0
	case ParamSubgroupFilter, ParamObjectIDFilter, ParamPriorityFilter, ParamObjectPropertyFilter,
		ParamTrackPropertyFilter:
		for i := range p.RangeFilters {
			if p.RangeFilters[i].Type == t {
				return true
			}
		}
		return false
	}
	return p.Set.Has(t)
}

// Present returns the set of every parameter present, repeatable ones included
func (p *Params) Present() ParamSet {
	s := p.Set
	if len(p.AuthTokens) > 0 {
		s |= Bit(ParamAuthorizationToken)
	}
	for i := range p.RangeFilters {
		s |= Bit(p.RangeFilters[i].Type)
	}
	return s
}

// Mark records a single-valued parameter as present
func (p *Params) Mark(t ParamType) {
	p.Set |= Bit(t)
}

// SetForward sets FORWARD
func (p *Params) SetForward(v bool) {
	p.Forward = v
	p.Mark(ParamForward)
}

// SetSubscriberPriority sets SUBSCRIBER_PRIORITY
func (p *Params) SetSubscriberPriority(v uint8) {
	p.SubscriberPriority = v
	p.Mark(ParamSubscriberPriority)
}

// SetGroupOrder sets GROUP_ORDER
func (p *Params) SetGroupOrder(v GroupOrder) {
	p.GroupOrder = v
	p.Mark(ParamGroupOrder)
}

// SetLocationFilter sets LOCATION_FILTER
func (p *Params) SetLocationFilter(v LocationFilter) {
	p.LocationFilter = v
	p.Mark(ParamLocationFilter)
}

// SetExpires sets EXPIRES
func (p *Params) SetExpires(ms uint64) {
	p.Expires = ms
	p.Mark(ParamExpires)
}

// SetLargestObject sets LARGEST_OBJECT
func (p *Params) SetLargestObject(v wire.Location) {
	p.LargestObject = v
	p.Mark(ParamLargestObject)
}

// SetObjectDeliveryTimeout sets OBJECT_DELIVERY_TIMEOUT
func (p *Params) SetObjectDeliveryTimeout(ms uint64) {
	p.ObjectDeliveryTimeout = ms
	p.Mark(ParamObjectDeliveryTimeout)
}

// SetSubgroupDeliveryTimeout sets SUBGROUP_DELIVERY_TIMEOUT
func (p *Params) SetSubgroupDeliveryTimeout(ms uint64) {
	p.SubgroupDeliveryTimeout = ms
	p.Mark(ParamSubgroupDeliveryTimeout)
}

// SetRendezvousTimeout sets RENDEZVOUS_TIMEOUT
func (p *Params) SetRendezvousTimeout(ms uint64) {
	p.RendezvousTimeout = ms
	p.Mark(ParamRendezvousTimeout)
}

// SetFillTimeout sets FILL_TIMEOUT
func (p *Params) SetFillTimeout(ms uint64) {
	p.FillTimeout = ms
	p.Mark(ParamFillTimeout)
}

// SetNewGroupRequest sets NEW_GROUP_REQUEST
func (p *Params) SetNewGroupRequest(v uint64) {
	p.NewGroupRequest = v
	p.Mark(ParamNewGroupRequest)
}

// SetTrackNamespacePrefix sets TRACK_NAMESPACE_PREFIX
func (p *Params) SetTrackNamespacePrefix(ns wire.Namespace) {
	p.TrackNamespacePrefix = ns
	p.Mark(ParamTrackNamespacePrefix)
}

// SetIncludeProperties sets INCLUDE_PROPERTIES
func (p *Params) SetIncludeProperties(v bool) {
	p.IncludeProperties = v
	p.Mark(ParamIncludeProperties)
}

// SetFill sets FILL_PARAMETERS to the given scope, whose presence requests a fill fetch stream
func (p *Params) SetFill(fill *Params) {
	p.Fill = fill
	p.Mark(ParamFillParameters)
}

// SubscriberPriorityOr returns SUBSCRIBER_PRIORITY, or def when absent
func (p *Params) SubscriberPriorityOr(def uint8) uint8 {
	if p.Set.Has(ParamSubscriberPriority) {
		return p.SubscriberPriority
	}
	return def
}

// ForwardOr returns FORWARD, or def when absent
func (p *Params) ForwardOr(def bool) bool {
	if p.Set.Has(ParamForward) {
		return p.Forward
	}
	return def
}

// IncludePropertiesOr returns INCLUDE_PROPERTIES, or def when absent
func (p *Params) IncludePropertiesOr(def bool) bool {
	if p.Set.Has(ParamIncludeProperties) {
		return p.IncludeProperties
	}
	return def
}

// DefaultSubscriberPriority is used when a SUBSCRIBE or FETCH omits SUBSCRIBER_PRIORITY
const DefaultSubscriberPriority = 128

var (
	subscriptionUpdateParams = Of(ParamObjectDeliveryTimeout, ParamAuthorizationToken,
		ParamSubgroupDeliveryTimeout, ParamForward, ParamSubscriberPriority, ParamLocationFilter,
		ParamFillParameters, ParamSubgroupFilter, ParamObjectIDFilter, ParamPriorityFilter,
		ParamObjectPropertyFilter, ParamNewGroupRequest)
	rangeFilterParams = Of(ParamSubgroupFilter, ParamObjectIDFilter, ParamPriorityFilter,
		ParamObjectPropertyFilter)
	subscribeParams = subscriptionUpdateParams | Of(ParamRendezvousTimeout, ParamGroupOrder,
		ParamIncludeProperties)
	// FillParams are the parameters allowed inside a FILL_PARAMETERS scope
	FillParams = Of(ParamFillTimeout, ParamSubscriberPriority, ParamLocationFilter, ParamGroupOrder) |
		rangeFilterParams
)

// AllowedParams returns the parameters that may appear in a message of kind k. REQUEST_OK and REQUEST_UPDATE
// allow the union over the requests they can answer or update; the session checks each request's own subset.
func AllowedParams(k Kind) ParamSet {
	switch k {
	case KindSubscribe:
		return subscribeParams
	case KindSubscribeOK, KindRequestOK:
		return Of(ParamExpires, ParamLargestObject)
	case KindPublishOK:
		// draft -17's PUBLISH_OK carries the subscriber's choices for the subscription
		return subscriptionUpdateParams | Of(ParamExpires, ParamGroupOrder)
	case KindPublish:
		return Of(ParamObjectDeliveryTimeout, ParamAuthorizationToken, ParamSubgroupDeliveryTimeout,
			ParamExpires, ParamLargestObject, ParamForward, ParamSubscriberPriority, ParamLocationFilter,
			ParamGroupOrder)
	case KindRequestUpdate:
		return subscriptionUpdateParams | Of(ParamTrackPropertyFilter, ParamTrackNamespacePrefix)
	case KindPublishStateNotify:
		return Of(ParamLargestObject, ParamForward, ParamLocationFilter)
	case KindFetch:
		return Of(ParamAuthorizationToken, ParamFillTimeout, ParamSubscriberPriority, ParamLocationFilter,
			ParamGroupOrder, ParamIncludeProperties) | rangeFilterParams
	case KindTrackStatus:
		return Of(ParamAuthorizationToken, ParamIncludeProperties)
	case KindPublishNamespace, KindSubscribeNamespace:
		return Of(ParamAuthorizationToken)
	case KindSubscribeTracks:
		// the draft lists a narrower set, but also lets SUBSCRIBE_TRACKS carry any subscription parameter
		return subscribeParams | Of(ParamTrackPropertyFilter)
	}
	return 0
}
