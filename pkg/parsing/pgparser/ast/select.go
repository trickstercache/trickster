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

// SelectStatement represents any SELECT statement.
type SelectStatement interface {
	Statement
	selectStatement()
}

func (*ParenSelect) selectStatement() {}

func (*SelectClause) selectStatement() {}

func (*UnionClause) selectStatement() {}

func (*ValuesClause) selectStatement() {}

// Select represents a SelectStatement with an ORDER and/or LIMIT.
type Select struct {
	With    *With
	Select  SelectStatement
	OrderBy OrderBy
	Limit   *Limit
	Locking LockingClause
}

// Format implements the NodeFormatter interface.
func (node *Select) Format(ctx *FmtCtx) {
	ctx.FormatNode(node.With)
	ctx.FormatNode(node.Select)
	if len(node.OrderBy) > 0 {
		ctx.WriteByte(' ')
		ctx.FormatNode(&node.OrderBy)
	}
	if node.Limit != nil {
		ctx.WriteByte(' ')
		ctx.FormatNode(node.Limit)
	}
	ctx.FormatNode(&node.Locking)
}

// ParenSelect represents a parenthesized SELECT/UNION/VALUES statement.
type ParenSelect struct {
	Select *Select
}

// Format implements the NodeFormatter interface.
func (node *ParenSelect) Format(ctx *FmtCtx) {
	ctx.WriteByte('(')
	ctx.FormatNode(node.Select)
	ctx.WriteByte(')')
}

// SelectClause represents a SELECT statement.
type SelectClause struct {
	From       From
	DistinctOn DistinctOn
	Exprs      SelectExprs
	GroupBy    GroupBy
	Window     Window
	Having     *Where
	Where      *Where
	// Dialect holds the dialect clauses that follow the WINDOW clause, in written order.
	Dialect     []*DialectClause
	Distinct    bool
	TableSelect bool
}

// Format implements the NodeFormatter interface.
func (node *SelectClause) Format(ctx *FmtCtx) {
	if node.TableSelect {
		ctx.WriteString("TABLE ")
		ctx.FormatNode(node.From.Tables[0])
	} else {
		ctx.WriteString("SELECT ")
		if node.Distinct {
			if node.DistinctOn != nil {
				ctx.FormatNode(&node.DistinctOn)
				ctx.WriteByte(' ')
			} else {
				ctx.WriteString("DISTINCT ")
			}
		}
		ctx.FormatNode(&node.Exprs)
		if len(node.From.Tables) > 0 {
			ctx.WriteByte(' ')
			ctx.FormatNode(&node.From)
		}
		if node.Where != nil {
			ctx.WriteByte(' ')
			ctx.FormatNode(node.Where)
		}
		if len(node.GroupBy) > 0 {
			ctx.WriteByte(' ')
			ctx.FormatNode(&node.GroupBy)
		}
		if node.Having != nil {
			ctx.WriteByte(' ')
			ctx.FormatNode(node.Having)
		}
		if len(node.Window) > 0 {
			ctx.WriteByte(' ')
			ctx.FormatNode(&node.Window)
		}
		for _, clause := range node.Dialect {
			ctx.WriteByte(' ')
			ctx.FormatNode(clause)
		}
	}
}

// SelectExprs represents SELECT expressions.
type SelectExprs []SelectExpr

// Format implements the NodeFormatter interface.
func (node *SelectExprs) Format(ctx *FmtCtx) {
	for i := range *node {
		if i > 0 {
			ctx.WriteString(", ")
		}
		ctx.FormatNode(&(*node)[i])
	}
}

// SelectExpr represents a SELECT expression.
type SelectExpr struct {
	Expr Expr
	As   UnrestrictedName
}

// StarSelectExpr is a convenience function that represents an unqualified "*"
// in a select expression.
func StarSelectExpr() SelectExpr {
	return SelectExpr{Expr: StarExpr()}
}

// Format implements the NodeFormatter interface.
func (node *SelectExpr) Format(ctx *FmtCtx) {
	ctx.FormatNode(node.Expr)
	if node.As != "" {
		ctx.WriteString(" AS ")
		ctx.FormatNode(&node.As)
	}
}

