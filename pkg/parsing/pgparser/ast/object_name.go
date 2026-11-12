// Copyright 2020 The Cockroach Authors.
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

import "fmt"

// objName is the internal type for a qualified object.
type objName struct {
	// ObjectName is the unqualified name for the object
	// (table/view/sequence/function/type).
	ObjectName Name

	// ObjectNamePrefix is the path to the object.  This can be modified
	// further by name resolution, see name_resolution.go.
	ObjectNamePrefix
}

func (o *objName) Object() string {
	return string(o.ObjectName)
}

func (o *objName) String() string { return AsString(o) }

// Format implements the NodeFormatter interface.
func (o *objName) Format(ctx *FmtCtx) {
	ctx.FormatNode(&o.ObjectNamePrefix)
	if o.ExplicitSchema {
		ctx.WriteByte('.')
	}
	ctx.FormatNode(&o.ObjectName)
}

// ObjectNamePrefix corresponds to the path prefix of an object name.
type ObjectNamePrefix struct {
	CatalogName Name
	SchemaName  Name

	// ExplicitCatalog is true iff the catalog was explicitly specified
	// or it needs to be rendered during pretty-printing.
	ExplicitCatalog bool
	// ExplicitSchema is true iff the schema was explicitly specified
	// or it needs to be rendered during pretty-printing.
	ExplicitSchema bool
}

// Format implements the NodeFormatter interface.
func (tp *ObjectNamePrefix) Format(ctx *FmtCtx) {
	if tp.ExplicitSchema {
		if tp.ExplicitCatalog {
			ctx.FormatNode(&tp.CatalogName)
			ctx.WriteByte('.')
		}
		ctx.FormatNode(&tp.SchemaName)
	}
}

func (tp *ObjectNamePrefix) String() string { return AsString(tp) }

// Schema retrieves the unqualified schema name.
func (tp *ObjectNamePrefix) Schema() string {
	return string(tp.SchemaName)
}

// Catalog retrieves the unqualified catalog name.
func (tp *ObjectNamePrefix) Catalog() string {
	return string(tp.CatalogName)
}

// UnresolvedObjectName is an unresolved qualified name for a database object
// (table, view, etc). It is like UnresolvedName but more restrictive.
// It should only be constructed via NewUnresolvedObjectName.
type UnresolvedObjectName struct {
	// NumParts indicates the number of name parts specified; always 1 or greater.
	NumParts int

	// Parts are the name components, in reverse order.
	// There are at most 3: object name, schema, catalog/db.
	//
	// Note: Parts has a fixed size so that we avoid a heap allocation for the
	// slice every time we construct an UnresolvedObjectName. It does imply
	// however that Parts does not have a meaningful "length"; its actual length
	// (the number of parts specified) is populated in NumParts above.
	Parts [3]string

	// UnresolvedObjectName can be annotated with a *tree.TableName.
}

// UnresolvedObjectName implements TableExpr.
func (*UnresolvedObjectName) tableExpr() {}

// NewUnresolvedObjectName creates an unresolved object name, verifying that it
// is well-formed.
func NewUnresolvedObjectName(
	numParts int, parts [3]string,
) (*UnresolvedObjectName, error) {
	n, err := MakeUnresolvedObjectName(numParts, parts)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// MakeUnresolvedObjectName creates an unresolved object name, verifying that it
// is well-formed.
func MakeUnresolvedObjectName(
	numParts int, parts [3]string,
) (UnresolvedObjectName, error) {
	u := UnresolvedObjectName{
		NumParts: numParts,
		Parts:    parts,
	}
	if u.NumParts < 1 {
		forErr := u // prevents u from escaping
		return UnresolvedObjectName{}, newInvTableNameError(&forErr)
	}

	// Check that all the parts specified are not empty.
	// It's OK if the catalog name is empty.
	lastCheck := min(u.NumParts, 2)
	for i := range lastCheck {
		if len(u.Parts[i]) == 0 {
			forErr := u // prevents u from escaping
			return UnresolvedObjectName{}, newInvTableNameError(&forErr)
		}
	}
	return u, nil
}

// Format implements the NodeFormatter interface.
func (u *UnresolvedObjectName) Format(ctx *FmtCtx) {
	for i := u.NumParts; i > 0; i-- {
		// The first part to print is the last item in u.Parts. It is also
		// a potentially restricted name to disambiguate from keywords in
		// the grammar, so print it out as a "Name". Every part after that is
		// necessarily an unrestricted name.
		if i == u.NumParts {
			ctx.FormatNode((*Name)(&u.Parts[i-1]))
		} else {
			ctx.WriteByte('.')
			ctx.FormatNode((*UnrestrictedName)(&u.Parts[i-1]))
		}
	}
}

func (u *UnresolvedObjectName) String() string { return AsString(u) }

func (u *UnresolvedObjectName) toObjName() objName {
	return objName{
		ObjectName:      Name(u.Parts[0]),
		SchemaName:      Name(u.Parts[1]),
		CatalogName:     Name(u.Parts[2]),
		ExplicitSchema:  u.NumParts >= 2,
		ExplicitCatalog: u.NumParts >= 3,
	}
}

// ToTableName converts the unresolved name to a table name.
func (u *UnresolvedObjectName) ToTableName() TableName {
	return TableName{u.toObjName()}
}

// Object returns the unqualified object name.
func (u *UnresolvedObjectName) Object() string {
	return u.Parts[0]
}

// Schema returns the schema of the object.
func (u *UnresolvedObjectName) Schema() string {
	return u.Parts[1]
}

// Catalog returns the catalog of the object.
func (u *UnresolvedObjectName) Catalog() string {
	return u.Parts[2]
}

func newInvTableNameError(n fmt.Stringer) error {
	return fmt.Errorf("invalid table name: %s", n)
}
