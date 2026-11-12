// Copyright 2015 The Cockroach Authors.
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

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/lex"
)

// Expr represents an expression.
type Expr interface {
	fmt.Stringer
	NodeFormatter
	// Walk recursively walks all children using WalkExpr. If any children are changed, it returns a
	// copy of this node updated to point to the new children. Otherwise the receiver is returned.
	// For childless (leaf) Exprs, its implementation is empty.
	Walk(Visitor) Expr
}

// operatorExpr is used to identify expression types that involve operators;
// used by exprStrWithParen.
type operatorExpr interface {
	Expr
	operatorExpr()
}

// Operator is used to identify Operators; used in sql.y.
type Operator interface {
	Operator()
}

// exprFmtWithParen is a variant of Format() which adds a set of outer parens
// if the expression involves an operator. It is used internally when the
// expression is part of another expression and we know it is preceded or
// followed by an operator.
func exprFmtWithParen(ctx *FmtCtx, e Expr) {
	if _, ok := e.(operatorExpr); ok {
		ctx.WriteByte('(')
		ctx.FormatNode(e)
		ctx.WriteByte(')')
	} else {
		ctx.FormatNode(e)
	}
}

// AndExpr represents an AND expression.
type AndExpr struct {
	Left, Right Expr
}

func (*AndExpr) operatorExpr() {}

func binExprFmtWithParen(ctx *FmtCtx, e1 Expr, op string, e2 Expr, pad bool) {
	exprFmtWithParen(ctx, e1)
	if pad {
		ctx.WriteByte(' ')
	}
	ctx.WriteString(op)
	if pad {
		ctx.WriteByte(' ')
	}
	exprFmtWithParen(ctx, e2)
}

func binExprFmtWithParenAndSubOp(ctx *FmtCtx, e1 Expr, subOp, op string, e2 Expr) {
	exprFmtWithParen(ctx, e1)
	ctx.WriteByte(' ')
	if subOp != "" {
		ctx.WriteString(subOp)
		ctx.WriteByte(' ')
	}
	ctx.WriteString(op)
	ctx.WriteByte(' ')
	exprFmtWithParen(ctx, e2)
}

// Format implements the NodeFormatter interface.
func (node *AndExpr) Format(ctx *FmtCtx) {
	binExprFmtWithParen(ctx, node.Left, "AND", node.Right, true)
}

// OrExpr represents an OR expression.
type OrExpr struct {
	Left, Right Expr
}

func (*OrExpr) operatorExpr() {}

// Format implements the NodeFormatter interface.
func (node *OrExpr) Format(ctx *FmtCtx) {
	binExprFmtWithParen(ctx, node.Left, "OR", node.Right, true)
}

// NotExpr represents a NOT expression.
type NotExpr struct {
	Expr Expr
}

func (*NotExpr) operatorExpr() {}

// Format implements the NodeFormatter interface.
func (node *NotExpr) Format(ctx *FmtCtx) {
	ctx.WriteString("NOT ")
	exprFmtWithParen(ctx, node.Expr)
}

// IsNullExpr represents an IS NULL expression. This is equivalent to IS NOT
// DISTINCT FROM NULL, except when the input is a tuple.
type IsNullExpr struct {
	Expr Expr
}

func (*IsNullExpr) operatorExpr() {}

// Format implements the NodeFormatter interface.
func (node *IsNullExpr) Format(ctx *FmtCtx) {
	exprFmtWithParen(ctx, node.Expr)
	ctx.WriteString(" IS NULL")
}

// IsNotNullExpr represents an IS NOT NULL expression. This is equivalent to IS
// DISTINCT FROM NULL, except when the input is a tuple.
type IsNotNullExpr struct {
	Expr Expr
}

func (*IsNotNullExpr) operatorExpr() {}

// Format implements the NodeFormatter interface.
func (node *IsNotNullExpr) Format(ctx *FmtCtx) {
	exprFmtWithParen(ctx, node.Expr)
	ctx.WriteString(" IS NOT NULL")
}

// ParenExpr represents a parenthesized expression.
type ParenExpr struct {
	Expr Expr
}

