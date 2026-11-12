// Copyright 2022 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// Modified by The Trickster Authors in 2026: reduced to query statements; type checking and evaluation removed.

package ast

// WindowFrameMode indicates which mode of framing is used.
type WindowFrameMode int

const (
	// RANGE is the mode of specifying frame in terms of logical range (e.g. 100 units cheaper).
	RANGE WindowFrameMode = iota
	// ROWS is the mode of specifying frame in terms of physical offsets (e.g. 1 row before etc).
	ROWS
	// GROUPS is the mode of specifying frame in terms of peer groups.
	GROUPS
)

func (m WindowFrameMode) String() string {
	switch m {
	case RANGE:
		return "RANGE"
	case ROWS:
		return "ROWS"
	case GROUPS:
		return "GROUPS"
	}
	return ""
}

// WindowFrameBoundType indicates which type of boundary is used.
type WindowFrameBoundType int

const (
	// UnboundedPreceding represents UNBOUNDED PRECEDING type of boundary.
	UnboundedPreceding WindowFrameBoundType = iota
	// OffsetPreceding represents 'value' PRECEDING type of boundary.
	OffsetPreceding
	// CurrentRow represents CURRENT ROW type of boundary.
	CurrentRow
	// OffsetFollowing represents 'value' FOLLOWING type of boundary.
	OffsetFollowing
	// UnboundedFollowing represents UNBOUNDED FOLLOWING type of boundary.
	UnboundedFollowing
)

// IsOffset returns true if the WindowFrameBoundType is an offset.
func (ft WindowFrameBoundType) IsOffset() bool {
	return ft == OffsetPreceding || ft == OffsetFollowing
}

func (ft WindowFrameBoundType) String() string {
	switch ft {
	case UnboundedPreceding:
		return "UNBOUNDED PRECEDING"
	case OffsetPreceding:
		return "OFFSET PRECEDING"
	case CurrentRow:
		return "CURRENT ROW"
	case OffsetFollowing:
		return "OFFSET FOLLOWING"
	case UnboundedFollowing:
		return "UNBOUNDED FOLLOWING"
	}
	return ""
}

// WindowFrameExclusion indicates which mode of exclusion is used.
type WindowFrameExclusion int

const (
	// NoExclusion represents an omitted frame exclusion clause.
	NoExclusion WindowFrameExclusion = iota
	// ExcludeCurrentRow represents EXCLUDE CURRENT ROW mode of frame exclusion.
	ExcludeCurrentRow
	// ExcludeGroup represents EXCLUDE GROUP mode of frame exclusion.
	ExcludeGroup
	// ExcludeTies represents EXCLUDE TIES mode of frame exclusion.
	ExcludeTies
)

func (node WindowFrameExclusion) String() string {
	switch node {
	case NoExclusion:
		return "EXCLUDE NO ROWS"
	case ExcludeCurrentRow:
		return "EXCLUDE CURRENT ROW"
	case ExcludeGroup:
		return "EXCLUDE GROUP"
	case ExcludeTies:
		return "EXCLUDE TIES"
	default:
		panic(assertionFailedf("unhandled case: %d", node))
	}
}

// WindowModeName returns the name of the window frame mode.
func WindowModeName(mode WindowFrameMode) string {
	switch mode {
	case RANGE:
		return "RANGE"
	case ROWS:
		return "ROWS"
	case GROUPS:
		return "GROUPS"
	default:
		panic(assertionFailedf("unhandled case: %d", mode))
	}
}