// AliasClause represents an alias, optionally with a column def list:
// "AS name", "AS name(col1, col2)", or "AS name(col1 INT, col2 STRING)".
// Note that the last form is only valid in the context of record-returning
// functions, which also require the last form to define their output types.
type AliasClause struct {
	Alias Name
	Cols  ColumnDefList
}

// Format implements the NodeFormatter interface.
func (f *AliasClause) Format(ctx *FmtCtx) {
	ctx.FormatNode(&f.Alias)
	if len(f.Cols) != 0 {
		// Format as "alias (col1, col2, ...)".
		ctx.WriteString(" (")
		ctx.FormatNode(&f.Cols)
		ctx.WriteByte(')')
	}
}

// ColumnDef represents a column definition in the context of a record type
// alias, like in select * from json_to_record(...) AS foo(a INT, b INT).
type ColumnDef struct {
	Name Name
	Type ResolvableTypeReference
}

// Format implements the NodeFormatter interface.
func (c *ColumnDef) Format(ctx *FmtCtx) {
	ctx.FormatNode(&c.Name)
	if c.Type != nil {
		ctx.WriteByte(' ')
		ctx.FormatTypeReference(c.Type)
	}
}

// ColumnDefList represents a list of ColumnDefs.
type ColumnDefList []ColumnDef

// Format implements the NodeFormatter interface.
func (c *ColumnDefList) Format(ctx *FmtCtx) {
	for i := range *c {
		if i > 0 {
			ctx.WriteString(", ")
		}
		ctx.FormatNode(&(*c)[i])
	}
}

// From represents a FROM clause.
type From struct {
	Tables TableExprs
}

// Format implements the NodeFormatter interface.
func (node *From) Format(ctx *FmtCtx) {
	ctx.WriteString("FROM ")
	ctx.FormatNode(&node.Tables)
}

// TableExprs represents a list of table expressions.
type TableExprs []TableExpr

// Format implements the NodeFormatter interface.
func (node *TableExprs) Format(ctx *FmtCtx) {
	prefix := ""
	for _, n := range *node {
		ctx.WriteString(prefix)
		ctx.FormatNode(n)
		prefix = ", "
	}
}

// TableExpr represents a table expression.
type TableExpr interface {
	NodeFormatter
	tableExpr()
	WalkTableExpr(Visitor) TableExpr
}

func (*AliasedTableExpr) tableExpr() {}

func (*ParenTableExpr) tableExpr() {}

func (*JoinTableExpr) tableExpr() {}

func (*RowsFromExpr) tableExpr() {}

func (*Subquery) tableExpr() {}

// AliasedTableExpr represents a table expression coupled with an optional
// alias.
type AliasedTableExpr struct {
	Expr       TableExpr
	Ordinality bool
	Lateral    bool
	As         AliasClause
}

// Format implements the NodeFormatter interface.
func (node *AliasedTableExpr) Format(ctx *FmtCtx) {
	if node.Lateral {
		ctx.WriteString("LATERAL ")
	}
	ctx.FormatNode(node.Expr)
	if node.Ordinality {
		ctx.WriteString(" WITH ORDINALITY")
	}
	if node.As.Alias != "" {
		ctx.WriteString(" AS ")
		ctx.FormatNode(&node.As)
	}
}

// ParenTableExpr represents a parenthesized TableExpr.
type ParenTableExpr struct {
	Expr TableExpr
}

// Format implements the NodeFormatter interface.
func (node *ParenTableExpr) Format(ctx *FmtCtx) {
	ctx.WriteByte('(')
	ctx.FormatNode(node.Expr)
	ctx.WriteByte(')')
}

// JoinTableExpr represents a TableExpr that's a JOIN operation.
type JoinTableExpr struct {
	JoinType string
	Left     TableExpr
	Right    TableExpr
	Cond     JoinCond
}

// JoinTableExpr.Join
const (
	AstFull  = "FULL"
	AstLeft  = "LEFT"
	AstRight = "RIGHT"
	AstCross = "CROSS"
	AstInner = "INNER"
)

