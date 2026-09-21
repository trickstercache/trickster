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

// FlowFunc supplies the flow for one level of a leaf pick. depth is how many levels are above
// it, level is the picker about to be asked, and via is the member whose payload offered it:
// nil for the outermost. It lets each level be keyed the way that level is configured; it must
// not retain its arguments. A retry may ask for the same depth more than once.
type FlowFunc func(depth int, level Picker, via *Member) Flow

// Repicker is implemented by a Picker that can offer a flow its other members, as Balancer
// does, for a caller retrying work that some members could not take.
type Repicker interface {
	// Alternatives returns the eligible members that skip does not report, the picker's choice
	// for the flow first and the others after it. It commits the flow to none of them.
	Alternatives(f Flow, skip func(*Member) bool) []*Member
	// Commit commits a flow to a member that Alternatives returned, as Pick would have.
	Commit(*Member) (Pick, bool)
}

// PickLeaf commits a flow to a member that is not itself balanced, asking a member whose
// payload is a PickerProvider to pick again among its own. It returns false when any level has
// no eligible member or the members nest deeper than MaxPickDepth; the levels already
// committed are then released without prejudice to their members, whose share is refused
// rather than passed to a sibling.
func PickLeaf(p Picker, f Flow) (LeafPick, bool) {
	return pickLeaf(p, func(int, Picker, *Member) Flow { return f })
}

// PickLeafFunc is PickLeaf with each level's flow supplied by flow.
func PickLeafFunc(p Picker, flow FlowFunc) (LeafPick, bool) {
	return pickLeaf(p, flow)
}

func pickLeaf(p Picker, flow FlowFunc) (LeafPick, bool) {
	var lp LeafPick
	var via *Member
	for lp.depth < MaxPickDepth && p != nil {
		pk, ok := p.Pick(flow(lp.depth, p, via))
		if !ok {
			break
		}
		lp.levels[lp.depth] = pk
		lp.depth++
		// a payload that offers no picker is a leaf, whether or not it could have offered one
		via, p = pk.Member(), pickerOf(pk.Member())
		if p == nil {
			return lp, true
		}
	}
	lp.Done(OutcomeCanceled)
	return LeafPick{}, false
}

func pickerOf(m *Member) Picker {
	if pp, ok := m.Value.(PickerProvider); ok {
		return pp.Picker()
	}
	return nil
}

// RepickLeafFunc is PickLeafFunc for a caller retrying work that the leaf members in failed
// could not take. No level that can offer alternatives commits to one of them, and a member
// whose own pool has no other leaf left is passed over for its siblings, so a leaf that can be
// reached is never given up on. It is not a selection path.
//
// The search is one traversal: each level is asked for its alternatives once, in its own
// order of preference, and each member is visited at most once, so the work is linear in the
// members searched however many of them turn out to have nothing to offer.
func RepickLeafFunc(p Picker, flow FlowFunc, failed ...*Member) (LeafPick, bool) {
	tried := make(map[*Member]struct{}, len(failed))
	for _, m := range failed {
		tried[m] = struct{}{}
	}
	var lp LeafPick
	if !lp.repick(p, nil, flow, func(m *Member) bool { _, ok := tried[m]; return ok }) {
		return LeafPick{}, false
	}
	return lp, true
}

// repick extends lp from p down to a leaf, backing out of any member that leads to none. On
// failure lp is left as it was found.
func (lp *LeafPick) repick(p Picker, via *Member, flow FlowFunc, skip func(*Member) bool) bool {
	if lp.depth >= MaxPickDepth {
		return false
	}
	rp, can := p.(Repicker)
	if !can {
		// a picker with no alternatives to offer is asked for an ordinary pick
		pk, ok := p.Pick(flow(lp.depth, p, via))
		return ok && lp.extend(pk, flow, skip)
	}
	for _, m := range rp.Alternatives(flow(lp.depth, p, via), skip) {
		if pk, ok := rp.Commit(m); ok && lp.extend(pk, flow, skip) {
			return true
		}
	}
	return false
}

// extend adds a committed pick to lp and follows it to a leaf, releasing it again, without
// prejudice to its member, when it leads to none
func (lp *LeafPick) extend(pk Pick, flow FlowFunc, skip func(*Member) bool) bool {
	lp.levels[lp.depth] = pk
	lp.depth++
	next := pickerOf(pk.Member())
	if next == nil || lp.repick(next, pk.Member(), flow, skip) {
		return true
	}
	lp.depth--
	lp.levels[lp.depth] = Pick{}
	pk.Done(OutcomeCanceled)
	return false
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

// Reached reports that the leaf member was reached. Only a leaf is ever blamed for a failed
// connect, so only the leaf has a run of them to end.
func (lp LeafPick) Reached() {
	if lp.depth > 0 {
		lp.levels[lp.depth-1].Reached()
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
