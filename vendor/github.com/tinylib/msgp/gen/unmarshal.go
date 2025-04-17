package gen

import (
	"io"
	"strconv"
)

func unmarshal(w io.Writer) *unmarshalGen {
	return &unmarshalGen{
		p: printer{w: w},
	}
}

type unmarshalGen struct {
	passes
	p        printer
	hasfield bool
	ctx      *Context
}

func (u *unmarshalGen) Method() Method { return Unmarshal }

func (u *unmarshalGen) needsField() {
	if u.hasfield {
		return
	}
	u.p.print("\nvar field []byte; _ = field")
	u.hasfield = true
}

func (u *unmarshalGen) Execute(p Elem, ctx Context) error {
	u.hasfield = false
	u.ctx = &ctx
	if !u.p.ok() {
		return u.p.err
	}
	p = u.applyall(p)
	if p == nil {
		return nil
	}
	if !IsPrintable(p) {
		return nil
	}

	u.p.comment("UnmarshalMsg implements msgp.Unmarshaler")

	u.p.printf("\nfunc (%s %s) UnmarshalMsg(bts []byte) (o []byte, err error) {", p.Varname(), methodReceiver(p))
	next(u, p)
	u.p.print("\no = bts")
	u.p.nakedReturn()
	unsetReceiver(p)
	return u.p.err
}

// does assignment to the variable "name" with the type "base"
func (u *unmarshalGen) assignAndCheck(name string, base string) {
	if !u.p.ok() {
		return
	}
	u.p.printf("\n%s, bts, err = msgp.Read%sBytes(bts)", name, base)
	u.p.wrapErrCheck(u.ctx.ArgsStr())
}

func (u *unmarshalGen) gStruct(s *Struct) {
	if !u.p.ok() {
		return
	}
	if s.AsTuple {
		u.tuple(s)
	} else {
		u.mapstruct(s)
	}
}

func (u *unmarshalGen) tuple(s *Struct) {
	// open block
	sz := randIdent()
	u.p.declare(sz, u32)
	u.assignAndCheck(sz, arrayHeader)
	u.p.arrayCheck(strconv.Itoa(len(s.Fields)), sz)
	for i := range s.Fields {
		if !u.p.ok() {
			return
		}
		u.ctx.PushString(s.Fields[i].FieldName)
		fieldElem := s.Fields[i].FieldElem
		anField := s.Fields[i].HasTagPart("allownil") && fieldElem.AllowNil()
		if anField {
			u.p.printf("\nif msgp.IsNil(bts) {\nbts = bts[1:]\n%s = nil\n} else {", fieldElem.Varname())
		}
		SetIsAllowNil(fieldElem, anField)
		next(u, fieldElem)
		u.ctx.Pop()
		if anField {
			u.p.printf("\n}")
		}
	}
}

func (u *unmarshalGen) mapstruct(s *Struct) {
	u.needsField()
	sz := randIdent()
	u.p.declare(sz, u32)
	u.assignAndCheck(sz, mapHeader)

	oeCount := s.CountFieldTagPart("omitempty") + s.CountFieldTagPart("omitzero")
	if !u.ctx.clearOmitted {
		oeCount = 0
	}
	bm := bmask{
		bitlen:  oeCount,
		varname: sz + "Mask",
	}
	if oeCount > 0 {
		// Declare mask
		u.p.printf("\n%s", bm.typeDecl())
		u.p.printf("\n_ = %s", bm.varname)
	}
	// Index to field idx of each emitted
	oeEmittedIdx := []int{}

	u.p.printf("\nfor %s > 0 {", sz)
	u.p.printf("\n%s--; field, bts, err = msgp.ReadMapKeyZC(bts)", sz)
	u.p.wrapErrCheck(u.ctx.ArgsStr())
	u.p.print("\nswitch msgp.UnsafeString(field) {")
	for i := range s.Fields {
		if !u.p.ok() {
			return
		}
		u.p.printf("\ncase %q:", s.Fields[i].FieldTag)
		u.ctx.PushString(s.Fields[i].FieldName)

		fieldElem := s.Fields[i].FieldElem
		anField := s.Fields[i].HasTagPart("allownil") && fieldElem.AllowNil()
		if anField {
			u.p.printf("\nif msgp.IsNil(bts) {\nbts = bts[1:]\n%s = nil\n} else {", fieldElem.Varname())
		}
		SetIsAllowNil(fieldElem, anField)
		next(u, fieldElem)
		u.ctx.Pop()
		if oeCount > 0 && (s.Fields[i].HasTagPart("omitempty") || s.Fields[i].HasTagPart("omitzero")) {
			u.p.printf("\n%s", bm.setStmt(len(oeEmittedIdx)))
			oeEmittedIdx = append(oeEmittedIdx, i)
		}
		if anField {
			u.p.printf("\n}")
		}
	}
	u.p.print("\ndefault:\nbts, err = msgp.Skip(bts)")
	u.p.wrapErrCheck(u.ctx.ArgsStr())
	u.p.print("\n}\n}") // close switch and for loop
	if oeCount > 0 {
		u.p.printf("\n// Clear omitted fields.\n")
		if bm.bitlen > 1 {
			u.p.printf("if %s {\n", bm.notAllSet())
		}
		for bitIdx, fieldIdx := range oeEmittedIdx {
			fieldElem := s.Fields[fieldIdx].FieldElem

			u.p.printf("if %s == 0 {\n", bm.readExpr(bitIdx))
			fze := fieldElem.ZeroExpr()
			if fze != "" {
				u.p.printf("%s = %s\n", fieldElem.Varname(), fze)
			} else {
				u.p.printf("%s = %s{}\n", fieldElem.Varname(), fieldElem.TypeName())
			}
			u.p.printf("}\n")
		}
		if bm.bitlen > 1 {
			u.p.printf("}")
		}
	}
}