// JoinTableExpr.Hint
const ()

// Format implements the NodeFormatter interface.
func (node *JoinTableExpr) Format(ctx *FmtCtx) {
	ctx.FormatNode(node.Left)
	ctx.WriteByte(' ')
	if _, isNatural := node.Cond.(NaturalJoinCond); isNatural {
		// Natural joins have a different syntax: "<a> NATURAL <join_type> <b>"
		ctx.FormatNode(node.Cond)
		ctx.WriteByte(' ')
		if node.JoinType != "" {
			ctx.WriteString(node.JoinType)
			ctx.WriteByte(' ')
		}
		ctx.WriteString("JOIN ")
		ctx.FormatNode(node.Right)
	} else {
		// General syntax: "<a> <join_type> [<join_hint>] JOIN <b> <condition>"
		if node.JoinType != "" {
			ctx.WriteString(node.JoinType)
			ctx.WriteByte(' ')
		}
		ctx.WriteString("JOIN ")
		ctx.FormatNode(node.Right)
		if node.Cond != nil {
			ctx.WriteByte(' ')
			ctx.FormatNode(node.Cond)
		}
	}
}

// JoinCond represents a join condition.
type JoinCond interface {
	NodeFormatter
	joinCond()
}

func (NaturalJoinCond) joinCond() {}

func (*OnJoinCond) joinCond() {}

func (*UsingJoinCond) joinCond() {}

// NaturalJoinCond represents a NATURAL join condition
type NaturalJoinCond struct{}

// Format implements the NodeFormatter interface.
func (NaturalJoinCond) Format(ctx *FmtCtx) {
	ctx.WriteString("NATURAL")
}

// OnJoinCond represents an ON join condition.
type OnJoinCond struct {
	Expr Expr
}

// Format implements the NodeFormatter interface.
func (node *OnJoinCond) Format(ctx *FmtCtx) {
	ctx.WriteString("ON ")
	ctx.FormatNode(node.Expr)
}

// UsingJoinCond represents a USING join condition.
type UsingJoinCond struct {
	Cols NameList
}

// Format implements the NodeFormatter interface.
func (node *UsingJoinCond) Format(ctx *FmtCtx) {
	ctx.WriteString("USING (")
	ctx.FormatNode(&node.Cols)
	ctx.WriteByte(')')
}

// Where represents a WHERE or HAVING clause.
type Where struct {
	Type string
	Expr Expr
}

// Where.Type
const (
	AstWhere  = "WHERE"
	AstHaving = "HAVING"
)

// NewWhere creates a WHERE or HAVING clause out of an Expr. If the expression
// is nil, it returns nil.
func NewWhere(typ string, expr Expr) *Where {
	if expr == nil {
		return nil
	}
	return &Where{Type: typ, Expr: expr}
}

// Format implements the NodeFormatter interface.
func (node *Where) Format(ctx *FmtCtx) {
	ctx.WriteString(node.Type)
	ctx.WriteByte(' ')
	ctx.FormatNode(node.Expr)
}

// GroupBy represents a GROUP BY clause.
type GroupBy []Expr

// Format implements the NodeFormatter interface.
func (node *GroupBy) Format(ctx *FmtCtx) {
	prefix := "GROUP BY "
	for _, n := range *node {
		ctx.WriteString(prefix)
		ctx.FormatNode(n)
		prefix = ", "
	}
}

// DistinctOn represents a DISTINCT ON clause.
type DistinctOn []Expr

// Format implements the NodeFormatter interface.
func (node *DistinctOn) Format(ctx *FmtCtx) {
	ctx.WriteString("DISTINCT ON (")
	ctx.FormatNode((*Exprs)(node))
	ctx.WriteByte(')')
}

// OrderBy represents an ORDER BY clause.
type OrderBy []*Order

// Format implements the NodeFormatter interface.
func (node *OrderBy) Format(ctx *FmtCtx) {
	prefix := "ORDER BY "
	for _, n := range *node {
		ctx.WriteString(prefix)
		ctx.FormatNode(n)
		prefix = ", "
	}
}

