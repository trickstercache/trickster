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

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/lex"
)

// A Name is an SQL identifier.
//
// In general, a Name is the result of parsing a name nonterminal, which is used
// in the grammar where reserved keywords cannot be distinguished from
// identifiers. A Name that matches a reserved keyword must thus be quoted when
// formatted. (Names also need quoting for a variety of other reasons; see
// isBareIdentifier.)
//
// For historical reasons, some Names are instead the result of parsing
// `unrestricted_name` nonterminals. See UnrestrictedName for details.
type Name string

// Format implements the NodeFormatter interface.
func (n *Name) Format(ctx *FmtCtx) {
	lex.EncodeRestrictedSQLIdent(&ctx.Buffer, string(*n), ctx.flags.EncodeFlags())
}

// An UnrestrictedName is a Name that does not need to be escaped when it
// matches a reserved keyword.
//
// In general, an UnrestrictedName is the result of parsing an unrestricted_name
// nonterminal, which is used in the grammar where reserved keywords can be
// unambiguously interpreted as identifiers. When formatted, an UnrestrictedName
// that matches a reserved keyword thus does not need to be quoted.
//
// For historical reasons, some unrestricted_name nonterminals are instead
// parsed as Names. The only user-visible impact of this is that we are too
// aggressive about quoting names in certain positions. New grammar rules should
// prefer to parse unrestricted_name nonterminals into UnrestrictedNames.
type UnrestrictedName string

// Format implements the NodeFormatter interface.
func (u *UnrestrictedName) Format(ctx *FmtCtx) {
	lex.EncodeUnrestrictedSQLIdent(&ctx.Buffer, string(*u), ctx.flags.EncodeFlags())
}

// A NameList is a list of identifiers.
type NameList []Name

// Format implements the NodeFormatter interface.
func (l *NameList) Format(ctx *FmtCtx) {
	for i := range *l {
		if i > 0 {
			ctx.WriteString(", ")
		}
		ctx.FormatNode(&(*l)[i])
	}
}

// ArraySubscript corresponds to the syntax `<name>[ ... ]`.
type ArraySubscript struct {
	Begin Expr
	End   Expr
	Slice bool
}

// Format implements the NodeFormatter interface.
func (a *ArraySubscript) Format(ctx *FmtCtx) {
	ctx.WriteByte('[')
	if a.Begin != nil {
		ctx.FormatNode(a.Begin)
	}
	if a.Slice {
		ctx.WriteByte(':')
		if a.End != nil {
			ctx.FormatNode(a.End)
		}
	}
	ctx.WriteByte(']')
}

// UnresolvedName corresponds to an unresolved qualified name.
type UnresolvedName struct {
	// NumParts indicates the number of name parts specified, including
	// the star. Always 1 or greater.
	NumParts int

	// Star indicates the name ends with a star.
	// In that case, Parts below is empty in the first position.
	Star bool

	// Parts are the name components, in reverse order.
	// There are at most 4: column, table, schema, catalog/db.
	//
	// Note: NameParts has a fixed size so that we avoid a heap
	// allocation for the slice every time we construct an
	// UnresolvedName. It does imply however that Parts does not have
	// a meaningful "length"; its actual length (the number of parts
	// specified) is populated in NumParts above.
	Parts NameParts
}

// NameParts is the array of strings that composes the path in an
// UnresolvedName.
type NameParts = [4]string

// Format implements the NodeFormatter interface.
func (u *UnresolvedName) Format(ctx *FmtCtx) {
	stopAt := 1
	if u.Star {
		stopAt = 2
	}
	for i := u.NumParts; i >= stopAt; i-- {
		// The first part to print is the last item in u.Parts.  It is also
		// a potentially restricted name to disambiguate from keywords in
		// the grammar, so print it out as a "Name". Every part after that is
		// necessarily an unrestricted name.
		if i == u.NumParts {
			ctx.FormatNode((*Name)(&u.Parts[i-1]))
		} else {
			ctx.FormatNode((*UnrestrictedName)(&u.Parts[i-1]))
		}
		if i > 1 {
			ctx.WriteByte('.')
		}
	}
	if u.Star {
		ctx.WriteByte('*')
	}
}

func (u *UnresolvedName) String() string { return AsString(u) }

// NewUnresolvedName constructs an UnresolvedName from some strings.
func NewUnresolvedName(args ...string) *UnresolvedName {
	n := MakeUnresolvedName(args...)
	return &n
}

// MakeUnresolvedName constructs an UnresolvedName from some strings.
func MakeUnresolvedName(args ...string) UnresolvedName {
	n := UnresolvedName{NumParts: len(args)}
	for i := range args {
		n.Parts[i] = args[len(args)-1-i]
	}
	return n
}

// ToUnresolvedObjectName converts an UnresolvedName to an UnresolvedObjectName.
func (u *UnresolvedName) ToUnresolvedObjectName() (UnresolvedObjectName, error) {
	if u.NumParts == 4 {
		return UnresolvedObjectName{}, fmt.Errorf("improper qualified name (too many dotted names): %s", u)
	}
	return MakeUnresolvedObjectName(
		u.NumParts,
		[3]string{u.Parts[0], u.Parts[1], u.Parts[2]},
	)
}
