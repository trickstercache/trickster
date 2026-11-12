// Copyright 2016 The Cockroach Authors.
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

import (
	"fmt"
)

// PlaceholderIdx is the 0-based index of a placeholder. Placeholder "$1"
// has PlaceholderIdx=0.
type PlaceholderIdx uint16

// String returns the index as a placeholder string representation ($1, $2 etc).
func (idx PlaceholderIdx) String() string {
	return fmt.Sprintf("$%d", idx+1)
}