// Format implements the NodeFormatter interface.
func (node *ParenExpr) Format(ctx *FmtCtx) {
	ctx.WriteByte('(')
	ctx.FormatNode(node.Expr)
	ctx.WriteByte(')')
}

// ComparisonExpr represents a two-value comparison expression.
type ComparisonExpr struct {
	Operator    ComparisonOperator
	SubOperator ComparisonOperator // used for array operators (when Operator is Any, Some, or All)
	Left, Right Expr
}

func (*ComparisonExpr) operatorExpr() {}

// Format implements the NodeFormatter interface.
func (node *ComparisonExpr) Format(ctx *FmtCtx) {
	opStr := node.Operator.String()
	// IS and IS NOT are equivalent to IS NOT DISTINCT FROM and IS DISTINCT
	// FROM, respectively, when the RHS is true or false. We prefer the less
	// verbose IS and IS NOT in those cases.
	if node.Operator.Symbol == IsDistinctFrom && (node.Right == BoolTrue || node.Right == BoolFalse) {
		opStr = "IS NOT"
	} else if node.Operator.Symbol == IsNotDistinctFrom && (node.Right == BoolTrue || node.Right == BoolFalse) {
		opStr = "IS"
	}
	if node.Operator.Symbol.HasSubOperator() {
		binExprFmtWithParenAndSubOp(ctx, node.Left, node.SubOperator.String(), opStr, node.Right)
	} else {
		binExprFmtWithParen(ctx, node.Left, opStr, node.Right, true)
	}
}

// RangeCond represents a BETWEEN [SYMMETRIC] or a NOT BETWEEN [SYMMETRIC]
// expression.
type RangeCond struct {
	Not       bool
	Symmetric bool
	Left      Expr
	From, To  Expr
}

func (*RangeCond) operatorExpr() {}

// Format implements the NodeFormatter interface.
func (node *RangeCond) Format(ctx *FmtCtx) {
	notStr := " BETWEEN "
	if node.Not {
		notStr = " NOT BETWEEN "
	}
	exprFmtWithParen(ctx, node.Left)
	ctx.WriteString(notStr)
	if node.Symmetric {
		ctx.WriteString("SYMMETRIC ")
	}
	binExprFmtWithParen(ctx, node.From, "AND", node.To, true)
}

// IsOfTypeExpr represents an IS {,NOT} OF (type_list) expression.
type IsOfTypeExpr struct {
	Not   bool
	Expr  Expr
	Types []ResolvableTypeReference
}

func (*IsOfTypeExpr) operatorExpr() {}

// Format implements the NodeFormatter interface.
func (node *IsOfTypeExpr) Format(ctx *FmtCtx) {
	exprFmtWithParen(ctx, node.Expr)
	ctx.WriteString(" IS")
	if node.Not {
		ctx.WriteString(" NOT")
	}
	ctx.WriteString(" OF (")
	for i, t := range node.Types {
		if i > 0 {
			ctx.WriteString(", ")
		}
		ctx.FormatTypeReference(t)
	}
	ctx.WriteByte(')')
}

// IfErrExpr represents an IFERROR expression.
type IfErrExpr struct {
	Cond    Expr
	Else    Expr
	ErrCode Expr
}

// Format implements the NodeFormatter interface.
func (node *IfErrExpr) Format(ctx *FmtCtx) {
	if node.Else != nil {
		ctx.WriteString("IFERROR(")
	} else {
		ctx.WriteString("ISERROR(")
	}
	ctx.FormatNode(node.Cond)
	if node.Else != nil {
		ctx.WriteString(", ")
		ctx.FormatNode(node.Else)
	}
	if node.ErrCode != nil {
		ctx.WriteString(", ")
		ctx.FormatNode(node.ErrCode)
	}
	ctx.WriteByte(')')
}

// IfExpr represents an IF expression.
type IfExpr struct {
	Cond Expr
	True Expr
	Else Expr
}

