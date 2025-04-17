package gen

import (
	"fmt"
	"io"

	"github.com/tinylib/msgp/msgp"
)

func encode(w io.Writer) *encodeGen {
	return &encodeGen{
		p: printer{w: w},
	}
}

type encodeGen struct {
	passes
	p    printer
	fuse []byte
	ctx  *Context
}

func (e *encodeGen) Method() Method { return Encode }

func (e *encodeGen) Apply(dirs []string) error {
	return nil
}

func (e *encodeGen) writeAndCheck(typ string, argfmt string, arg interface{}) {
	if e.ctx.compFloats && typ == "Float64" {
		typ = "Float"
	}
	if e.ctx.newTime && typ == "Time" {
		typ = "TimeExt"
	}

	e.p.printf("\nerr = en.Write%s(%s)", typ, fmt.Sprintf(argfmt, arg))
	e.p.wrapErrCheck(e.ctx.ArgsStr())
}

func (e *encodeGen) fuseHook() {
	if len(e.fuse) > 0 {
		e.appendraw(e.fuse)
		e.fuse = e.fuse[:0]
	}
}

func (e *encodeGen) Fuse(b []byte) {
	if len(e.fuse) > 0 {
		e.fuse = append(e.fuse, b...)
	} else {
		e.fuse = b
	}
}

func (e *encodeGen) Execute(p Elem, ctx Context) error {
	e.ctx = &ctx
	if !e.p.ok() {
		return e.p.err
	}
	p = e.applyall(p)
	if p == nil {
		return nil
	}
	if !IsPrintable(p) {
		return nil
	}

	e.p.comment("EncodeMsg implements msgp.Encodable")
	rcv := imutMethodReceiver(p)
	ogVar := p.Varname()
	if p.AlwaysPtr(nil) {
		rcv = methodReceiver(p)
	}
	e.p.printf("\nfunc (%s %s) EncodeMsg(en *msgp.Writer) (err error) {", ogVar, rcv)
	next(e, p)
	if p.AlwaysPtr(nil) {
		p.SetVarname(ogVar)
	}
	e.p.nakedReturn()
	return e.p.err
}

func (e *encodeGen) gStruct(s *Struct) {
	if !e.p.ok() {
		return
	}
	if s.AsTuple {
		e.tuple(s)
	} else {
		e.structmap(s)
	}
}

func (e *encodeGen) tuple(s *Struct) {
	nfields := len(s.Fields)
	data := msgp.AppendArrayHeader(nil, uint32(nfields))
	e.p.printf("\n// array header, size %d", nfields)
	e.Fuse(data)
	if len(s.Fields) == 0 {
		e.fuseHook()
	}
	for i := range s.Fields {
		if !e.p.ok() {
			return
		}
		fieldElem := s.Fields[i].FieldElem
		anField := s.Fields[i].HasTagPart("allownil") && fieldElem.AllowNil()
		if anField {
			e.p.printf("\nif %s { // allownil: if nil", fieldElem.IfZeroExpr())
			e.p.printf("\nerr = en.WriteNil(); if err != nil { return; }")
			e.p.printf("\n} else {")
		}
		SetIsAllowNil(fieldElem, anField)
		e.ctx.PushString(s.Fields[i].FieldName)
		next(e, s.Fields[i].FieldElem)
		e.ctx.Pop()
		if anField {
			e.p.print("\n}") // close if statement
		}
	}
}

func (e *encodeGen) appendraw(bts []byte) {
	e.p.print("\nerr = en.Append(")
	for i, b := range bts {
		if i != 0 {
			e.p.print(", ")
		}
		e.p.printf("0x%x", b)
	}
	e.p.print(")\nif err != nil { return }")
}