// Direction for ordering results.
type Direction int8

// Direction values.
const (
	DefaultDirection Direction = iota
	Ascending
	Descending
)

var directionName = [...]string{
	DefaultDirection: "",
	Ascending:        "ASC",
	Descending:       "DESC",
}

func (d Direction) String() string {
	if d < 0 || d > Direction(len(directionName)-1) {
		return fmt.Sprintf("Direction(%d)", d)
	}
	return directionName[d]
}

// NullsOrder for specifying ordering of NULLs.
type NullsOrder int8

// Null order values.
const (
	DefaultNullsOrder NullsOrder = iota
	NullsFirst
	NullsLast
)

var nullsOrderName = [...]string{
	DefaultNullsOrder: "",
	NullsFirst:        "NULLS FIRST",
	NullsLast:         "NULLS LAST",
}

func (n NullsOrder) String() string {
	if n < 0 || n > NullsOrder(len(nullsOrderName)-1) {
		return fmt.Sprintf("NullsOrder(%d)", n)
	}
	return nullsOrderName[n]
}

// OrderType indicates which type of expression is used in ORDER BY.
type OrderType int

const (
	// OrderByColumn is the regular "by expression/column" ORDER BY specification.
	OrderByColumn OrderType = iota
	// OrderByIndex enables the user to specify a given index' columns implicitly.
	OrderByIndex
)

// Order represents an ordering expression.
type Order struct {
	OrderType  OrderType
	Expr       Expr
	Direction  Direction
	NullsOrder NullsOrder
	// Table/Index replaces Expr when OrderType = OrderByIndex.
	Table TableName
	// If Index is empty, then the order should use the primary key.
	Index UnrestrictedName
}

// Format implements the NodeFormatter interface.
func (node *Order) Format(ctx *FmtCtx) {
	if node.OrderType == OrderByColumn {
		ctx.FormatNode(node.Expr)
	} else {
		if node.Index == "" {
			ctx.WriteString("PRIMARY KEY ")
			ctx.FormatNode(&node.Table)
		} else {
			ctx.WriteString("INDEX ")
			ctx.FormatNode(&node.Table)
			ctx.WriteByte('@')
			ctx.FormatNode(&node.Index)
		}
	}
	if node.Direction != DefaultDirection {
		ctx.WriteByte(' ')
		ctx.WriteString(node.Direction.String())
	}
	if node.NullsOrder != DefaultNullsOrder {
		ctx.WriteByte(' ')
		ctx.WriteString(node.NullsOrder.String())
	}
}

// Limit represents a LIMIT clause.
type Limit struct {
	Offset, Count Expr
	LimitAll      bool
}

// Format implements the NodeFormatter interface.
func (node *Limit) Format(ctx *FmtCtx) {
	needSpace := false
	if node.Count != nil {
		ctx.WriteString("LIMIT ")
		ctx.FormatNode(node.Count)
		needSpace = true
	} else if node.LimitAll {
		ctx.WriteString("LIMIT ALL")
		needSpace = true
	}
	if node.Offset != nil {
		if needSpace {
			ctx.WriteByte(' ')
		}
		ctx.WriteString("OFFSET ")
		ctx.FormatNode(node.Offset)
	}
}

// RowsFromExpr represents a ROWS FROM(...) expression.
type RowsFromExpr struct {
	Items Exprs
}

// Format implements the NodeFormatter interface.
func (node *RowsFromExpr) Format(ctx *FmtCtx) {
	ctx.WriteString("ROWS FROM (")
	ctx.FormatNode(&node.Items)
	ctx.WriteByte(')')
}

// Window represents a WINDOW clause.
type Window []*WindowDef

// Format implements the NodeFormatter interface.
func (node *Window) Format(ctx *FmtCtx) {
	prefix := "WINDOW "
	for _, n := range *node {
		ctx.WriteString(prefix)
		ctx.FormatNode(&n.Name)
		ctx.WriteString(" AS ")
		ctx.FormatNode(n)
		prefix = ", "
	}
}

