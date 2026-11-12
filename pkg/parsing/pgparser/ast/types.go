// Copyright 2015 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// Modified by The Trickster Authors in 2026: rewritten as a small type descriptor; only the type names are kept.

package ast

import (
	"strconv"
	"strings"
)

// Family is the broad kind of a SQL type.
type Family uint8

// The type families the parser can name.
const (
	UnknownFamily Family = iota
	BoolFamily
	IntFamily
	FloatFamily
	DecimalFamily
	DateFamily
	TimestampFamily
	TimestampTZFamily
	IntervalFamily
	TimeFamily
	TimeTZFamily
	StringFamily
	BytesFamily
	BitFamily
	UUIDFamily
	INetFamily
	JSONFamily
	OidFamily
	ArrayFamily
)

// IntervalDurationType is a field of an INTERVAL type qualifier, such as HOUR.
type IntervalDurationType uint8

// The fields an INTERVAL qualifier can name.
const (
	IntervalDurationUnset IntervalDurationType = iota
	IntervalDurationYear
	IntervalDurationMonth
	IntervalDurationDay
	IntervalDurationHour
	IntervalDurationMinute
	IntervalDurationSecond
)

// String returns the SQL keyword of the field.
func (d IntervalDurationType) String() string {
	switch d {
	case IntervalDurationYear:
		return "YEAR"
	case IntervalDurationMonth:
		return "MONTH"
	case IntervalDurationDay:
		return "DAY"
	case IntervalDurationHour:
		return "HOUR"
	case IntervalDurationMinute:
		return "MINUTE"
	case IntervalDurationSecond:
		return "SECOND"
	}
	return "UNSET"
}

// IntervalDurationField is an INTERVAL qualifier: DurationType alone, or FromDurationType TO DurationType.
type IntervalDurationField struct {
	DurationType     IntervalDurationType
	FromDurationType IntervalDurationType
}

// IntervalTypeMetadata is everything written after the INTERVAL type name.
type IntervalTypeMetadata struct {
	DurationField  IntervalDurationField
	Precision      int32
	PrecisionIsSet bool
}

const (
	bits16 = 16
	bits32 = 32
	bits64 = 64

	nameString  = "STRING"
	nameVarChar = "VARCHAR"
	nameChar    = "CHAR"
	nameQChar   = `"char"`
	nameName    = "NAME"
	nameBit     = "BIT"
	nameVarBit  = "VARBIT"
	arraySuffix = "[]"
)

// T describes a SQL type the parser knows by name. Values are immutable; compare with Identical.
type T struct {
	family       Family
	width        int32
	precision    int32
	scale        int32
	precisionSet bool
	// name is the spelling of a type printed by a fixed name: string and bit kinds, OID aliases
	name string
	// fixedName marks a type that prints as its name whatever its family
	fixedName bool
	interval  IntervalDurationField
	elem      *T
}

// The parameterless types.
var (
	TypeBool        = &T{family: BoolFamily, name: "BOOL"}
	TypeInt         = &T{family: IntFamily, width: bits64}
	TypeInt4        = &T{family: IntFamily, width: bits32}
	TypeInt2        = &T{family: IntFamily, width: bits16}
	TypeFloat       = &T{family: FloatFamily, width: bits64}
	TypeFloat4      = &T{family: FloatFamily, width: bits32}
	TypeDecimal     = &T{family: DecimalFamily, name: "DECIMAL"}
	TypeDate        = &T{family: DateFamily, name: "DATE"}
	TypeTime        = &T{family: TimeFamily, name: "TIME"}
	TypeTimeTZ      = &T{family: TimeTZFamily, name: "TIMETZ"}
	TypeTimestamp   = &T{family: TimestampFamily, name: "TIMESTAMP"}
	TypeTimestampTZ = &T{family: TimestampTZFamily, name: "TIMESTAMPTZ"}
	TypeInterval    = &T{family: IntervalFamily, name: "INTERVAL"}
	TypeString      = &T{family: StringFamily, name: nameString}
	TypeVarChar     = &T{family: StringFamily, name: nameVarChar}
	TypeQChar       = &T{family: StringFamily, name: nameQChar}
	TypeBytes       = &T{family: BytesFamily, name: "BYTES"}
	TypeVarBit      = &T{family: BitFamily, name: nameVarBit}
	TypeUUID        = &T{family: UUIDFamily, name: "UUID"}
	TypeINet        = &T{family: INetFamily, name: "INET"}
	TypeJSON        = &T{family: JSONFamily, name: "JSONB"}
)

