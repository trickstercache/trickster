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

// DeliveryMode is how an Object travels, fixed by how the Original Publisher first sent it
type DeliveryMode uint8

// Delivery modes
const (
	DeliverySubgroup DeliveryMode = iota
	DeliveryDatagram
)

// ObjectStatus says whether an Object holds data or marks the end of its group or track
type ObjectStatus uint64

// Object statuses; only Normal permits a payload
const (
	StatusNormal     ObjectStatus = 0x0
	StatusEndOfGroup ObjectStatus = 0x3
	StatusEndOfTrack ObjectStatus = 0x4
)

// Valid reports whether s is a defined status
func (s ObjectStatus) Valid() bool {
	return s == StatusNormal || s == StatusEndOfGroup || s == StatusEndOfTrack
}

// SubgroupIDMode is how a SUBGROUP_HEADER conveys its Subgroup ID
type SubgroupIDMode uint8

// Subgroup ID modes
const (
	// SubgroupIDZero omits the field; the Subgroup ID is 0
	SubgroupIDZero SubgroupIDMode = 0
	// SubgroupIDFirstObject omits the field; the Subgroup ID is the first Object's ID
	SubgroupIDFirstObject SubgroupIDMode = 1
	// SubgroupIDExplicit carries the field
	SubgroupIDExplicit SubgroupIDMode = 2
)

// SubgroupHeader begins a stream carrying one subgroup's objects
type SubgroupHeader struct {
	TrackAlias uint64
	Group      uint64
	// Subgroup is known from the header unless Mode is SubgroupIDFirstObject, when the first object sets it
	Subgroup uint64
	Mode     SubgroupIDMode
	Priority uint8
	// DefaultPriority omits Priority, which the subscription's publisher priority then supplies
	DefaultPriority bool
	// HasProperties gives every object on the stream a Properties field
	HasProperties bool
	// EndOfGroup says the subgroup holds the group's largest object, so a FIN ends the group
	EndOfGroup bool
	// FirstObject says the stream begins with the first object ever published in the subgroup
	FirstObject bool
}

// SubgroupObject is the header of one object on a subgroup stream; PayloadLen bytes of payload follow it
type SubgroupObject struct {
	ID         uint64
	Properties Properties
	Status     ObjectStatus
	PayloadLen uint64
}

// Datagram carries a single object
type Datagram struct {
	TrackAlias      uint64
	Group           uint64
	Object          uint64
	Priority        uint8
	DefaultPriority bool
	EndOfGroup      bool
	Properties      Properties
	// HasStatus carries Status in place of a payload
	HasStatus bool
	Status    ObjectStatus
	Payload   []byte
}

// RangeEnd marks a span of a fetch response that holds no serialized objects
type RangeEnd uint8

// End of Range kinds
const (
	RangeEndNone RangeEnd = iota
	RangeEndNonExistent
	RangeEndUnknown
	RangeEndTimedOut
)

// FetchObject is one entry on a fetch stream: an object, whose PayloadLen bytes of payload follow it, or an
// End of Range marker naming the last Location of a span that is non-existent, unknown or timed out
type FetchObject struct {
	EndOfRange RangeEnd
	Group      uint64
	Subgroup   uint64
	Object     uint64
	// Datagram marks an object delivered in datagrams, which has no Subgroup ID
	Datagram   bool
	Priority   uint8
	Properties Properties
	PayloadLen uint64
}