// WindowDef represents a single window definition expression.
type WindowDef struct {
	Name       Name
	RefName    Name
	Partitions Exprs
	OrderBy    OrderBy
	Frame      *WindowFrame
}

// Format implements the NodeFormatter interface.
func (node *WindowDef) Format(ctx *FmtCtx) {
	ctx.WriteByte('(')
	needSpaceSeparator := false
	if node.RefName != "" {
		ctx.FormatNode(&node.RefName)
		needSpaceSeparator = true
	}
	if len(node.Partitions) > 0 {
		if needSpaceSeparator {
			ctx.WriteByte(' ')
		}
		ctx.WriteString("PARTITION BY ")
		ctx.FormatNode(&node.Partitions)
		needSpaceSeparator = true
	}
	if len(node.OrderBy) > 0 {
		if needSpaceSeparator {
			ctx.WriteByte(' ')
		}
		ctx.FormatNode(&node.OrderBy)
		needSpaceSeparator = true
	}
	if node.Frame != nil {
		if needSpaceSeparator {
			ctx.WriteByte(' ')
		}
		ctx.FormatNode(node.Frame)
	}
	ctx.WriteByte(')')
}

// WindowFrameBound specifies the offset and the type of boundary.
type WindowFrameBound struct {
	BoundType  WindowFrameBoundType
	OffsetExpr Expr
}

// HasOffset returns whether node contains an offset.
func (node *WindowFrameBound) HasOffset() bool {
	return node.BoundType.IsOffset()
}

// WindowFrameBounds specifies boundaries of the window frame.
// The row at StartBound is included whereas the row at EndBound is not.
type WindowFrameBounds struct {
	StartBound *WindowFrameBound
	EndBound   *WindowFrameBound
}

// WindowFrame represents static state of window frame over which calculations are made.
type WindowFrame struct {
	Mode      WindowFrameMode      // the mode of framing being used
	Bounds    WindowFrameBounds    // the bounds of the frame
	Exclusion WindowFrameExclusion // optional frame exclusion
}

// Format implements the NodeFormatter interface.
func (node *WindowFrameBound) Format(ctx *FmtCtx) {
	switch node.BoundType {
	case UnboundedPreceding:
		ctx.WriteString("UNBOUNDED PRECEDING")
	case OffsetPreceding:
		ctx.FormatNode(node.OffsetExpr)
		ctx.WriteString(" PRECEDING")
	case CurrentRow:
		ctx.WriteString("CURRENT ROW")
	case OffsetFollowing:
		ctx.FormatNode(node.OffsetExpr)
		ctx.WriteString(" FOLLOWING")
	case UnboundedFollowing:
		ctx.WriteString("UNBOUNDED FOLLOWING")
	default:
		panic(assertionFailedf("unhandled case: %d", node.BoundType))
	}
}

// Format implements the NodeFormatter interface.
func (f *WindowFrame) Format(ctx *FmtCtx) {
	ctx.WriteString(WindowModeName(f.Mode))
	ctx.WriteByte(' ')
	if f.Bounds.EndBound != nil {
		ctx.WriteString("BETWEEN ")
		ctx.FormatNode(f.Bounds.StartBound)
		ctx.WriteString(" AND ")
		ctx.FormatNode(f.Bounds.EndBound)
	} else {
		ctx.FormatNode(f.Bounds.StartBound)
	}
	if f.Exclusion != NoExclusion {
		ctx.WriteByte(' ')
		ctx.WriteString(f.Exclusion.String())
	}
}

// LockingClause represents a locking clause, like FOR UPDATE.
type LockingClause []*LockingItem

// Format implements the NodeFormatter interface.
func (node *LockingClause) Format(ctx *FmtCtx) {
	for _, n := range *node {
		ctx.FormatNode(n)
	}
}

// LockingItem represents a single locking item in a locking clause.
type LockingItem struct {
	Strength   LockingStrength
	Targets    TableNames
	WaitPolicy LockingWaitPolicy
	Form       LockingForm
}

