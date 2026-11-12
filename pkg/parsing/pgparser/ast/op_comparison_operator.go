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

import (
	"fmt"
)

// ComparisonOperator represents a binary operator which returns a bool.
type ComparisonOperator struct {
	Symbol ComparisonOperatorSymbol
	// IsExplicitOperator is true if OPERATOR(symbol) is used.
	IsExplicitOperator bool
}

// MakeComparisonOperator creates a ComparisonOperator given a symbol.
func MakeComparisonOperator(symbol ComparisonOperatorSymbol) ComparisonOperator {
	return ComparisonOperator{Symbol: symbol}
}

func (o ComparisonOperator) String() string {
	if o.IsExplicitOperator {
		return fmt.Sprintf("OPERATOR(%s)", o.Symbol.String())
	}
	return o.Symbol.String()
}

// Operator implements tree.Operator.
func (ComparisonOperator) Operator() {}

// ComparisonOperatorSymbol represents a comparison operator symbol.
type ComparisonOperatorSymbol int

// ComparisonExpr.Operator
const (
	EQ ComparisonOperatorSymbol = iota
	LT
	GT
	LE
	GE
	NE
	In
	NotIn
	Like
	NotLike
	ILike
	NotILike
	SimilarTo
	NotSimilarTo
	RegMatch
	NotRegMatch
	RegIMatch
	NotRegIMatch
	IsDistinctFrom
	IsNotDistinctFrom
	Contains
	ContainedBy
	JSONExists
	JSONSomeExists
	JSONAllExists
	Overlaps
	TSMatches

	// The following operators will always be used with an associated SubOperator.
	// If Go had algebraic data types they would be defined in a self-contained
	// manner like:
	//
	// Any(ComparisonOperator)
	// Some(ComparisonOperator)
	// ...
	//
	// where the internal ComparisonOperator qualifies the behavior of the primary
	// operator. Instead, a secondary ComparisonOperator is optionally included in
	// ComparisonExpr for the cases where these operators are the primary op.
	//
	// ComparisonOperator.HasSubOperator returns true for ops in this group.
	Any
	Some
	All

	NumComparisonOperatorSymbols
)

var _ = NumComparisonOperatorSymbols

var comparisonOpName = [...]string{
	EQ:                "=",
	LT:                "<",
	GT:                ">",
	LE:                "<=",
	GE:                ">=",
	NE:                "!=",
	In:                "IN",
	NotIn:             "NOT IN",
	Like:              "LIKE",
	NotLike:           "NOT LIKE",
	ILike:             "ILIKE",
	NotILike:          "NOT ILIKE",
	SimilarTo:         "SIMILAR TO",
	NotSimilarTo:      "NOT SIMILAR TO",
	RegMatch:          "~",
	NotRegMatch:       "!~",
	RegIMatch:         "~*",
	NotRegIMatch:      "!~*",
	IsDistinctFrom:    "IS DISTINCT FROM",
	IsNotDistinctFrom: "IS NOT DISTINCT FROM",
	Contains:          "@>",
	ContainedBy:       "<@",
	JSONExists:        "?",
	JSONSomeExists:    "?|",
	JSONAllExists:     "?&",
	Overlaps:          "&&",
	TSMatches:         "@@",
	Any:               "ANY",
	Some:              "SOME",
	All:               "ALL",
}

func (i ComparisonOperatorSymbol) String() string {
	if i < 0 || i > ComparisonOperatorSymbol(len(comparisonOpName)-1) {
		return fmt.Sprintf("ComparisonOp(%d)", i)
	}
	return comparisonOpName[i]
}

// HasSubOperator returns if the ComparisonOperator is used with a sub-operator.
func (i ComparisonOperatorSymbol) HasSubOperator() bool {
	switch i {
	case Any, Some, All:
		return true
	}
	return false
}
