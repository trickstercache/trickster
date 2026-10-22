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

package session

import "github.com/trickstercache/trickster/v2/pkg/moq/message"

// RFC 9218 urgencies: lower is more urgent. Control and request streams outrank every object, and padding
// ranks below them all.
const (
	UrgencyControl int8 = 0
	UrgencyPadding int8 = 7
)

const (
	subscriberHigh = 64
	subscriberLow  = 192
	publisherLow   = 128
)

// Urgency maps subscriber and publisher priority onto urgencies 1 to 6, as three subscriber bands
// times two publisher bands, so the publisher's default of 128 still counts
func Urgency(subscriber, publisher uint8) int8 {
	band := int8(1)
	switch {
	case subscriber < subscriberHigh:
		band = 0
	case subscriber >= subscriberLow:
		band = 2
	}
	u := 1 + band*2
	if publisher >= publisherLow {
		u++
	}
	return u
}

// Incremental reports whether one urgency's streams share bandwidth: a descending subscription wants
// its newest group moving, while an ascending one finishes older groups first
func Incremental(order message.GroupOrder) bool {
	return order == message.GroupOrderDescending
}