// Format implements the NodeFormatter interface.
func (node *IfExpr) Format(ctx *FmtCtx) {
	ctx.WriteString("IF(")
	ctx.FormatNode(node.Cond)
	ctx.WriteString(", ")
	ctx.FormatNode(node.True)
	ctx.WriteString(", ")
	ctx.FormatNode(node.Else)
	ctx.WriteByte(')')
}

// NullIfExpr represents a NULLIF expression.
type NullIfExpr struct {
	Expr1 Expr
	Expr2 Expr
}

// Format implements the NodeFormatter interface.
func (node *NullIfExpr) Format(ctx *FmtCtx) {
	ctx.WriteString("NULLIF(")
	ctx.FormatNode(node.Expr1)
	ctx.WriteString(", ")
	ctx.FormatNode(node.Expr2)
	ctx.WriteByte(')')
}

// CoalesceExpr represents a COALESCE or IFNULL expression.
type CoalesceExpr struct {
	Name  string
	Exprs Exprs
}

// Format implements the NodeFormatter interface.
func (node *CoalesceExpr) Format(ctx *FmtCtx) {
	ctx.WriteString(node.Name)
	ctx.WriteByte('(')
	ctx.FormatNode(&node.Exprs)
	ctx.WriteByte(')')
}

// DefaultVal represents the DEFAULT expression.
type DefaultVal struct{}

// Format implements the NodeFormatter interface.
func (node DefaultVal) Format(ctx *FmtCtx) {
	ctx.WriteString("DEFAULT")
}

// Placeholder represents a named placeholder.
type Placeholder struct {
	Idx PlaceholderIdx
}

// Format implements the NodeFormatter interface.
func (node *Placeholder) Format(ctx *FmtCtx) {
	ctx.Printf("$%d", node.Idx+1)
}

// Tuple represents a parenthesized list of expressions.
type Tuple struct {
	Exprs  Exprs
	Labels []string

	// Row indicates whether `ROW` was used in the input syntax. This is
	// used solely to generate column names automatically, see
	// col_name.go.
	Row bool
}

// Format implements the NodeFormatter interface.
func (node *Tuple) Format(ctx *FmtCtx) {
	// If there are labels, extra parentheses are required surrounding the
	// expression.
	if len(node.Labels) > 0 {
		ctx.WriteByte('(')
	}
	if len(node.Exprs) == 1 && len(node.Labels) == 0 && node.Row {
		// ROW keeps a one-element row distinct from a parenthesized value, which a trailing comma
		// would do too, but only CockroachDB reads that
		ctx.WriteString("ROW")
	}
	ctx.WriteByte('(')
	ctx.FormatNode(&node.Exprs)
	if len(node.Exprs) == 1 && len(node.Labels) > 0 {
		ctx.WriteByte(',')
	}
	ctx.WriteByte(')')
	if len(node.Labels) > 0 {
		ctx.WriteString(" AS ")
		comma := ""
		for i := range node.Labels {
			ctx.WriteString(comma)
			ctx.FormatNode((*Name)(&node.Labels[i]))
			comma = ", "
		}
		ctx.WriteByte(')')
	}
}

// Array represents an array constructor.
type Array struct {
	Exprs Exprs
}

// Format implements the NodeFormatter interface.
func (node *Array) Format(ctx *FmtCtx) {
	ctx.WriteString("ARRAY[")
	ctx.FormatNode(&node.Exprs)
	ctx.WriteByte(']')
}

// ArrayFlatten represents a subquery array constructor.
type ArrayFlatten struct {
	Subquery Expr
}

// Format implements the NodeFormatter interface.
func (node *ArrayFlatten) Format(ctx *FmtCtx) {
	ctx.WriteString("ARRAY ")
	exprFmtWithParen(ctx, node.Subquery)
}

// Exprs represents a list of value expressions. It's not a valid expression
// because it's not parenthesized.
type Exprs []Expr

// Format implements the NodeFormatter interface.
func (node *Exprs) Format(ctx *FmtCtx) {
	for i, n := range *node {
		if i > 0 {
			ctx.WriteString(", ")
		}
		ctx.FormatNode(n)
	}
}

