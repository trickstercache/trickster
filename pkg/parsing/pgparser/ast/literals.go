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

package ast

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const assertionPrefix = "pgparser internal error: "

// assertionFailedf builds the error for a state the parser and formatter should never reach
func assertionFailedf(format string, args ...any) error {
	return fmt.Errorf(assertionPrefix+format, args...)
}

// constantExpr marks the literal nodes, which a unary minus must not print flush against
type constantExpr interface {
	Expr
	constantExpr()
}

func (*NumVal) constantExpr()  {}
func (*StrVal) constantExpr()  {}
func (*BoolVal) constantExpr() {}
func (*NullVal) constantExpr() {}
func (*BitVal) constantExpr()  {}

const (
	literalTrue  = "true"
	literalFalse = "false"
	literalNull  = "NULL"
)

// BoolVal is a TRUE or FALSE literal; BoolTrue and BoolFalse are its only values
type BoolVal struct {
	value bool
}

var (
	// BoolTrue is the TRUE literal
	BoolTrue = &BoolVal{value: true}
	// BoolFalse is the FALSE literal
	BoolFalse = &BoolVal{}
	// Null is the NULL literal
	Null = &NullVal{}
)

// MakeBool returns the literal node for a boolean
func MakeBool(value bool) *BoolVal {
	if value {
		return BoolTrue
	}
	return BoolFalse
}

// Format implements the NodeFormatter interface.
func (b *BoolVal) Format(ctx *FmtCtx) {
	if b.value {
		ctx.WriteString(literalTrue)
		return
	}
	ctx.WriteString(literalFalse)
}

func (b *BoolVal) String() string { return AsString(b) }

// Walk implements the Expr interface.
func (b *BoolVal) Walk(Visitor) Expr { return b }

// NullVal is the NULL literal
type NullVal struct{}

// Format implements the NodeFormatter interface.
func (*NullVal) Format(ctx *FmtCtx) { ctx.WriteString(literalNull) }

func (n *NullVal) String() string { return AsString(n) }

// Walk implements the Expr interface.
func (n *NullVal) Walk(Visitor) Expr { return n }

// BitVal is a bit-string literal such as B'101'
type BitVal struct {
	bits string
	// Written is the literal as the statement spelled it, such as b'101'
	Written string
}

// NewBitVal returns the literal for a string of 0 and 1 digits
func NewBitVal(bits string) (*BitVal, error) {
	for i := range len(bits) {
		if bits[i] != '0' && bits[i] != '1' {
			return nil, fmt.Errorf("could not parse string as bit array: %q is not a valid binary digit", bits[i])
		}
	}
	return &BitVal{bits: bits}, nil
}

// Format implements the NodeFormatter interface.
func (b *BitVal) Format(ctx *FmtCtx) {
	if b.Written != "" {
		ctx.WriteString(b.Written)
		return
	}
	ctx.WriteString("B'")
	ctx.WriteString(b.bits)
	ctx.WriteByte('\'')
}

func (b *BitVal) String() string { return AsString(b) }

// Walk implements the Expr interface.
func (b *BitVal) Walk(Visitor) Expr { return b }

const maxPlaceholderIdx = 1 << 16

// NewPlaceholder returns the node for the placeholder with the given 1-based number, as written after $
func NewPlaceholder(number string) (*Placeholder, error) {
	n, err := strconv.ParseUint(number, 10, 64)
	if err != nil || n == 0 || n > maxPlaceholderIdx {
		return nil, fmt.Errorf("placeholder index must be between 1 and %d", maxPlaceholderIdx)
	}
	return &Placeholder{Idx: PlaceholderIdx(n - 1)}, nil
}

// SetNegative marks the value as negative.
func (expr *NumVal) SetNegative() {
	expr.negative = true
}

// AsInt32 returns the value as a 32-bit integer when it is integral and in range
func (expr *NumVal) AsInt32() (int32, error) {
	v, err := expr.AsInt64()
	if err != nil || v < math.MinInt32 || v > math.MaxInt32 {
		return 0, assertionFailedf("numeric constant %q is not an int32", expr.origString)
	}
	return int32(v), nil
}

// FunctionDefinition names a function the grammar builds from special syntax, such as EXTRACT(x FROM y)
type FunctionDefinition struct {
	// Name is the function's name
	Name string
}

func (*FunctionDefinition) functionReference() {}

// Format implements the NodeFormatter interface.
func (fd *FunctionDefinition) Format(ctx *FmtCtx) { ctx.WriteString(fd.Name) }

func (fd *FunctionDefinition) String() string { return AsString(fd) }

// WrapFunction returns the reference to a function the grammar names itself
func WrapFunction(name string) ResolvableFunctionReference {
	return ResolvableFunctionReference{FunctionReference: &FunctionDefinition{Name: name}}
}

// Name returns the function's name as the grammar read it, unquoted, with any schema parts first
func (r ResolvableFunctionReference) Name() string {
	switch fn := r.FunctionReference.(type) {
	case *FunctionDefinition:
		return fn.Name
	case *UnresolvedName:
		if fn.NumParts == 1 {
			return fn.Parts[0]
		}
		parts := make([]string, 0, fn.NumParts)
		for i := fn.NumParts - 1; i >= 0; i-- {
			parts = append(parts, fn.Parts[i])
		}
		return strings.Join(parts, ".")
	}
	return ""
}