func (u *unmarshalGen) gBase(b *BaseElem) {
	if !u.p.ok() {
		return
	}

	refname := b.Varname() // assigned to
	lowered := b.Varname() // passed as argument
	// begin 'tmp' block
	if b.Convert && b.Value != IDENT { // we don't need block for 'tmp' in case of IDENT
		refname = randIdent()
		lowered = b.ToBase() + "(" + lowered + ")"
		u.p.printf("\n{\nvar %s %s", refname, b.BaseType())
	}

	switch b.Value {
	case Bytes:
		u.p.printf("\n%s, bts, err = msgp.ReadBytesBytes(bts, %s)", refname, lowered)
	case Ext:
		u.p.printf("\nbts, err = msgp.ReadExtensionBytes(bts, %s)", lowered)
	case IDENT:
		if b.Convert {
			lowered = b.ToBase() + "(" + lowered + ")"
		}
		u.p.printf("\nbts, err = %s.UnmarshalMsg(bts)", lowered)
	default:
		u.p.printf("\n%s, bts, err = msgp.Read%sBytes(bts)", refname, b.BaseName())
	}
	u.p.wrapErrCheck(u.ctx.ArgsStr())

	if b.Value == Bytes && b.AllowNil() {
		// Ensure that 0 sized slices are allocated.
		u.p.printf("\nif %s == nil {\n%s = make([]byte, 0)\n}", refname, refname)
	}

	// close 'tmp' block
	if b.Convert && b.Value != IDENT {
		if b.ShimMode == Cast {
			u.p.printf("\n%s = %s(%s)\n", b.Varname(), b.FromBase(), refname)
		} else {
			u.p.printf("\n%s, err = %s(%s)\n", b.Varname(), b.FromBase(), refname)
			u.p.wrapErrCheck(u.ctx.ArgsStr())
		}
		u.p.printf("}")
	}
}

func (u *unmarshalGen) gArray(a *Array) {
	if !u.p.ok() {
		return
	}

	// special case for [const]byte objects
	// see decode.go for symmetry
	if be, ok := a.Els.(*BaseElem); ok && be.Value == Byte {
		u.p.printf("\nbts, err = msgp.ReadExactBytes(bts, (%s)[:])", a.Varname())
		u.p.wrapErrCheck(u.ctx.ArgsStr())
		return
	}

	sz := randIdent()
	u.p.declare(sz, u32)
	u.assignAndCheck(sz, arrayHeader)
	u.p.arrayCheck(coerceArraySize(a.Size), sz)
	u.p.rangeBlock(u.ctx, a.Index, a.Varname(), u, a.Els)
}

func (u *unmarshalGen) gSlice(s *Slice) {
	if !u.p.ok() {
		return
	}
	sz := randIdent()
	u.p.declare(sz, u32)
	u.assignAndCheck(sz, arrayHeader)
	if s.isAllowNil {
		u.p.resizeSliceNoNil(sz, s)
	} else {
		u.p.resizeSlice(sz, s)
	}
	u.p.rangeBlock(u.ctx, s.Index, s.Varname(), u, s.Els)
}

func (u *unmarshalGen) gMap(m *Map) {
	if !u.p.ok() {
		return
	}
	sz := randIdent()
	u.p.declare(sz, u32)
	u.assignAndCheck(sz, mapHeader)

	// allocate or clear map
	u.p.resizeMap(sz, m)

	// We likely need a field.
	// Add now to not be inside for scope.
	u.needsField()

	// loop and get key,value
	u.p.printf("\nfor %s > 0 {", sz)
	u.p.printf("\nvar %s string; var %s %s; %s--", m.Keyidx, m.Validx, m.Value.TypeName(), sz)
	u.assignAndCheck(m.Keyidx, stringTyp)
	u.ctx.PushVar(m.Keyidx)
	m.Value.SetIsAllowNil(false)
	next(u, m.Value)
	u.ctx.Pop()
	u.p.mapAssign(m)
	u.p.closeblock()
}

func (u *unmarshalGen) gPtr(p *Ptr) {
	u.p.printf("\nif msgp.IsNil(bts) { bts, err = msgp.ReadNilBytes(bts); if err != nil { return }; %s = nil; } else { ", p.Varname())
	u.p.initPtr(p)
	next(u, p.Value)
	u.p.closeblock()
}