// Subquery represents a subquery.
type Subquery struct {
	Select SelectStatement
	Exists bool

	// Idx is a query-unique index for the subquery.
	// Subqueries are 1-indexed to ensure that the default
	// value 0 can be used to detect uninitialized subqueries.
	Idx int
}

// Format implements the NodeFormatter interface.
func (node *Subquery) Format(ctx *FmtCtx) {
	if node.Exists {
		ctx.WriteString("EXISTS ")
	}
	ctx.FormatNode(node.Select)
}

// BinaryExpr represents a binary value expression.
type BinaryExpr struct {
	Operator    BinaryOperator
	Left, Right Expr
}

func (*BinaryExpr) operatorExpr() {}

// Format implements the NodeFormatter interface.
func (node *BinaryExpr) Format(ctx *FmtCtx) {
	binExprFmtWithParen(ctx, node.Left, node.Operator.String(), node.Right, node.Operator.Symbol.IsPadded())
}

// UnaryOperator represents a unary operator used in a UnaryExpr.
type UnaryOperator struct {
	Symbol UnaryOperatorSymbol
	// IsExplicitOperator is true if OPERATOR(symbol) is used.
	IsExplicitOperator bool
}

// MakeUnaryOperator creates a UnaryOperator given a symbol.
func MakeUnaryOperator(symbol UnaryOperatorSymbol) UnaryOperator {
	return UnaryOperator{Symbol: symbol}
}

func (o UnaryOperator) String() string {
	if o.IsExplicitOperator {
		return fmt.Sprintf("OPERATOR(%s)", o.Symbol.String())
	}
	return o.Symbol.String()
}

// Operator implements tree.Operator.
func (UnaryOperator) Operator() {}

// IsUnaryComplement returns whether op is a unary complement operator.
func IsUnaryComplement(op Operator) bool {
	u, ok := op.(UnaryOperator)
	return ok && u.Symbol == UnaryComplement
}

// UnaryOperatorSymbol represents a unary operator.
type UnaryOperatorSymbol uint8

// UnaryExpr.Operator.Symbol
const (
	UnaryMinus UnaryOperatorSymbol = iota
	UnaryComplement
	UnarySqrt
	UnaryCbrt
	UnaryPlus
)

// UnaryOpName is the mapping of unary operators to names.
var UnaryOpName = [...]string{
	UnaryMinus:      "-",
	UnaryPlus:       "+",
	UnaryComplement: "~",
	UnarySqrt:       "|/",
	UnaryCbrt:       "||/",
}

func (i UnaryOperatorSymbol) String() string {
	if i > UnaryOperatorSymbol(len(UnaryOpName)-1) {
		return fmt.Sprintf("UnaryOp(%d)", i)
	}
	return UnaryOpName[i]
}

// UnaryExpr represents a unary value expression.
type UnaryExpr struct {
	Operator UnaryOperator
	Expr     Expr
}

func (*UnaryExpr) operatorExpr() {}

// Format implements the NodeFormatter interface.
func (node *UnaryExpr) Format(ctx *FmtCtx) {
	ctx.WriteString(node.Operator.String())
	e := node.Expr
	_, isOp := e.(operatorExpr)
	_, isConstant := e.(constantExpr)
	if isOp || (node.Operator.Symbol == UnaryMinus && isConstant) {
		ctx.WriteByte('(')
		ctx.FormatNode(e)
		ctx.WriteByte(')')
	} else {
		ctx.FormatNode(e)
	}
}

// FuncExpr represents a function call.
type FuncExpr struct {
	Func  ResolvableFunctionReference
	Type  funcType
	Exprs Exprs
	// Filter is used for filters on aggregates: SUM(k) FILTER (WHERE k > 0)
	Filter    Expr
	WindowDef *WindowDef

	// AggType is used to specify the type of aggregation.
	AggType AggType
	// OrderBy is used for aggregations which specify an order. This same field
	// is used for any type of aggregation.
	OrderBy OrderBy

	// Suffix is a dialect suffix written after the call, such as GreptimeDB's RANGE '5m'.
	Suffix *DialectClause

	// Bare marks a SQL-value function written without parentheses, such as CURRENT_DATE.
	Bare bool
}

type funcType int

