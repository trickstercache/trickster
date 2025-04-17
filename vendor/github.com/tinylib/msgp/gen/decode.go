package gen

import (
	"io"
	"strconv"
)

func decode(w io.Writer) *decodeGen {
	return &decodeGen{
		p:        printer{w: w},
		hasfield: false,
	}
}

type decodeGen struct {
	passes
	p        printer
	hasfield bool
	ctx      *Context
}

func (d *decodeGen) Method() Method { return Decode }

func (d *decodeGen) needsField() {
	if d.hasfield {
		return
	}
	d.p.print("\nvar field []byte; _ = field")
	d.hasfield = true
}

func (d *decodeGen) Execute(p Elem, ctx Context) error {
	d.ctx = &ctx
	p = d.applyall(p)
	if p == nil {
		return nil
	}
	d.hasfield = false
	if !d.p.ok() {
		return d.p.err
	}

	if !IsPrintable(p) {
		return nil
	}

	d.p.comment("DecodeMsg implements msgp.Decodable")

	d.p.printf("\nfunc (%s %s) DecodeMsg(dc *msgp.Reader) (err error) {", p.Varname(), methodReceiver(p))
	next(d, p)
	d.p.nakedReturn()
	unsetReceiver(p)
	return d.p.err
}

func (d *decodeGen) gStruct(s *Struct) {
	if !d.p.ok() {
		return
	}
	if s.AsTuple {
		d.structAsTuple(s)
	} else {
		d.structAsMap(s)
	}
}

func (d *decodeGen) assignAndCheck(name string, typ string) {
	if !d.p.ok() {
		return
	}
	d.p.printf("\n%s, err = dc.Read%s()", name, typ)
	d.p.wrapErrCheck(d.ctx.ArgsStr())
}

func (d *decodeGen) structAsTuple(s *Struct) {
	nfields := len(s.Fields)

	sz := randIdent()
	d.p.declare(sz, u32)
	d.assignAndCheck(sz, arrayHeader)
	d.p.arrayCheck(strconv.Itoa(nfields), sz)
	for i := range s.Fields {
		if !d.p.ok() {
			return
		}
		fieldElem := s.Fields[i].FieldElem
		anField := s.Fields[i].HasTagPart("allownil") && fieldElem.AllowNil()
		if anField {
			d.p.print("\nif dc.IsNil() {")
			d.p.print("\nerr = dc.ReadNil()")
			d.p.wrapErrCheck(d.ctx.ArgsStr())
			d.p.printf("\n%s = nil\n} else {", s.Fields[i].FieldElem.Varname())
		}
		SetIsAllowNil(fieldElem, anField)
		d.ctx.PushString(s.Fields[i].FieldName)
		next(d, fieldElem)
		d.ctx.Pop()
		if anField {
			d.p.printf("\n}") // close if statement
		}
	}
}

func (d *decodeGen) structAsMap(s *Struct) {
	d.needsField()
	sz := randIdent()
	d.p.declare(sz, u32)
	d.assignAndCheck(sz, mapHeader)

	oeCount := s.CountFieldTagPart("omitempty") + s.CountFieldTagPart("omitzero")
	if !d.ctx.clearOmitted {
		oeCount = 0
	}
	bm := bmask{
		bitlen:  oeCount,
		varname: sz + "Mask",
	}
	if oeCount > 0 {
		// Declare mask
		d.p.printf("\n%s", bm.typeDecl())
		d.p.printf("\n_ = %s", bm.varname)
	}
	// Index to field idx of each emitted
	oeEmittedIdx := []int{}

	d.p.printf("\nfor %s > 0 {\n%s--", sz, sz)
	d.assignAndCheck("field", mapKey)
	d.p.print("\nswitch msgp.UnsafeString(field) {")
	for i := range s.Fields {
		d.ctx.PushString(s.Fields[i].FieldName)
		d.p.printf("\ncase %q:", s.Fields[i].FieldTag)
		fieldElem := s.Fields[i].FieldElem
		anField := s.Fields[i].HasTagPart("allownil") && fieldElem.AllowNil()
		if anField {
			d.p.print("\nif dc.IsNil() {")
			d.p.print("\nerr = dc.ReadNil()")
			d.p.wrapErrCheck(d.ctx.ArgsStr())
			d.p.printf("\n%s = nil\n} else {", fieldElem.Varname())
		}
		SetIsAllowNil(fieldElem, anField)
		next(d, fieldElem)
		if oeCount > 0 && (s.Fields[i].HasTagPart("omitempty") || s.Fields[i].HasTagPart("omitzero")) {
			d.p.printf("\n%s", bm.setStmt(len(oeEmittedIdx)))
			oeEmittedIdx = append(oeEmittedIdx, i)
		}
		d.ctx.Pop()
		if !d.p.ok() {
			return
		}
		if anField {
			d.p.printf("\n}") // close if statement
		}
	}
	d.p.print("\ndefault:\nerr = dc.Skip()")
	d.p.wrapErrCheck(d.ctx.ArgsStr())

	d.p.closeblock() // close switch
	d.p.closeblock() // close for loop

	if oeCount > 0 {
		d.p.printf("\n// Clear omitted fields.\n")
		if bm.bitlen > 1 {
			d.p.printf("if %s {\n", bm.notAllSet())
		}
		for bitIdx, fieldIdx := range oeEmittedIdx {
			fieldElem := s.Fields[fieldIdx].FieldElem

			d.p.printf("if %s == 0 {\n", bm.readExpr(bitIdx))
			fze := fieldElem.ZeroExpr()
			if fze != "" {
				d.p.printf("%s = %s\n", fieldElem.Varname(), fze)
			} else {
				d.p.printf("%s = %s{}\n", fieldElem.Varname(), fieldElem.TypeName())
			}
			d.p.printf("}\n")
		}
		if bm.bitlen > 1 {
			d.p.printf("}")
		}
	}
}

