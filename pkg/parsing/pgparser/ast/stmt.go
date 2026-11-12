// Copyright 2012, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in licenses/BSD-vitess.txt.

// Portions of this file are additionally subject to the following
// license and copyright.
//
// Copyright 2015 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// This code was derived from https://github.com/youtube/vitess.

// Modified by The Trickster Authors in 2026: reduced to query statements; type checking and evaluation removed.

package ast

import (
	"fmt"
)

// Statement represents a statement.
type Statement interface {
	fmt.Stringer
	NodeFormatter
}

func (n *ParenSelect) String() string { return AsString(n) }

func (n *Select) String() string { return AsString(n) }

func (n *SelectClause) String() string { return AsString(n) }

func (n *UnionClause) String() string { return AsString(n) }

func (n *ValuesClause) String() string { return AsString(n) }
