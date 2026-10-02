/*
 * Copyright 2018 The Trickster Authors
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

package timeseries

import (
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"time"
)

// StepAlignment is a bit set of step alignment modes; a resolved mode has exactly one bit set
type StepAlignment uint8

const (
	// StepAlignmentOff sends the query through the object proxy cache with its range unaltered
	StepAlignmentOff StepAlignment = 1 << iota
	// StepAlignmentTruncate floors both ends of the range to the step grid
	StepAlignmentTruncate
	// StepAlignmentDrop leaves partial buckets out of the response
	StepAlignmentDrop
	// StepAlignmentPartial fetches the partial buckets at both edges, bounded by the client's range
	StepAlignmentPartial
	// StepAlignmentPartialStart fetches a partial start bucket and truncates the end
	StepAlignmentPartialStart
	// StepAlignmentPartialEnd truncates the start and fetches a partial end bucket
	StepAlignmentPartialEnd
)

// step alignment mode names, as configured and as sent in the trickster-step-align directive
const (
	StepAlignmentNameOff          = "off"
	StepAlignmentNameTruncate     = "truncate"
	StepAlignmentNameDrop         = "drop"
	StepAlignmentNamePartial      = "partial"
	StepAlignmentNamePartialStart = "partial_start"
	StepAlignmentNamePartialEnd   = "partial_end"
)

// StepAlignmentPartialModes are the modes that fetch partial buckets
const StepAlignmentPartialModes = StepAlignmentPartial | StepAlignmentPartialStart |
	StepAlignmentPartialEnd

// StepAlignmentAll is every mode, as a bucketed query path supports
const StepAlignmentAll = StepAlignmentOff | StepAlignmentTruncate | StepAlignmentDrop |
	StepAlignmentPartialModes

// StepAlignmentOffTTL is how long an object cache keeps a response served under StepAlignmentOff,
// whose key holds the client's raw range and whose buckets may still be filling
const StepAlignmentOffTTL = time.Minute

const stepAlignmentModeCount = 6

var stepAlignmentNames = [stepAlignmentModeCount]string{
	StepAlignmentNameOff, StepAlignmentNameTruncate, StepAlignmentNameDrop,
	StepAlignmentNamePartial, StepAlignmentNamePartialStart, StepAlignmentNamePartialEnd,
}

// ErrInvalidStepAlignment is returned when a name matches no step alignment mode
var ErrInvalidStepAlignment = errors.New("invalid step alignment")

// ParseStepAlignment returns the mode named by name, ignoring case and surrounding space;
// an empty name returns the zero value, meaning no mode was chosen
func ParseStepAlignment(name string) (StepAlignment, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	for i, n := range stepAlignmentNames {
		if strings.EqualFold(name, n) {
			return 1 << i, nil
		}
	}
	return 0, fmt.Errorf("%w: %q (expected one of %s)", ErrInvalidStepAlignment, name,
		strings.Join(stepAlignmentNames[:], ", "))
}

// IsMode reports whether sa holds exactly one mode
func (sa StepAlignment) IsMode() bool {
	return sa != 0 && sa&(sa-1) == 0 && bits.TrailingZeros8(uint8(sa)) < stepAlignmentModeCount
}

// String returns a mode's name, or the names in a set separated by commas
func (sa StepAlignment) String() string {
	if sa.IsMode() {
		return stepAlignmentNames[bits.TrailingZeros8(uint8(sa))]
	}
	var b strings.Builder
	for i, n := range stepAlignmentNames {
		if sa&(1<<i) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		b.WriteString(n)
	}
	return b.String()
}

// MarshalText returns the name of the mode
func (sa StepAlignment) MarshalText() ([]byte, error) {
	return []byte(sa.String()), nil
}

// UnmarshalText sets sa to the mode named by text
func (sa *StepAlignment) UnmarshalText(text []byte) error {
	v, err := ParseStepAlignment(string(text))
	if err != nil {
		return err
	}
	*sa = v
	return nil
}

// EdgePolicy is what a step alignment mode does with a partial bucket at one edge of a range
type EdgePolicy uint8

const (
	// EdgeNone is reported for both edges of off and of an unresolved mode, which plan no edges
	EdgeNone EdgePolicy = iota
	// EdgeTruncate floors the edge to the step grid
	EdgeTruncate
	// EdgeDrop leaves the partial bucket out
	EdgeDrop
	// EdgePartial fetches the partial bucket, bounded by the client's range
	EdgePartial
)

// Edges returns the policies a mode applies to the start and end edges of a range
func (sa StepAlignment) Edges() (start, end EdgePolicy) {
	switch sa {
	case StepAlignmentTruncate:
		return EdgeTruncate, EdgeTruncate
	case StepAlignmentDrop:
		return EdgeDrop, EdgeDrop
	case StepAlignmentPartial:
		return EdgePartial, EdgePartial
	case StepAlignmentPartialStart:
		return EdgePartial, EdgeTruncate
	case StepAlignmentPartialEnd:
		return EdgeTruncate, EdgePartial
	}
	return EdgeNone, EdgeNone
}

// StepAligner reports the step alignment modes a provider supports across all of its paths,
// and the mode it uses when none is configured
type StepAligner interface {
	StepAlignments() (supported, def StepAlignment)
}

// BucketEdge names the edge of a range that a partial bucket sits on
type BucketEdge uint8

const (
	// BucketEdgeStart is the edge at the start of the range
	BucketEdgeStart BucketEdge = iota
	// BucketEdgeEnd is the edge at the end of the range
	BucketEdgeEnd
)

const (
	bucketEdgeNameStart = "start"
	bucketEdgeNameEnd   = "end"
)

// String returns the edge's name
func (e BucketEdge) String() string {
	if e == BucketEdgeEnd {
		return bucketEdgeNameEnd
	}
	return bucketEdgeNameStart
}

// ParseBucketEdge returns the edge named by name
func ParseBucketEdge(name string) (BucketEdge, bool) {
	switch name {
	case bucketEdgeNameStart:
		return BucketEdgeStart, true
	case bucketEdgeNameEnd:
		return BucketEdgeEnd, true
	}
	return 0, false
}

// PartialBucket is an edge bucket whose rows are bounded by the client's raw range
type PartialBucket struct {
	Label          time.Time  // timestamp the origin labels the bucket with
	Lower, Upper   time.Time  // raw half-open fetch bounds; a zero Upper means unbounded
	LowerExclusive bool       // the client's lower bound excludes Lower
	UpperInclusive bool       // the client's upper bound includes Upper
	Edge           BucketEdge // the edge of the range the bucket sits on
}

// RequestedRange is a query's time range as the client sent it, before any alignment
type RequestedRange struct {
	Start, End time.Time
	// StartExclusive and EndInclusive record comparators that differ from a half-open [Start, End)
	StartExclusive, EndInclusive bool
	// OpenEnded is true when the range has no upper bound, in which case End is the request time
	OpenEnded bool
}

// IsZero reports whether no range was recorded
func (r RequestedRange) IsZero() bool {
	return r.Start.IsZero() && r.End.IsZero()
}

// ResolveStepAlignment makes override, else configured, the query's mode when the query supports
// it; otherwise it keeps the default and returns the unsupported mode
func (trq *TimeRangeQuery) ResolveStepAlignment(override, configured StepAlignment) StepAlignment {
	requested := override
	if requested == 0 {
		requested = configured
	}
	if requested == 0 || requested == trq.StepAlignment {
		return 0
	}
	if !requested.IsMode() || trq.StepAlignments&requested == 0 {
		return requested
	}
	trq.StepAlignment = requested
	return 0
}

// RequestedExtent returns the client's range, or Extent when no parser recorded it
func (trq *TimeRangeQuery) RequestedExtent() Extent {
	if trq.Requested.IsZero() {
		return trq.Extent
	}
	return Extent{Start: trq.Requested.Start, End: trq.Requested.End}
}