// FuncExpr.Type
const (
	_ funcType = iota
	DistinctFuncType
	AllFuncType
)

var funcTypeName = [...]string{
	DistinctFuncType: "DISTINCT",
	AllFuncType:      "ALL",
}

// AggType specifies the type of aggregation.
type AggType int

// FuncExpr.AggType
const (
	_ AggType = iota
	// GeneralAgg is used for general-purpose aggregate functions.
	// array_agg(col1 ORDER BY col2)
	GeneralAgg
	// OrderedSetAgg is used for ordered-set aggregate functions.
	// percentile_disc(fraction) WITHIN GROUP (ORDER BY col1)
	OrderedSetAgg
)

// Format implements the NodeFormatter interface.
func (node *FuncExpr) Format(ctx *FmtCtx) {
	var typ string
	if node.Type != 0 {
		typ = funcTypeName[node.Type] + " "
	}

	// A function name is quoted like any other identifier, so a quoted or reserved name reads back.
	ctx.FormatNode(&node.Func)
	if node.Bare {
		return
	}
	if field, ok := extractField(node); ok {
		ctx.WriteByte('(')
		ctx.WriteString(field)
		ctx.WriteString(" FROM ")
		ctx.FormatNode(node.Exprs[1])
		ctx.WriteByte(')')
		return
	}

	ctx.WriteByte('(')
	ctx.WriteString(typ)
	ctx.FormatNode(&node.Exprs)
	if node.AggType == GeneralAgg && len(node.OrderBy) > 0 {
		ctx.WriteByte(' ')
		ctx.FormatNode(&node.OrderBy)
	}
	ctx.WriteByte(')')
	if node.AggType == OrderedSetAgg && len(node.OrderBy) > 0 {
		ctx.WriteString(" WITHIN GROUP (")
		ctx.FormatNode(&node.OrderBy)
		ctx.WriteString(")")
	}
	if node.Filter != nil {
		ctx.WriteString(" FILTER (WHERE ")
		ctx.FormatNode(node.Filter)
		ctx.WriteString(")")
	}
	if window := node.WindowDef; window != nil {
		ctx.WriteString(" OVER ")
		if window.Name != "" {
			ctx.FormatNode(&window.Name)
		} else {
			ctx.FormatNode(window)
		}
	}
	if node.Suffix != nil {
		ctx.WriteByte(' ')
		ctx.FormatNode(node.Suffix)
	}
}

const intervalKeyword = "INTERVAL "

// extractField returns the field of an EXTRACT written as EXTRACT(field FROM source)
func extractField(node *FuncExpr) (string, bool) {
	if len(node.Exprs) != 2 || node.Type != 0 || len(node.OrderBy) != 0 || node.Filter != nil || node.WindowDef != nil {
		return "", false
	}
	field, ok := node.Exprs[0].(*StrVal)
	if !ok || !field.fromField {
		return "", false
	}
	if lex.IsBareIdentifier(field.s) {
		return field.s, true
	}
	return lex.EscapeStandardSQLString(field.s), true
}

// CaseExpr represents a CASE expression.
type CaseExpr struct {
	Expr  Expr
	Whens []*When
	Else  Expr
}

// Format implements the NodeFormatter interface.
func (node *CaseExpr) Format(ctx *FmtCtx) {
	ctx.WriteString("CASE ")
	if node.Expr != nil {
		ctx.FormatNode(node.Expr)
		ctx.WriteByte(' ')
	}
	for _, when := range node.Whens {
		ctx.FormatNode(when)
		ctx.WriteByte(' ')
	}
	if node.Else != nil {
		ctx.WriteString("ELSE ")
		ctx.FormatNode(node.Else)
		ctx.WriteByte(' ')
	}
	ctx.WriteString("END")
}

// When represents a WHEN sub-expression.
type When struct {
	Cond Expr
	Val  Expr
}

// Format implements the NodeFormatter interface.
func (node *When) Format(ctx *FmtCtx) {
	ctx.WriteString("WHEN ")
	ctx.FormatNode(node.Cond)
	ctx.WriteString(" THEN ")
	ctx.FormatNode(node.Val)
}

