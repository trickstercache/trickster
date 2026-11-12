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

// ResolvableTypeReference represents a type that is possibly unknown
// until type-checking/type name resolution is performed.
// N.B. ResolvableTypeReferences in expressions must be formatted with
// FormatTypeReference instead of SQLString.
type ResolvableTypeReference interface {
	SQLString() string
}

// FormatTypeReference formats a ResolvableTypeReference.
func (ctx *FmtCtx) FormatTypeReference(ref ResolvableTypeReference) {
	switch t := ref.(type) {
	case *T:
		ctx.WriteString(t.SQLString())

	// All other ResolvableTypeReferences must implement NodeFormatter.
	case NodeFormatter:
		ctx.FormatNode(t)

	default:
		panic(assertionFailedf("type reference %T must implement NodeFormatter", ref))
	}
}

// GetStaticallyKnownType possibly promotes a ResolvableTypeReference into a
// *T if the reference is a statically known type. It is only safe to
// access the returned type if ok is true.
func GetStaticallyKnownType(ref ResolvableTypeReference) (typ *T, ok bool) {
	if written, isWritten := ref.(*WrittenType); isWritten {
		ref = written.Type
	}
	typ, ok = ref.(*T)
	return typ, ok
}

// WrittenType is a type reference that prints as the statement spelled it, so a type name reaches
// the origin unchanged; Type holds what the parser read it as.
type WrittenType struct {
	Type ResolvableTypeReference
	Text string
}

// WriteType wraps a type reference with its written spelling, or returns it as is when text is empty.
func WriteType(ref ResolvableTypeReference, text string) ResolvableTypeReference {
	if text == "" {
		return ref
	}
	return &WrittenType{Type: ref, Text: text}
}

// Format implements the NodeFormatter interface.
func (w *WrittenType) Format(ctx *FmtCtx) { ctx.WriteString(w.Text) }

// SQLString implements the ResolvableTypeReference interface.
func (w *WrittenType) SQLString() string { return w.Text }

// SQLString implements the ResolvableTypeReference interface.
func (name *UnresolvedObjectName) SQLString() string {
	return AsString(name)
}

// ArrayTypeReference represents an array of possibly unknown type references.
type ArrayTypeReference struct {
	ElementType ResolvableTypeReference
}

// Format implements the NodeFormatter interface.
func (node *ArrayTypeReference) Format(ctx *FmtCtx) {
	if typ, ok := GetStaticallyKnownType(node.ElementType); ok {
		ctx.FormatTypeReference(MakeTypeArray(typ))
	} else {
		ctx.FormatTypeReference(node.ElementType)
		ctx.WriteString("[]")
	}
}

// SQLString implements the ResolvableTypeReference interface.
func (node *ArrayTypeReference) SQLString() string {
	return AsString(node)
}
