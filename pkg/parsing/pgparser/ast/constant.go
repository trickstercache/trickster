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
	"math/big"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/lex"
)

const (
	hexPrefixLen = 2
	base10       = 10
	base16       = 16
	int64Bits    = 64
)

// NumVal represents a constant numeric value.
type NumVal struct {
	// negative is the sign bit to add to any interpretation of the
	// origString field.
	negative bool
	// origString is the "original" string representation (before
	// folding). This should remain sign-less.
	origString string
}

// NewNumVal constructs a new NumVal instance. This is used during parsing and
// in tests.
func NewNumVal(origString string, negative bool) *NumVal {
	return &NumVal{origString: origString, negative: negative}
}

// Format implements the NodeFormatter interface.
func (expr *NumVal) Format(ctx *FmtCtx) {
	s := expr.origString
	if strings.EqualFold(s, "NaN") {
		s = "'NaN'"
	}
	if expr.negative {
		ctx.WriteByte('-')
	}
	ctx.WriteString(s)
}

// Negate flips the sign of the value in place, which the parser uses to fold a unary minus
func (expr *NumVal) Negate() {
	expr.negative = !expr.negative
}

// AsInt64 returns the value as a 64-bit integer when it is integral and in range
func (expr *NumVal) AsInt64() (int64, error) {
	magnitude, ok := expr.magnitude()
	if !ok {
		return 0, assertionFailedf("numeric constant %q is not an int64", expr.origString)
	}
	if expr.negative {
		return -magnitude, nil
	}
	return magnitude, nil
}

func (expr *NumVal) magnitude() (int64, bool) {
	s := expr.origString
	if len(s) > hexPrefixLen && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		v, err := strconv.ParseInt(s[hexPrefixLen:], base16, int64Bits)
		return v, err == nil
	}
	if v, err := strconv.ParseInt(s, base10, int64Bits); err == nil {
		return v, true
	}
	// a decimal or exponent literal with an integral value, such as 1e3 or 2.0
	rat, ok := new(big.Rat).SetString(s)
	if !ok || !rat.IsInt() || !rat.Num().IsInt64() {
		return 0, false
	}
	return rat.Num().Int64(), true
}

// StrVal represents a constant string value.
type StrVal struct {
	s string

	// scannedAsBytes is true iff the input syntax was using b'...' or
	// x'....'. If false, the string is guaranteed to be a valid UTF-8
	// sequence.
	scannedAsBytes bool
	// fromField marks the field of an EXTRACT written as EXTRACT(field FROM source)
	fromField bool
	// written is a bytes literal as the statement spelled it, such as x'1F'
	written string
}

// NewBytesStrValWritten constructs a bytes literal that prints as the statement spelled it.
func NewBytesStrValWritten(s, written string) *StrVal {
	return &StrVal{s: s, scannedAsBytes: true, written: written}
}

// NewFieldStrVal constructs the field of an EXTRACT written with FROM, which prints the same way.
func NewFieldStrVal(field string) *StrVal {
	return &StrVal{s: field, fromField: true}
}

// NewStrVal constructs a StrVal instance. This is used during
// parsing when interpreting a token of type SCONST, i.e. *not* using
// the b'...' or x'...' syntax.
func NewStrVal(s string) *StrVal {
	return &StrVal{s: s}
}

// NewBytesStrVal constructs a StrVal instance suitable as byte array.
// This is used during parsing when interpreting a token of type BCONST,
// i.e. using the b'...' or x'...' syntax.
func NewBytesStrVal(s string) *StrVal {
	return &StrVal{s: s, scannedAsBytes: true}
}

// RawString retrieves the underlying string of the StrVal.
func (expr *StrVal) RawString() string {
	return expr.s
}

// Format implements the NodeFormatter interface.
func (expr *StrVal) Format(ctx *FmtCtx) {
	buf, f := &ctx.Buffer, ctx.flags
	if expr.scannedAsBytes && expr.written != "" {
		buf.WriteString(expr.written)
	} else if expr.scannedAsBytes {
		lex.EncodeSQLBytes(buf, expr.s)
	} else {
		lex.EncodeStandardSQLString(buf, expr.s, f.EncodeFlags())
	}
}