// Format implements the NodeFormatter interface.
func (f *LockingItem) Format(ctx *FmtCtx) {
	ctx.FormatNode(f.Strength)
	if len(f.Targets) > 0 {
		ctx.WriteString(" OF ")
		ctx.FormatNode(&f.Targets)
	}
	ctx.FormatNode(f.WaitPolicy)
	// Form and durability are not currently exposed through SQL.
}

// LockingStrength represents the possible row-level lock modes for a SELECT
// statement.
type LockingStrength byte

// The ordering of the variants is important, because the highest numerical
// value takes precedence when row-level locking is specified multiple ways.
const (
	// ForNone represents the default - no for statement at all.
	// LockingItem AST nodes are never created with this strength.
	ForNone LockingStrength = iota
	// ForKeyShare represents FOR KEY SHARE.
	ForKeyShare
	// ForShare represents FOR SHARE.
	ForShare
	// ForNoKeyUpdate represents FOR NO KEY UPDATE.
	ForNoKeyUpdate
	// ForUpdate represents FOR UPDATE.
	ForUpdate
)

var lockingStrengthName = [...]string{
	ForNone:        "",
	ForKeyShare:    "FOR KEY SHARE",
	ForShare:       "FOR SHARE",
	ForNoKeyUpdate: "FOR NO KEY UPDATE",
	ForUpdate:      "FOR UPDATE",
}

func (s LockingStrength) String() string {
	return lockingStrengthName[s]
}

// Format implements the NodeFormatter interface.
func (s LockingStrength) Format(ctx *FmtCtx) {
	if s != ForNone {
		ctx.WriteString(" ")
		ctx.WriteString(s.String())
	}
}

// LockingWaitPolicy represents the possible policies for handling conflicting
// locks held by other active transactions when attempting to lock rows due to
// FOR UPDATE/SHARE clauses (i.e. it represents the NOWAIT and SKIP LOCKED
// options).
type LockingWaitPolicy byte

// The ordering of the variants is important, because the highest numerical
// value takes precedence when row-level locking is specified multiple ways.
const (
	// LockWaitBlock represents the default - wait for the lock to become
	// available.
	LockWaitBlock LockingWaitPolicy = iota
	// LockWaitSkipLocked represents SKIP LOCKED - skip rows that can't be locked.
	LockWaitSkipLocked
	// LockWaitError represents NOWAIT - raise an error if a row cannot be
	// locked.
	LockWaitError
)

var lockingWaitPolicyName = [...]string{
	LockWaitBlock:      "",
	LockWaitSkipLocked: "SKIP LOCKED",
	LockWaitError:      "NOWAIT",
}

func (p LockingWaitPolicy) String() string {
	return lockingWaitPolicyName[p]
}

// Format implements the NodeFormatter interface.
func (p LockingWaitPolicy) Format(ctx *FmtCtx) {
	if p != LockWaitBlock {
		ctx.WriteString(" ")
		ctx.WriteString(p.String())
	}
}

// LockingForm represents the form of locking to use, record locking or
// predicate locking. It is not currently exposed through SQL, but could be once
// more fully supported.
type LockingForm byte

// The ordering of the variants is important, because the highest numerical
// value takes precedence when row-level locking is specified multiple ways.
const (
	// LockRecord represents the default: lock existing rows within the specified
	// span(s), which prevents modification of those rows but does not prevent
	// insertion of new rows (phantoms) into the span(s).
	LockRecord LockingForm = iota
	// LockPredicate represents locking the logical predicate defined by the
	// span(s), preventing modification of existing rows as well as insertion of
	// new rows (phantoms). This is similar to the behavior of "next-key locks" in
	// InnoDB, "key-range locks" in SQL Server, "phantom locks" in Sybase, etc.
	// (Postgres also has predicate locks, which it uses under serializable
	// isolation, but these are used to detect serializable violations rather than
	// for mutual exclusion.)
	//
	// We currently only use predicate locks for uniqueness checks under snapshot
	// and read committed isolation, and only support predicate locks on
	// single-key spans.
	LockPredicate
)

var lockingClassName = [...]string{
	LockRecord:    "record",
	LockPredicate: "predicate",
}

func (p LockingForm) String() string {
	return lockingClassName[p]
}