func (e *encodeGen) structmap(s *Struct) {
	oeIdentPrefix := randIdent()

	var data []byte
	nfields := len(s.Fields)
	bm := bmask{
		bitlen:  nfields,
		varname: oeIdentPrefix + "Mask",
	}

	omitempty := s.AnyHasTagPart("omitempty")
	omitzero := s.AnyHasTagPart("omitzero")
	var closeZero bool
	var fieldNVar string
	if omitempty || omitzero {

		fieldNVar = oeIdentPrefix + "Len"

		e.p.printf("\n// check for omitted fields")
		e.p.printf("\n%s := uint32(%d)", fieldNVar, nfields)
		e.p.printf("\n%s", bm.typeDecl())
		e.p.printf("\n_ = %s", bm.varname)
		for i, sf := range s.Fields {
			if !e.p.ok() {
				return
			}
			if ize := sf.FieldElem.IfZeroExpr(); ize != "" && sf.HasTagPart("omitempty") {
				e.p.printf("\nif %s {", ize)
				e.p.printf("\n%s--", fieldNVar)
				e.p.printf("\n%s", bm.setStmt(i))
				e.p.printf("\n}")
			} else if sf.HasTagPart("omitzero") {
				e.p.printf("\nif %s.IsZero() {", sf.FieldElem.Varname())
				e.p.printf("\n%s--", fieldNVar)
				e.p.printf("\n%s", bm.setStmt(i))
				e.p.printf("\n}")
			}
		}

		e.p.printf("\n// variable map header, size %s", fieldNVar)
		e.p.varWriteMapHeader("en", fieldNVar, nfields)
		e.p.print("\nif err != nil { return }")
		if !e.p.ok() {
			return
		}

		// Skip block, if no fields are set.
		if nfields > 1 {
			e.p.printf("\n\n// skip if no fields are to be emitted")
			e.p.printf("\nif %s != 0 {", fieldNVar)
			closeZero = true
		}

	} else {

		// non-omit version
		data = msgp.AppendMapHeader(nil, uint32(nfields))
		e.p.printf("\n// map header, size %d", nfields)
		e.Fuse(data)
		if len(s.Fields) == 0 {
			e.p.printf("\n_ = %s", s.vname)
			e.fuseHook()
		}

	}

	for i := range s.Fields {
		if !e.p.ok() {
			return
		}

		// if field is omitempty or omitzero, wrap with if statement based on the emptymask
		oeField := (omitempty || omitzero) &&
			((s.Fields[i].HasTagPart("omitempty") && s.Fields[i].FieldElem.IfZeroExpr() != "") ||
				s.Fields[i].HasTagPart("omitzero"))
		if oeField {
			e.p.printf("\nif %s == 0 { // if not omitted", bm.readExpr(i))
		}

		data = msgp.AppendString(nil, s.Fields[i].FieldTag)
		e.p.printf("\n// write %q", s.Fields[i].FieldTag)
		e.Fuse(data)
		e.fuseHook()
		fieldElem := s.Fields[i].FieldElem
		anField := !oeField && s.Fields[i].HasTagPart("allownil") && fieldElem.AllowNil()
		if anField {
			e.p.printf("\nif %s { // allownil: if nil", s.Fields[i].FieldElem.IfZeroExpr())
			e.p.printf("\nerr = en.WriteNil(); if err != nil { return; }")
			e.p.printf("\n} else {")
		}
		SetIsAllowNil(fieldElem, anField)

		e.ctx.PushString(s.Fields[i].FieldName)
		next(e, s.Fields[i].FieldElem)
		e.ctx.Pop()

		if oeField || anField {
			e.p.print("\n}") // close if statement
		}
	}
	if closeZero {
		e.p.printf("\n}") // close if statement
	}
}

func (e *encodeGen) gMap(m *Map) {
	if !e.p.ok() {
		return
	}
	e.fuseHook()
	vname := m.Varname()
	e.writeAndCheck(mapHeader, lenAsUint32, vname)

	e.p.printf("\nfor %s, %s := range %s {", m.Keyidx, m.Validx, vname)
	e.writeAndCheck(stringTyp, literalFmt, m.Keyidx)
	e.ctx.PushVar(m.Keyidx)
	m.Value.SetIsAllowNil(false)
	next(e, m.Value)
	e.ctx.Pop()
	e.p.closeblock()
}

func (e *encodeGen) gPtr(s *Ptr) {
	if !e.p.ok() {
		return
	}
	e.fuseHook()
	e.p.printf("\nif %s == nil { err = en.WriteNil(); if err != nil { return; } } else {", s.Varname())
	next(e, s.Value)
	e.p.closeblock()
}

func (e *encodeGen) gSlice(s *Slice) {
	if !e.p.ok() {
		return
	}
	e.fuseHook()
	e.writeAndCheck(arrayHeader, lenAsUint32, s.Varname())
	e.p.rangeBlock(e.ctx, s.Index, s.Varname(), e, s.Els)
}

func (e *encodeGen) gArray(a *Array) {
	if !e.p.ok() {
		return
	}
	e.fuseHook()
	// shortcut for [const]byte
	if be, ok := a.Els.(*BaseElem); ok && (be.Value == Byte || be.Value == Uint8) {
		e.p.printf("\nerr = en.WriteBytes((%s)[:])", a.Varname())
		e.p.wrapErrCheck(e.ctx.ArgsStr())
		return
	}

	e.writeAndCheck(arrayHeader, literalFmt, coerceArraySize(a.Size))
	e.p.rangeBlock(e.ctx, a.Index, a.Varname(), e, a.Els)
}

func (e *encodeGen) gBase(b *BaseElem) {
	if !e.p.ok() {
		return
	}
	e.fuseHook()
	vname := b.Varname()
	if b.Convert {
		if b.ShimMode == Cast {
			vname = tobaseConvert(b)
		} else {
			vname = randIdent()
			e.p.printf("\nvar %s %s", vname, b.BaseType())
			e.p.printf("\n%s, err = %s", vname, tobaseConvert(b))
			e.p.wrapErrCheck(e.ctx.ArgsStr())
		}
	}

	if b.Value == IDENT { // unknown identity
		e.p.printf("\nerr = %s.EncodeMsg(en)", vname)
		e.p.wrapErrCheck(e.ctx.ArgsStr())
	} else { // typical case
		e.writeAndCheck(b.BaseName(), literalFmt, vname)
	}
}
