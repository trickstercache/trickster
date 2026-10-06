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

package ratelimit

import "time"

// Result is the metric class of one decision. It is not by itself whether the event proceeds.
type Result uint8

const (
	// ResultAllowed is an event that was under the estimate and was counted.
	ResultAllowed Result = iota
	// ResultLimited is an event the estimate refused. It was not counted.
	ResultLimited
	// ResultCounted is an event the estimate would have refused, counted once because the caller
	// asked to charge limited events too.
	ResultCounted
	// ResultExempt is an event with a missing key that the policy does not count.
	ResultExempt
	// ResultFull is a new key a full table refused to store.
	ResultFull
)

func (r Result) String() string {
	switch r {
	case ResultAllowed:
		return "allowed"
	case ResultLimited:
		return "limited"
	case ResultCounted:
		return "counted"
	case ResultExempt:
		return "exempt"
	case ResultFull:
		return "full"
	default:
		return ""
	}
}

// Decision is one judged event. RetryAfter is the precise wait until the same cost could pass;
// RetryKnown is false when the cost is above the limit and no wait will admit it. Reset is how
// long until the counts in the estimate have aged out, which policy headers advertise.
type Decision struct {
	Result     Result
	Allowed    bool
	RetryAfter time.Duration
	RetryKnown bool
	Remaining  uint32
	Reset      time.Duration
}

// RetryAfterSeconds rounds a precise wait up to whole seconds, and at least one second.
func RetryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	sec := int((d + time.Second - 1) / time.Second)
	if sec < 1 {
		return 1
	}
	return sec
}