type castSyntaxMode int

// These constants separate the syntax X::Y from CAST(X AS Y).
const (
	CastExplicit castSyntaxMode = iota
	CastShort
	CastPrepend
	// CastInterval is an INTERVAL literal, whose field qualifier follows the string: INTERVAL '1' HOUR
	CastInterval
)

// CastExpr represents a CAST(expr AS type) expression.
type CastExpr struct {
	Expr Expr
	Type ResolvableTypeReference

	SyntaxMode castSyntaxMode
}

// Format implements the NodeFormatter interface.
func (node *CastExpr) Format(ctx *FmtCtx) {
	switch node.SyntaxMode {
	case CastInterval:
		if typ, ok := GetStaticallyKnownType(node.Type); ok {
			if _, isStr := node.Expr.(*StrVal); isStr {
				ctx.WriteString(intervalKeyword)
				ctx.FormatNode(node.Expr)
				if qualifier := typ.IntervalQualifier(); qualifier != "" {
					ctx.WriteByte(' ')
					ctx.WriteString(qualifier)
				}
				return
			}
		}
		exprFmtWithParen(ctx, node.Expr)
		ctx.WriteString("::")
		ctx.FormatTypeReference(node.Type)
	case CastPrepend:
		// This is a special case for things like INTERVAL '1s'. These only work
		// with string constants; if the underlying expression was changed, we fall
		// back to the short syntax.
		if _, ok := node.Expr.(*StrVal); ok {
			ctx.FormatTypeReference(node.Type)
			ctx.WriteByte(' ')
			// We need to replace this with a quoted string constants in certain
			// cases because the grammar requires a string constant rather than an
			// expression for this form of casting in the typed_literal rule
			ctx.FormatNode(node.Expr)
			break
		}
		fallthrough
	case CastShort:
		exprFmtWithParen(ctx, node.Expr)
		ctx.WriteString("::")
		ctx.FormatTypeReference(node.Type)
	default:
		ctx.WriteString("CAST(")
		ctx.FormatNode(node.Expr)
		ctx.WriteString(" AS ")
		ctx.FormatTypeReference(node.Type)
		ctx.WriteByte(')')
	}
}

// ArraySubscripts represents a sequence of one or more array subscripts.
type ArraySubscripts []*ArraySubscript

// Format implements the NodeFormatter interface.
func (a *ArraySubscripts) Format(ctx *FmtCtx) {
	for _, s := range *a {
		ctx.FormatNode(s)
	}
}

// IndirectionExpr represents a subscript expression.
type IndirectionExpr struct {
	Expr        Expr
	Indirection ArraySubscripts
}

// Format implements the NodeFormatter interface.
func (node *IndirectionExpr) Format(ctx *FmtCtx) {
	// If the sub expression is a CastExpr or an Array that has a type,
	// we need to wrap it in a ParenExpr, otherwise the indirection
	// will get interpreted as part of the type.
	// Ex. ('{a}'::_typ)[1] vs. '{a}'::_typ[1].
	// Ex. (ARRAY['a'::typ]:::typ[])[1] vs. ARRAY['a'::typ]:::typ[][1].
	if _, isCast := node.Expr.(*CastExpr); isCast {
		withParens := ParenExpr{Expr: node.Expr}
		exprFmtWithParen(ctx, &withParens)
	} else {
		exprFmtWithParen(ctx, node.Expr)
	}
	ctx.FormatNode(&node.Indirection)
}

type annotateSyntaxMode int

// These constants separate the syntax X:::Y from ANNOTATE_TYPE(X, Y)
const (
	AnnotateExplicit annotateSyntaxMode = iota
	AnnotateShort
)

// AnnotateTypeExpr represents a ANNOTATE_TYPE(expr, type) expression.
type AnnotateTypeExpr struct {
	Expr Expr
	Type ResolvableTypeReference

	SyntaxMode annotateSyntaxMode
}

