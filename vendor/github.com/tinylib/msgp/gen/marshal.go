package gen

import (
	"fmt"
	"io"

	"github.com/tinylib/msgp/msgp"
)

func marshal(w io.Writer) *marshalGen {
	return &marshalGen{
		p: printer{w: w},
	}
}

type marshalGen struct {
	passes
	p    printer
	fuse []byte
	ctx  *Context
}

func (m *marshalGen) Method() Method { return Marshal }

func (m *marshalGen) Apply(dirs []string) error {
	return nil
}

func (m *marshalGen) Execute(p Elem, ctx Context) error {
	m.ctx = &ctx
	if !m.p.ok() {
		return m.p.err
	}
	p = m.applyall(p)
	if p == nil {
		return nil
	}
	if !IsPrintable(p) {
		return nil
	}

	m.p.comment("MarshalMsg implements msgp.Marshaler")

	// save the vname before
	// calling methodReceiver so
	// that z.Msgsize() is printed correctly
	c := p.Varname()
	rcv := imutMethodReceiver(p)
	ogVar := p.Varname()
	if p.AlwaysPtr(nil) {
		rcv = methodReceiver(p)
	}
	m.p.printf("\nfunc (%s %s) MarshalMsg(b []byte) (o []byte, err error) {", ogVar, rcv)
	m.p.printf("\no = msgp.Require(b, %s.Msgsize())", c)
	next(m, p)
	if p.AlwaysPtr(nil) {
		p.SetVarname(ogVar)
	}

	m.p.nakedReturn()
	return m.p.err
}

func (m *marshalGen) rawAppend(typ string, argfmt string, arg interface{}) {
	if m.ctx.compFloats && typ == "Float64" {
		typ = "Float"
	}
	if m.ctx.newTime && typ == "Time" {
		typ = "TimeExt"
	}

	m.p.printf("\no = msgp.Append%s(o, %s)", typ, fmt.Sprintf(argfmt, arg))
}

func (m *marshalGen) fuseHook() {
	if len(m.fuse) > 0 {
		m.rawbytes(m.fuse)
		m.fuse = m.fuse[:0]
	}
}

func (m *marshalGen) Fuse(b []byte) {
	if len(m.fuse) == 0 {
		m.fuse = b
	} else {
		m.fuse = append(m.fuse, b...)
	}
}

func (m *marshalGen) gStruct(s *Struct) {
	if !m.p.ok() {
		return
	}

	if s.AsTuple {
		m.tuple(s)
	} else {
		m.mapstruct(s)
	}
}

func (m *marshalGen) tuple(s *Struct) {
	data := make([]byte, 0, 5)
	data = msgp.AppendArrayHeader(data, uint32(len(s.Fields)))
	m.p.printf("\n// array header, size %d", len(s.Fields))
	m.Fuse(data)
	if len(s.Fields) == 0 {
		m.fuseHook()
	}
	for i := range s.Fields {
		if !m.p.ok() {
			return
		}
		fieldElem := s.Fields[i].FieldElem
		anField := s.Fields[i].HasTagPart("allownil") && fieldElem.AllowNil()
		if anField {
			m.p.printf("\nif %s { // allownil: if nil", fieldElem.IfZeroExpr())
			m.p.printf("\no = msgp.AppendNil(o)")
			m.p.printf("\n} else {")
		}
		m.ctx.PushString(s.Fields[i].FieldName)
		SetIsAllowNil(fieldElem, anField)
		next(m, fieldElem)
		m.ctx.Pop()
		if anField {
			m.p.printf("\n}") // close if statement
		}
	}
}

