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
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

type window struct {
	start wire.Location
	end   wire.Location
	open  bool
}

func newWindow(f message.LocationFilter, largest *wire.Location) (window, bool) {
	start, end, open, ok := f.Bounds(largest, false)
	return window{start: start, end: end, open: open}, ok
}

func (w window) contains(l wire.Location) bool {
	return !l.Less(w.start) && (w.open || !w.end.Less(l))
}

func (w window) admitsGroup(g uint64) bool {
	return g >= w.start.Group && (w.open || g <= w.end.Group)
}

type ranges []message.RangeFilter

func (rs ranges) pass(o *object, defaultPrio uint8) bool {
	// filters sharing a SetID are AND'd and the sets are OR'd, so an object passes when every filter of some set
	// admits it; no filters at all admit everything
	if len(rs) == 0 {
		return true
	}
	var tried, failed [4]uint64
	for i := range rs {
		f := &rs[i]
		if f.Type == message.ParamTrackPropertyFilter {
			continue
		}
		bit := uint64(1) << (f.SetID & 63)
		word := f.SetID >> 6
		tried[word] |= bit
		if failed[word]&bit != 0 {
			continue
		}
		if !filterAdmits(f, o, defaultPrio) {
			failed[word] |= bit
		}
	}
	for i := range tried {
		if tried[i]&^failed[i] != 0 {
			return true
		}
	}
	return tried == [4]uint64{}
}

func filterAdmits(f *message.RangeFilter, o *object, defaultPrio uint8) bool {
	var v uint64
	switch f.Type {
	case message.ParamSubgroupFilter:
		if o.datagram {
			return false
		}
		v = o.subgroup
	case message.ParamObjectIDFilter:
		v = o.id
	case message.ParamPriorityFilter:
		v = uint64(o.priority)
		if o.defaultPrio {
			v = uint64(defaultPrio)
		}
	case message.ParamObjectPropertyFilter:
		kv, ok := o.props.Find(f.PropertyType)
		if !ok || wire.IsBytesType(f.PropertyType) {
			return false
		}
		v = kv.Uint
	default:
		return true
	}
	for _, r := range f.Ranges {
		if r.Contains(v) {
			return true
		}
	}
	return false
}

func (rs ranges) update(in []message.RangeFilter) ranges {
	// a REQUEST_UPDATE's filter replaces the one with its type, SetID and property type; zero length removes it
	out := rs
	for _, f := range in {
		kept := out[:0:0]
		for _, cur := range out {
			if cur.Type != f.Type || cur.SetID != f.SetID || cur.PropertyType != f.PropertyType {
				kept = append(kept, cur)
			}
		}
		if !f.Remove {
			kept = append(kept, f)
		}
		out = kept
	}
	return out
}

func newRanges(in []message.RangeFilter) ranges {
	var out ranges
	for _, f := range in {
		if !f.Remove && f.Type != message.ParamTrackPropertyFilter {
			out = append(out, f)
		}
	}
	return out
}