// namedType returns a type the formatter prints by a fixed name.
func namedType(family Family, name string) *T {
	return &T{family: family, name: name, fixedName: true}
}

// nonKeywordType returns the built-in type a name the grammar reads as a plain identifier refers to.
func nonKeywordType(name string) *T {
	switch name {
	case "_char":
		return namedType(ArrayFamily, `"char"[]`)
	case "anyelement":
		return namedType(UnknownFamily, "ANYELEMENT")
	case "anyarray":
		return namedType(ArrayFamily, "ANYELEMENT[]")
	case "bit":
		return namedType(BitFamily, "BIT")
	case "_bit":
		return namedType(ArrayFamily, "BIT(0)[]")
	case "bool", "boolean":
		return TypeBool
	case "_bool":
		return namedType(ArrayFamily, "BOOL[]")
	case "box2d":
		return namedType(UnknownFamily, "BOX2D")
	case "bpchar":
		return namedType(StringFamily, "BPCHAR")
	case "_bpchar":
		return namedType(ArrayFamily, "BPCHAR[]")
	case "blob", "bytea", "bytes":
		return TypeBytes
	case "_bytea":
		return namedType(ArrayFamily, "BYTES[]")
	case "char", "character":
		return namedType(StringFamily, "CHAR")
	case "date":
		return TypeDate
	case "_date":
		return namedType(ArrayFamily, "DATE[]")
	case "dec", "decimal", "numeric":
		return TypeDecimal
	case "_numeric":
		return namedType(ArrayFamily, "DECIMAL[]")
	case "float4", "real":
		return TypeFloat4
	case "_float4":
		return namedType(ArrayFamily, "FLOAT4[]")
	case "float", "float8":
		return TypeFloat
	case "_float8":
		return namedType(ArrayFamily, "FLOAT8[]")
	case "geography":
		return namedType(UnknownFamily, "GEOGRAPHY")
	case "geometry":
		return namedType(UnknownFamily, "GEOMETRY")
	case "inet":
		return TypeINet
	case "_inet":
		return namedType(ArrayFamily, "INET[]")
	case "int2", "serial2", "smallint", "smallserial":
		return TypeInt2
	case "int2vector":
		return namedType(ArrayFamily, "INT2VECTOR")
	case "_int2vector":
		return namedType(ArrayFamily, "INT2VECTOR[]")
	case "_int2":
		return namedType(ArrayFamily, "INT2[]")
	case "int4", "serial4":
		return TypeInt4
	case "_int4":
		return namedType(ArrayFamily, "INT4[]")
	case "bigint", "bigserial", "int64", "int8", "integer", "serial8":
		return TypeInt
	case "_int8":
		return namedType(ArrayFamily, "INT8[]")
	case "interval":
		return TypeInterval
	case "_interval":
		return namedType(ArrayFamily, "INTERVAL[]")
	case "json", "jsonb":
		return TypeJSON
	case "_jsonb":
		return namedType(ArrayFamily, "JSONB[]")
	case "name":
		return namedType(StringFamily, "NAME")
	case "_name":
		return namedType(ArrayFamily, "NAME[]")
	case "oid":
		return namedType(OidFamily, "OID")
	case "oidvector":
		return namedType(ArrayFamily, "OIDVECTOR")
	case "_oidvector":
		return namedType(ArrayFamily, "OIDVECTOR[]")
	case "_oid":
		return namedType(ArrayFamily, "OID[]")
	case "pg_lsn":
		return namedType(UnknownFamily, "PG_LSN")
	case "_pg_lsn":
		return namedType(ArrayFamily, "PG_LSN[]")
	case "record":
		return namedType(UnknownFamily, "RECORD")
	case "_record":
		return namedType(ArrayFamily, "RECORD[]")
	case "refcursor":
		return namedType(UnknownFamily, "REFCURSOR")
	case "_refcursor":
		return namedType(ArrayFamily, "REFCURSOR[]")
	case "regclass":
		return namedType(OidFamily, "REGCLASS")
	case "_regclass":
		return namedType(ArrayFamily, "REGCLASS[]")
	case "regnamespace":
		return namedType(OidFamily, "REGNAMESPACE")
	case "_regnamespace":
		return namedType(ArrayFamily, "REGNAMESPACE[]")
	case "regproc":
		return namedType(OidFamily, "REGPROC")
	case "regprocedure":
		return namedType(OidFamily, "REGPROCEDURE")
	case "_regprocedure":
		return namedType(ArrayFamily, "REGPROCEDURE[]")
	case "_regproc":
		return namedType(ArrayFamily, "REGPROC[]")
	case "regrole":
		return namedType(OidFamily, "REGROLE")
	case "_regrole":
		return namedType(ArrayFamily, "REGROLE[]")
	case "regtype":
		return namedType(OidFamily, "REGTYPE")
	case "_regtype":
		return namedType(ArrayFamily, "REGTYPE[]")
	case "string", "text":
		return TypeString
	case "_text":
		return namedType(ArrayFamily, "STRING[]")
	case "time":
		return TypeTime
	case "timestamp":
		return TypeTimestamp
	case "timestamptz":
		return TypeTimestampTZ
	case "_timestamptz":
		return namedType(ArrayFamily, "TIMESTAMPTZ[]")
	case "_timestamp":
		return namedType(ArrayFamily, "TIMESTAMP[]")
	case "timetz":
		return TypeTimeTZ
	case "_timetz":
		return namedType(ArrayFamily, "TIMETZ[]")
	case "_time":
		return namedType(ArrayFamily, "TIME[]")
	case "trigger":
		return namedType(UnknownFamily, "TRIGGER")
	case "tsquery":
		return namedType(UnknownFamily, "TSQUERY")
	case "_tsquery":
		return namedType(ArrayFamily, "TSQUERY[]")
	case "tsvector":
		return namedType(UnknownFamily, "TSVECTOR")
	case "_tsvector":
		return namedType(ArrayFamily, "TSVECTOR[]")
	case "unknown":
		return namedType(UnknownFamily, "UNKNOWN")
	case "uuid":
		return TypeUUID
	case "_uuid":
		return namedType(ArrayFamily, "UUID[]")
	case "varbit":
		return TypeVarBit
	case "_varbit":
		return namedType(ArrayFamily, "VARBIT[]")
	case "varchar":
		return TypeVarChar
	case "_varchar":
		return namedType(ArrayFamily, "VARCHAR[]")
	case "vector":
		return namedType(UnknownFamily, "VECTOR")
	case "void":
		return namedType(UnknownFamily, "VOID")
	}
	return nil
}

