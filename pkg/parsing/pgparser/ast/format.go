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
	"bytes"
	"fmt"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/lex"
)

// FmtFlags carries options for the pretty-printer.
type FmtFlags uint8

// EncodeFlags returns the subset of the flags that are also lex encode flags.
func (f FmtFlags) EncodeFlags() lex.EncodeFlags {
	return lex.EncodeFlags(f)
}

// Basic bit definitions for the FmtFlags bitmask.
const (
	// FmtSimple instructs the pretty-printer to produce
	// a straightforward representation.
	FmtSimple FmtFlags = 0

	// FmtBareIdentifiers instructs the pretty-printer to print
	// identifiers without wrapping quotes in any case.
	FmtBareIdentifiers = FmtFlags(lex.EncBareIdentifiers)

	// FmtBacktickIdentifiers quotes identifiers that need quoting with backticks, for dialects that
	// read double quotes as strings.
	FmtBacktickIdentifiers = FmtFlags(lex.EncBacktickIdentifiers)
)

// FmtCtx is suitable for passing to Format() methods.
// It also exposes the underlying bytes.Buffer interface for
// convenience.
//
// FmtCtx cannot be copied by value.
type FmtCtx struct {
	bytes.Buffer

	// The flags to use for pretty-printing.
	flags FmtFlags
}

// NewFmtCtx creates a FmtCtx.
func NewFmtCtx(f FmtFlags) *FmtCtx {
	ctx := fmtCtxPool.Get().(*FmtCtx)
	ctx.flags = f
	return ctx
}

// WithFlags changes the flags in the FmtCtx, runs the given function, then
// restores the old flags.
func (ctx *FmtCtx) WithFlags(flags FmtFlags, fn func()) {
	oldFlags := ctx.flags
	ctx.flags = flags
	defer func() { ctx.flags = oldFlags }()

	fn()
}

// Printf calls fmt.Fprintf on the linked bytes.Buffer. It is provided
// for convenience, to avoid having to call fmt.Fprintf(&ctx.Buffer, ...).
//
// Note: DO NOT USE THIS TO INTERPOLATE %s ON NodeFormatter OBJECTS.
// This would call the String() method on them and would fail to reuse
// the same bytes buffer (and waste allocations). Instead use
// ctx.FormatNode().
func (ctx *FmtCtx) Printf(f string, args ...any) {
	fmt.Fprintf(&ctx.Buffer, f, args...)
}

// NodeFormatter is implemented by nodes that can be pretty-printed.
type NodeFormatter interface {
	// Format performs pretty-printing towards a bytes buffer.
	Format(ctx *FmtCtx)
}

// FormatNode recurses into a node for pretty-printing.
func (ctx *FmtCtx) FormatNode(n NodeFormatter) {
	n.Format(ctx)
}

// AsStringWithFlags pretty prints a node to a string given specific flags.
func AsStringWithFlags(n NodeFormatter, fl FmtFlags) string {
	ctx := NewFmtCtx(fl)
	ctx.FormatNode(n)
	return ctx.CloseAndGetString()
}

// AsString pretty prints a node to a string.
func AsString(n NodeFormatter) string {
	return AsStringWithFlags(n, FmtSimple)
}

var fmtCtxPool = sync.Pool{
	New: func() any {
		return &FmtCtx{}
	},
}

// Close releases a FmtCtx for reuse. Closing a FmtCtx is not required, but is
// recommended for performance-sensitive paths.
func (ctx *FmtCtx) Close() {
	ctx.Buffer.Reset()
	ctx.flags = FmtSimple
	fmtCtxPool.Put(ctx)
}

// CloseAndGetString combines Close() and String().
func (ctx *FmtCtx) CloseAndGetString() string {
	s := ctx.String()
	ctx.Close()
	return s
}