func (m *marshalGen) mapstruct(s *Struct) {
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

		m.p.printf("\n// check for omitted fields")
		m.p.printf("\n%s := uint32(%d)", fieldNVar, nfields)
		m.p.printf("\n%s", bm.typeDecl())
		m.p.printf("\n_ = %s", bm.varname)
		for i, sf := range s.Fields {
			if !m.p.ok() {
				return
			}
			if ize := sf.FieldElem.IfZeroExpr(); ize != "" && sf.HasTagPart("omitempty") {
				m.p.printf("\nif %s {", ize)
				m.p.printf("\n%s--", fieldNVar)
				m.p.printf("\n%s", bm.setStmt(i))
				m.p.printf("\n}")
			} else if sf.HasTagPart("omitzero") {
				m.p.printf("\nif %s.IsZero() {", sf.FieldElem.Varname())
				m.p.printf("\n%s--", fieldNVar)
				m.p.printf("\n%s", bm.setStmt(i))
				m.p.printf("\n}")
			}
		}

		m.p.printf("\n// variable map header, size %s", fieldNVar)
		m.p.varAppendMapHeader("o", fieldNVar, nfields)
		if !m.p.ok() {
			return
		}

		// Skip block, if no fields are set.
		if nfields > 1 {
			m.p.printf("\n\n// skip if no fields are to be emitted")
			m.p.printf("\nif %s != 0 {", fieldNVar)
			closeZero = true
		}

	} else {

		// non-omitempty version
		data = make([]byte, 0, 64)
		data = msgp.AppendMapHeader(data, uint32(len(s.Fields)))
		m.p.printf("\n// map header, size %d", len(s.Fields))
		m.Fuse(data)
		if len(s.Fields) == 0 {
			m.p.printf("\n_ = %s", s.vname)
			m.fuseHook()
		}

	}

	for i := range s.Fields {
		if !m.p.ok() {
			return
		}

		// if field is omitempty or omitzero, wrap with if statement based on the emptymask
		oeField := (omitempty || omitzero) &&
			((s.Fields[i].HasTagPart("omitempty") && s.Fields[i].FieldElem.IfZeroExpr() != "") ||
				s.Fields[i].HasTagPart("omitzero"))
		if oeField {
			m.p.printf("\nif %s == 0 { // if not omitted", bm.readExpr(i))
		}

		data = msgp.AppendString(nil, s.Fields[i].FieldTag)

		m.p.printf("\n// string %q", s.Fields[i].FieldTag)
		m.Fuse(data)
		m.fuseHook()

		fieldElem := s.Fields[i].FieldElem
		anField := !oeField && s.Fields[i].HasTagPart("allownil") && fieldElem.AllowNil()
		if anField {
			m.p.printf("\nif %s { // allownil: if nil", fieldElem.IfZeroExpr())
			m.p.printf("\no = msgp.AppendNil(o)")
			m.p.printf("\n} else {")
		}
		m.ctx.PushString(s.Fields[i].FieldName)
		SetIsAllowNil(fieldElem, anField)
		next(m, fieldElem)
		m.ctx.Pop()

		if oeField || anField {
			m.p.printf("\n}") // close if statement
		}
	}
	if closeZero {
		m.p.printf("\n}") // close if statement
	}
}

// append raw data
func (m *marshalGen) rawbytes(bts []byte) {
	m.p.print("\no = append(o, ")
	for _, b := range bts {
		m.p.printf("0x%x,", b)
	}
	m.p.print(")")
}

func (m *marshalGen) gMap(s *Map) {
	if !m.p.ok() {
		return
	}
	m.fuseHook()
	vname := s.Varname()
	m.rawAppend(mapHeader, lenAsUint32, vname)
	m.p.printf("\nfor %s, %s := range %s {", s.Keyidx, s.Validx, vname)
	m.rawAppend(stringTyp, literalFmt, s.Keyidx)
	m.ctx.PushVar(s.Keyidx)
	s.Value.SetIsAllowNil(false)
	next(m, s.Value)
	m.ctx.Pop()
	m.p.closeblock()
}

func (m *marshalGen) gSlice(s *Slice) {
	if !m.p.ok() {
		return
	}
	m.fuseHook()
	vname := s.Varname()
	m.rawAppend(arrayHeader, lenAsUint32, vname)
	m.p.rangeBlock(m.ctx, s.Index, vname, m, s.Els)
}

func (m *marshalGen) gArray(a *Array) {
	if !m.p.ok() {
		return
	}
	m.fuseHook()
	if be, ok := a.Els.(*BaseElem); ok && be.Value == Byte {
		m.rawAppend("Bytes", "(%s)[:]", a.Varname())
		return
	}

	m.rawAppend(arrayHeader, literalFmt, coerceArraySize(a.Size))
	m.p.rangeBlock(m.ctx, a.Index, a.Varname(), m, a.Els)
}

func (m *marshalGen) gPtr(p *Ptr) {
	if !m.p.ok() {
		return
	}
	m.fuseHook()
	m.p.printf("\nif %s == nil {\no = msgp.AppendNil(o)\n} else {", p.Varname())
	next(m, p.Value)
	m.p.closeblock()
}

func (m *marshalGen) gBase(b *BaseElem) {
	if !m.p.ok() {
		return
	}
	m.fuseHook()
	vname := b.Varname()
	if b.Convert {
		if b.ShimMode == Cast {
			vname = tobaseConvert(b)
		} else {
			vname = randIdent()
			m.p.printf("\nvar %s %s", vname, b.BaseType())
			m.p.printf("\n%s, err = %s", vname, tobaseConvert(b))
			m.p.wrapErrCheck(m.ctx.ArgsStr())
		}
	}

	var echeck bool
	switch b.Value {
	case IDENT:
		echeck = true
		m.p.printf("\no, err = %s.MarshalMsg(o)", vname)
	case Intf, Ext, JsonNumber:
		echeck = true
		m.p.printf("\no, err = msgp.Append%s(o, %s)", b.BaseName(), vname)
	default:
		m.rawAppend(b.BaseName(), literalFmt, vname)
	}

	if echeck {
		m.p.wrapErrCheck(m.ctx.ArgsStr())
	}
}