// unsupportedTypeName reports a PostgreSQL type name the parser rejects outright.
func unsupportedTypeName(name string) bool {
	switch name {
	case "box", "cidr", "circle", "jsonpath", "line", "lseg", "macaddr", "macaddr8", "money", "path",
		"point", "polygon", "txid_snapshot", "xml":
		return true
	}
	return false
}

// TypeForNonKeywordTypeName returns the built-in type a non-keyword name refers to. ok is false
// for a name that is not built in, and unsupported is true for a name the parser must reject.
func TypeForNonKeywordTypeName(name string) (typ *T, ok, unsupported bool) {
	if typ = nonKeywordType(name); typ != nil {
		return typ, true, false
	}
	return nil, false, unsupportedTypeName(name)
}

// MakeTypeBit returns the BIT type of the given width.
func MakeTypeBit(width int32) *T { return &T{family: BitFamily, name: nameBit, width: width} }

// MakeTypeVarBit returns the VARBIT type of the given width.
func MakeTypeVarBit(width int32) *T { return &T{family: BitFamily, name: nameVarBit, width: width} }

// MakeTypeChar returns the CHAR type of the given width.
func MakeTypeChar(width int32) *T { return &T{family: StringFamily, name: nameChar, width: width} }

// MakeTypeDecimal returns the DECIMAL type of the given precision and scale.
func MakeTypeDecimal(precision, scale int32) *T {
	return &T{family: DecimalFamily, name: TypeDecimal.name, precision: precision, scale: scale}
}

func makeTimeType(base *T, precision int32) *T {
	return &T{family: base.family, name: base.name, precision: precision, precisionSet: true}
}

// MakeTypeTime returns TIME with an explicit precision.
func MakeTypeTime(precision int32) *T { return makeTimeType(TypeTime, precision) }

// MakeTypeTimeTZ returns TIMETZ with an explicit precision.
func MakeTypeTimeTZ(precision int32) *T { return makeTimeType(TypeTimeTZ, precision) }

// MakeTypeTimestamp returns TIMESTAMP with an explicit precision.
func MakeTypeTimestamp(precision int32) *T { return makeTimeType(TypeTimestamp, precision) }

