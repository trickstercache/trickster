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
package lb

import "time"

// MaxPickDepth is how many balancers a leaf pick may pass through: a pool, and the pools
// that its members may themselves be. A member that is balanced any deeper is refused.
const MaxPickDepth = 2

// LeafPick is a selection followed through members that are themselves balanced, down to a
// member that is not. It is returned by value and reports to every level it passed through.
type LeafPick struct {
	levels [MaxPickDepth]Pick
	depth  int
}

// FlowFunc supplies the flow for one level of a leaf pick. level is the picker about to be
// asked, and via is the member whose payload offered it: nil for the outermost. It lets each
// level be keyed the way that level is configured; it must not retain its arguments.
type FlowFunc func(level Picker, via *Member) Flow

// Repicker is implemented by a Picker that can pick again while avoiding a member, as
// Balancer does.
type Repicker interface {
	Repick(Flow, *Member) (Pick, bool)
}

// PickLeaf commits a flow to a member that is not itself balanced, asking a member whose
// payload is a PickerProvider to pick again among its own. It returns false when any level has
// no eligible member or the members nest deeper than MaxPickDepth; the levels already
// committed are then released without prejudice to their members, whose share is refused
// rather than passed to a sibling.
func PickLeaf(p Picker, f Flow) (LeafPick, bool) {
	return pickLeaf(p, func(Picker, *Member) Flow { return f }, nil)
}

// PickLeafFunc is PickLeaf with each level's flow supplied by flow.
func PickLeafFunc(p Picker, flow FlowFunc) (LeafPick, bool) {
	return pickLeaf(p, flow, nil)
}

// RepickLeafFunc is PickLeafFunc for a caller retrying work that the leaf member failed
// could not take: every level that can avoids it. It is not a selection path.
func RepickLeafFunc(p Picker, flow FlowFunc, failed *Member) (LeafPick, bool) {
	return pickLeaf(p, flow, failed)
}

func pickLeaf(p Picker, flow FlowFunc, avoid *Member) (LeafPick, bool) {
	var lp LeafPick
	var via *Member
	for lp.depth < MaxPickDepth && p != nil {
		var pk Pick
		var ok bool
		if rp, can := p.(Repicker); can && avoid != nil {
			pk, ok = rp.Repick(flow(p, via), avoid)
		} else {
			pk, ok = p.Pick(flow(p, via))
		}
		if !ok {
			break
		}
		lp.levels[lp.depth] = pk
		lp.depth++
		// a payload that offers no picker is a leaf, whether or not it could have offered one
		via, p = pk.Member(), nil
		if pp, ok := via.Value.(PickerProvider); ok {
			p = pp.Picker()
		}
		if p == nil {
			return lp, true
		}
	}
	lp.Done(OutcomeCanceled)
	return LeafPick{}, false
}

// Member returns the selected leaf member, or nil for the zero LeafPick.
func (lp LeafPick) Member() *Member {
	if lp.depth == 0 {
		return nil
	}
	return lp.levels[lp.depth-1].Member()
}

// Depth returns how many balancers the pick passed through.
func (lp LeafPick) Depth() int {
	return lp.depth
}

// Level returns the pick made at one of the balancers passed through, outermost first, for a
// caller that reports to each level in its own terms; the zero Pick when out of range.
func (lp LeafPick) Level(i int) Pick {
	if i < 0 || i >= lp.depth {
		return Pick{}
	}
	return lp.levels[i]
}

// Established reports to every level that the leaf was reached and how long that took.
func (lp LeafPick) Established(d time.Duration) {
	for i := range lp.depth {
		lp.levels[i].Established(d)
	}
}

// FirstByte reports the first sign of a response to every level.
func (lp LeafPick) FirstByte() {
	for i := range lp.depth {
		lp.levels[i].FirstByte()
	}
}

// Done reports the flow's end to every level. It must be called exactly once per LeafPick.
// A failure belongs to the leaf that failed: the levels above it, whose member is a whole
// pool, are released without prejudice, so one bad member does not condemn its pool.
func (lp LeafPick) Done(o Outcome) {
	for i := range lp.depth {
		if i < lp.depth-1 && o != OutcomeOK {
			lp.levels[i].Done(OutcomeCanceled)
			continue
		}
		lp.levels[i].Done(o)
	}
}