func (d *decodeGen) gBase(b *BaseElem) {
	if !d.p.ok() {
		return
	}

	// open block for 'tmp'
	var tmp string
	if b.Convert && b.Value != IDENT { // we don't need block for 'tmp' in case of IDENT
		tmp = randIdent()
		d.p.printf("\n{ var %s %s", tmp, b.BaseType())
	}

	vname := b.Varname()  // e.g. "z.FieldOne"
	bname := b.BaseName() // e.g. "Float64"
	checkNil := vname     // Name of var to check for nil

	// handle special cases
	// for object type.
	switch b.Value {
	case Bytes:
		if b.Convert {
			lowered := b.ToBase() + "(" + vname + ")"
			d.p.printf("\n%s, err = dc.ReadBytes(%s)", tmp, lowered)
			checkNil = tmp
		} else {
			d.p.printf("\n%s, err = dc.ReadBytes(%s)", vname, vname)
			checkNil = vname
		}
	case IDENT:
		if b.Convert {
			lowered := b.ToBase() + "(" + vname + ")"
			d.p.printf("\nerr = %s.DecodeMsg(dc)", lowered)
		} else {
			d.p.printf("\nerr = %s.DecodeMsg(dc)", vname)
		}
	case Ext:
		d.p.printf("\nerr = dc.ReadExtension(%s)", vname)
	default:
		if b.Convert {
			d.p.printf("\n%s, err = dc.Read%s()", tmp, bname)
		} else {
			d.p.printf("\n%s, err = dc.Read%s()", vname, bname)
		}
	}
	d.p.wrapErrCheck(d.ctx.ArgsStr())

	if checkNil != "" && b.AllowNil() {
		// Ensure that 0 sized slices are allocated.
		d.p.printf("\nif %s == nil {\n%s = make([]byte, 0)\n}", checkNil, checkNil)
	}

	// close block for 'tmp'
	if b.Convert && b.Value != IDENT {
		if b.ShimMode == Cast {
			d.p.printf("\n%s = %s(%s)\n}", vname, b.FromBase(), tmp)
		} else {
			d.p.printf("\n%s, err = %s(%s)\n}", vname, b.FromBase(), tmp)
			d.p.wrapErrCheck(d.ctx.ArgsStr())
		}
	}
}

func (d *decodeGen) gMap(m *Map) {
	if !d.p.ok() {
		return
	}
	sz := randIdent()

	// resize or allocate map
	d.p.declare(sz, u32)
	d.assignAndCheck(sz, mapHeader)
	d.p.resizeMap(sz, m)

	// for element in map, read string/value
	// pair and assign
	d.needsField()
	d.p.printf("\nfor %s > 0 {\n%s--", sz, sz)
	d.p.declare(m.Keyidx, "string")
	d.p.declare(m.Validx, m.Value.TypeName())
	d.assignAndCheck(m.Keyidx, stringTyp)
	d.ctx.PushVar(m.Keyidx)
	m.Value.SetIsAllowNil(false)
	next(d, m.Value)
	d.p.mapAssign(m)
	d.ctx.Pop()
	d.p.closeblock()
}

func (d *decodeGen) gSlice(s *Slice) {
	if !d.p.ok() {
		return
	}
	sz := randIdent()
	d.p.declare(sz, u32)
	d.assignAndCheck(sz, arrayHeader)
	if s.isAllowNil {
		d.p.resizeSliceNoNil(sz, s)
	} else {
		d.p.resizeSlice(sz, s)
	}
	d.p.rangeBlock(d.ctx, s.Index, s.Varname(), d, s.Els)
}

func (d *decodeGen) gArray(a *Array) {
	if !d.p.ok() {
		return
	}

	// special case if we have [const]byte
	if be, ok := a.Els.(*BaseElem); ok && (be.Value == Byte || be.Value == Uint8) {
		d.p.printf("\nerr = dc.ReadExactBytes((%s)[:])", a.Varname())
		d.p.wrapErrCheck(d.ctx.ArgsStr())
		return
	}
	sz := randIdent()
	d.p.declare(sz, u32)
	d.assignAndCheck(sz, arrayHeader)
	d.p.arrayCheck(coerceArraySize(a.Size), sz)
	d.p.rangeBlock(d.ctx, a.Index, a.Varname(), d, a.Els)
}

func (d *decodeGen) gPtr(p *Ptr) {
	if !d.p.ok() {
		return
	}
	d.p.print("\nif dc.IsNil() {")
	d.p.print("\nerr = dc.ReadNil()")
	d.p.wrapErrCheck(d.ctx.ArgsStr())
	d.p.printf("\n%s = nil\n} else {", p.Varname())
	d.p.initPtr(p)
	next(d, p.Value)
	d.p.closeblock()
}