// MakeTypeTimestampTZ returns TIMESTAMPTZ with an explicit precision.
func MakeTypeTimestampTZ(precision int32) *T { return makeTimeType(TypeTimestampTZ, precision) }

// MakeTypeInterval returns INTERVAL with a field qualifier, a precision, or both.
func MakeTypeInterval(meta IntervalTypeMetadata) *T {
	return &T{
		family: IntervalFamily, name: TypeInterval.name, interval: meta.DurationField,
		precision: meta.Precision, precisionSet: meta.PrecisionIsSet,
	}
}

// MakeTypeArray returns the array type of an element type.
func MakeTypeArray(elem *T) *T { return &T{family: ArrayFamily, elem: elem} }

// WithWidth returns a copy of a string type with a declared length.
func (t *T) WithWidth(width int32) *T {
	c := *t
	c.width = width
	return &c
}

// Family returns the type's family.
func (t *T) Family() Family { return t.family }

// Width returns the bit width of a numeric type, or the declared length of a string or bit type.
func (t *T) Width() int32 { return t.width }

// ArrayContents returns the element type of an array type, or nil.
func (t *T) ArrayContents() *T { return t.elem }

// Identical reports whether two types are the same in every written detail.
func (t *T) Identical(other *T) bool {
	if t == nil || other == nil {
		return t == other
	}
	if t.family != other.family || t.width != other.width || t.precision != other.precision ||
		t.scale != other.scale || t.precisionSet != other.precisionSet || t.name != other.name ||
		t.fixedName != other.fixedName ||
		t.interval != other.interval {
		return false
	}
	if t.elem == nil || other.elem == nil {
		return t.elem == other.elem
	}
	return t.elem.Identical(other.elem)
}

func withArg(name string, args ...int32) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('(')
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(int64(arg), 10))
	}
	b.WriteByte(')')
	return b.String()
}

func (t *T) hasPrecision() bool { return t.precision > 0 || t.precisionSet }

// SQLString returns the type as the formatter writes it in a cast.
func (t *T) SQLString() string {
	if t.fixedName {
		return t.name
	}
	switch t.family {
	case BitFamily:
		// BIT(1) prints as BIT; a VARBIT prints any declared width
		if (t.name != nameVarBit && t.width > 1) || (t.name == nameVarBit && t.width > 0) {
			return withArg(t.name, t.width)
		}
	case IntFamily:
		switch t.width {
		case bits16:
			return "INT2"
		case bits32:
			return "INT4"
		}
		return "INT8"
	case StringFamily:
		if t.width > 0 && (t.width != 1 || (t.name != nameChar && t.name != nameQChar)) {
			return withArg(t.name, t.width)
		}
	case FloatFamily:
		if t.width == bits32 {
			return "FLOAT4"
		}
		return "FLOAT8"
	case DecimalFamily:
		if t.precision > 0 {
			if t.scale > 0 {
				return withArg(t.name, t.precision, t.scale)
			}
			return withArg(t.name, t.precision)
		}
	case TimestampFamily, TimestampTZFamily, TimeFamily, TimeTZFamily:
		if t.hasPrecision() {
			return withArg(t.name, t.precision)
		}
	case IntervalFamily:
		return t.intervalSQLString()
	case ArrayFamily:
		if t.name == "" {
			return t.elem.SQLString() + arraySuffix
		}
	}
	return t.name
}

func (t *T) intervalSQLString() string {
	if t.interval.DurationType == IntervalDurationUnset {
		if t.hasPrecision() {
			return withArg(t.name, t.precision)
		}
		return t.name
	}
	return t.name + " " + t.IntervalQualifier()
}

// IntervalQualifier returns the field qualifier of an INTERVAL type, such as DAY TO SECOND(2), or ""
func (t *T) IntervalQualifier() string {
	if t.interval.DurationType == IntervalDurationUnset {
		return ""
	}
	var b strings.Builder
	if t.interval.FromDurationType != IntervalDurationUnset {
		b.WriteString(t.interval.FromDurationType.String())
		b.WriteString(" TO ")
	}
	if t.hasPrecision() {
		b.WriteString(withArg(t.interval.DurationType.String(), t.precision))
	} else {
		b.WriteString(t.interval.DurationType.String())
	}
	return b.String()
}

// String implements fmt.Stringer.
func (t *T) String() string { return t.SQLString() }