// Format implements the NodeFormatter interface.
func (node *AnnotateTypeExpr) Format(ctx *FmtCtx) {
	switch node.SyntaxMode {
	case AnnotateShort:
		exprFmtWithParen(ctx, node.Expr)
		ctx.WriteString(":::")
		ctx.FormatTypeReference(node.Type)

	default:
		ctx.WriteString("ANNOTATE_TYPE(")
		ctx.FormatNode(node.Expr)
		ctx.WriteString(", ")
		ctx.FormatTypeReference(node.Type)
		ctx.WriteByte(')')
	}
}

// CollateExpr represents an (expr COLLATE locale) expression.
type CollateExpr struct {
	Expr   Expr
	Locale string
}

// Format implements the NodeFormatter interface.
func (node *CollateExpr) Format(ctx *FmtCtx) {
	exprFmtWithParen(ctx, node.Expr)
	ctx.WriteString(" COLLATE ")
	lex.EncodeUnrestrictedSQLIdent(&ctx.Buffer, node.Locale, ctx.flags.EncodeFlags())
}

// TupleStar represents (E).* expressions.
// It is meant to evaporate during star expansion.
type TupleStar struct {
	Expr Expr
}

// Format implements the NodeFormatter interface.
func (node *TupleStar) Format(ctx *FmtCtx) {
	ctx.WriteByte('(')
	ctx.FormatNode(node.Expr)
	ctx.WriteString(").*")
}

// ColumnAccessExpr represents (E).x expressions. Specifically, it
// allows accessing the column(s) from a Set Returning Function.
type ColumnAccessExpr struct {
	Expr Expr

	// ColName is the name of the column to access.
	ColName Name
}

// Format implements the NodeFormatter interface.
func (node *ColumnAccessExpr) Format(ctx *FmtCtx) {
	ctx.WriteByte('(')
	ctx.FormatNode(node.Expr)
	ctx.WriteString(").")
	ctx.FormatNode(&node.ColName)
}

func (node *AliasedTableExpr) String() string { return AsString(node) }

func (node *ParenTableExpr) String() string { return AsString(node) }

func (node *JoinTableExpr) String() string { return AsString(node) }

func (node *AndExpr) String() string { return AsString(node) }

func (node *Array) String() string { return AsString(node) }

func (node *BinaryExpr) String() string { return AsString(node) }

func (node *CaseExpr) String() string { return AsString(node) }

func (node *CastExpr) String() string { return AsString(node) }

func (node *CoalesceExpr) String() string { return AsString(node) }

func (node *ColumnAccessExpr) String() string { return AsString(node) }

func (node *CollateExpr) String() string { return AsString(node) }

func (node *ComparisonExpr) String() string { return AsString(node) }

func (node *Exprs) String() string { return AsString(node) }

func (node *ArrayFlatten) String() string { return AsString(node) }

func (node *FuncExpr) String() string { return AsString(node) }

func (node *IfExpr) String() string { return AsString(node) }

func (node *IfErrExpr) String() string { return AsString(node) }

func (node *IndirectionExpr) String() string { return AsString(node) }

func (node *IsOfTypeExpr) String() string { return AsString(node) }

func (node *Name) String() string { return AsString(node) }

func (node *UnrestrictedName) String() string { return AsString(node) }

func (node *NotExpr) String() string { return AsString(node) }

func (node *IsNullExpr) String() string { return AsString(node) }

func (node *IsNotNullExpr) String() string { return AsString(node) }

func (node *NullIfExpr) String() string { return AsString(node) }

func (node *NumVal) String() string { return AsString(node) }

func (node *OrExpr) String() string { return AsString(node) }

func (node *ParenExpr) String() string { return AsString(node) }

func (node *RangeCond) String() string { return AsString(node) }

func (node *StrVal) String() string { return AsString(node) }

func (node *Subquery) String() string { return AsString(node) }

func (node *Tuple) String() string { return AsString(node) }

func (node *TupleStar) String() string { return AsString(node) }

func (node *AnnotateTypeExpr) String() string { return AsString(node) }

func (node *UnaryExpr) String() string { return AsString(node) }

func (node DefaultVal) String() string { return AsString(node) }

func (node *Placeholder) String() string { return AsString(node) }

func (list *NameList) String() string { return AsString(list) }
