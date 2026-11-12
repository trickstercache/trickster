/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package ast_test

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser"
	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
	"github.com/trickstercache/trickster/v2/pkg/testutil/sqlgolden"
)

const (
	exprCases  = "../testdata/expression_cases.json"
	selectHead = "SELECT "
)

// copier replaces every literal with an equal new node, so every parent on the path is copied
type copier struct{ visited int }

func (c *copier) VisitPre(expr ast.Expr) (bool, ast.Expr) {
	c.visited++
	_ = expr.String()
	switch v := expr.(type) {
	case *ast.NumVal:
		n := *v
		return false, &n
	case *ast.StrVal:
		s := *v
		return false, &s
	}
	return true, expr
}

func (*copier) VisitPost(expr ast.Expr) ast.Expr { return expr }

// renamer also replaces every name, so nodes holding only names are copied too
type renamer struct{ copier }

func (r *renamer) VisitPre(expr ast.Expr) (bool, ast.Expr) {
	if v, ok := expr.(*ast.UnresolvedName); ok {
		n := *v
		return false, &n
	}
	return r.copier.VisitPre(expr)
}

// printAll formats every node reachable from v, so each node type's String and Format run
func printAll(v reflect.Value, seen map[uintptr]bool) {
	if !v.IsValid() {
		return
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() || seen[v.Pointer()] {
			return
		}
		seen[v.Pointer()] = true
	}
	if v.CanInterface() {
		switch n := v.Interface().(type) {
		case fmt.Stringer:
			if v.Kind() != reflect.Pointer || !v.IsNil() {
				_ = n.String()
			}
		case ast.NodeFormatter:
			_ = ast.AsString(n)
		}
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			printAll(v.Elem(), seen)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				printAll(v.Field(i), seen)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			printAll(v.Index(i), seen)
		}
	}
	if v.CanAddr() && v.Addr().CanInterface() {
		if s, ok := v.Addr().Interface().(fmt.Stringer); ok && v.Kind() != reflect.Pointer {
			_ = s.String()
		}
	}
}

// pruner stops at the root of every expression
type pruner struct{}

func (pruner) VisitPre(expr ast.Expr) (bool, ast.Expr) { return false, expr }
func (pruner) VisitPost(expr ast.Expr) ast.Expr        { return expr }

func statements(t *testing.T) []*ast.Select {
	t.Helper()
	var out []*ast.Select
	for _, sql := range sqlgolden.Statements(t) {
		if stmt, err := pgparser.ParseQuery(sql, pgparser.Options{}); err == nil && stmt != nil {
			out = append(out, stmt)
		}
	}
	var cases []string
	loadCases(t, &cases)
	for _, expr := range cases {
		if stmt, err := pgparser.ParseQuery(selectHead+expr, pgparser.Options{}); err == nil {
			out = append(out, stmt)
		}
	}
	return out
}

func TestWalkCopiesOnWrite(t *testing.T) {
	visited := 0
	for _, stmt := range statements(t) {
		before := ast.AsString(stmt)
		c := &copier{}
		walked, changed := ast.WalkStmt(c, stmt)
		visited += c.visited
		if got := ast.AsString(walked); got != before {
			t.Errorf("a copied statement prints %q, want %q", got, before)
		}
		if after := ast.AsString(stmt); after != before {
			t.Errorf("walking changed the original: %q, was %q", after, before)
		}
		if !changed && walked != ast.Statement(stmt) {
			t.Errorf("an unchanged walk returned a new statement for %q", before)
		}
		if same, changed := ast.WalkStmt(pruner{}, stmt); changed || same != ast.Statement(stmt) {
			t.Errorf("a walk that changes nothing copied %q", before)
		}
		if renamed, _ := ast.WalkStmt(&renamer{}, stmt); ast.AsString(renamed) != before {
			t.Errorf("a renamed statement prints %q, want %q", ast.AsString(renamed), before)
		}
		printAll(reflect.ValueOf(stmt), map[uintptr]bool{})
	}
	if visited == 0 {
		t.Fatal("no nodes were visited")
	}
}

func TestNumVal(t *testing.T) {
	for literal, want := range map[string]int64{"12": 12, "0x1F": 31, "1e3": 1000, "2.0": 2, "-7": -7} {
		expr, err := pgparser.ParseExpr(literal, pgparser.Options{})
		if err != nil {
			t.Fatal(err)
		}
		n, ok := expr.(*ast.NumVal)
		if !ok {
			t.Fatalf("%q parsed as %T", literal, expr)
		}
		if got, err := n.AsInt64(); err != nil || got != want {
			t.Errorf("%q: AsInt64 = %d, %v; want %d", literal, got, err, want)
		}
		if got, err := n.AsInt32(); err != nil || int64(got) != want {
			t.Errorf("%q: AsInt32 = %d, %v", literal, got, err)
		}
	}
	for _, literal := range []string{"1.5", "1e40", "0xFFFFFFFFFFFFFFFFFF", "NaN"} {
		n := ast.NewNumVal(literal, false)
		if _, err := n.AsInt64(); err == nil {
			t.Errorf("%q read as an int64", literal)
		}
	}
	if _, err := ast.NewNumVal("3000000000", false).AsInt32(); err == nil {
		t.Error("3000000000 read as an int32")
	}
	n := ast.NewNumVal("5", false)
	n.SetNegative()
	if got := ast.AsString(n); got != "-5" {
		t.Errorf("negative: %s", got)
	}
	n.Negate()
	if got := ast.AsString(n); got != "5" {
		t.Errorf("negated: %s", got)
	}
	if got := ast.AsString(ast.NewNumVal("NaN", false)); got != "'NaN'" {
		t.Errorf("NaN: %s", got)
	}
}

func TestLiterals(t *testing.T) {
	if ast.MakeBool(true) != ast.BoolTrue || ast.MakeBool(false) != ast.BoolFalse {
		t.Error("MakeBool returned a new node")
	}
	for node, want := range map[ast.Expr]string{
		ast.BoolTrue: "true", ast.BoolFalse: "false", ast.Null: "NULL", ast.NewBytesStrVal("a"): "b'a'",
	} {
		if got := node.String(); got != want {
			t.Errorf("%T prints %q, want %q", node, got, want)
		}
		if node.Walk(pruner{}) != node {
			t.Errorf("%T walked to a new node", node)
		}
	}
	bits, err := ast.NewBitVal("101")
	if err != nil || bits.String() != "B'101'" || bits.Walk(pruner{}) != ast.Expr(bits) {
		t.Errorf("bit literal: %v, %v", bits, err)
	}
	if _, err := ast.NewBitVal("12"); err == nil {
		t.Error("a non-binary digit was accepted")
	}
	for number, ok := range map[string]bool{"1": true, "65536": true, "0": false, "65537": false, "x": false} {
		if _, err := ast.NewPlaceholder(number); (err == nil) != ok {
			t.Errorf("placeholder %q: err = %v", number, err)
		}
	}
	fn := ast.WrapFunction("f")
	if fn.String() != "f" {
		t.Errorf("function reference prints %q", fn.String())
	}
}

func TestTypes(t *testing.T) {
	for _, test := range []struct {
		typ    *ast.T
		want   string
		family ast.Family
	}{
		{ast.TypeInt, "INT8", ast.IntFamily},
		{ast.TypeInt4, "INT4", ast.IntFamily},
		{ast.TypeInt2, "INT2", ast.IntFamily},
		{ast.TypeFloat4, "FLOAT4", ast.FloatFamily},
		{ast.MakeTypeBit(1), "BIT", ast.BitFamily},
		{ast.MakeTypeBit(3), "BIT(3)", ast.BitFamily},
		{ast.MakeTypeVarBit(0), "VARBIT", ast.BitFamily},
		{ast.MakeTypeVarBit(2), "VARBIT(2)", ast.BitFamily},
		{ast.MakeTypeChar(1), "CHAR", ast.StringFamily},
		{ast.TypeVarChar.WithWidth(4), "VARCHAR(4)", ast.StringFamily},
		{ast.MakeTypeDecimal(5, 0), "DECIMAL(5)", ast.DecimalFamily},
		{ast.MakeTypeDecimal(5, 2), "DECIMAL(5,2)", ast.DecimalFamily},
		{ast.TypeDecimal, "DECIMAL", ast.DecimalFamily},
		{ast.MakeTypeTime(3), "TIME(3)", ast.TimeFamily},
		{ast.MakeTypeTimeTZ(0), "TIMETZ(0)", ast.TimeTZFamily},
		{ast.MakeTypeTimestamp(6), "TIMESTAMP(6)", ast.TimestampFamily},
		{ast.MakeTypeTimestampTZ(2), "TIMESTAMPTZ(2)", ast.TimestampTZFamily},
		{ast.MakeTypeInterval(ast.IntervalTypeMetadata{Precision: 3, PrecisionIsSet: true}), "INTERVAL(3)", ast.IntervalFamily},
		{ast.MakeTypeInterval(ast.IntervalTypeMetadata{
			DurationField: ast.IntervalDurationField{DurationType: ast.IntervalDurationSecond, FromDurationType: ast.IntervalDurationDay},
			Precision:     2, PrecisionIsSet: true,
		}), "INTERVAL DAY TO SECOND(2)", ast.IntervalFamily},
		{ast.MakeTypeArray(ast.TypeString), "STRING[]", ast.ArrayFamily},
	} {
		if got := test.typ.SQLString(); got != test.want || test.typ.String() != test.want {
			t.Errorf("%s printed %q", test.want, got)
		}
		if test.typ.Family() != test.family {
			t.Errorf("%s family %d, want %d", test.want, test.typ.Family(), test.family)
		}
		if !test.typ.Identical(test.typ) {
			t.Errorf("%s is not identical to itself", test.want)
		}
	}
	arr := ast.MakeTypeArray(ast.TypeInt)
	if arr.ArrayContents() != ast.TypeInt || arr.Identical(ast.MakeTypeArray(ast.TypeInt4)) ||
		!arr.Identical(ast.MakeTypeArray(ast.TypeInt)) || arr.Identical(ast.TypeInt) {
		t.Error("array identity")
	}
	var none *ast.T
	if !none.Identical(nil) || none.Identical(ast.TypeInt) || ast.TypeInt.Identical(nil) {
		t.Error("nil identity")
	}
	if ast.TypeVarChar.WithWidth(3).Width() != 3 || ast.TypeVarChar.Width() != 0 {
		t.Error("WithWidth changed the shared type")
	}
	for _, name := range []string{"money", "xml", "point"} {
		if _, ok, unsupported := ast.TypeForNonKeywordTypeName(name); ok || !unsupported {
			t.Errorf("%s: ok %v, unsupported %v", name, ok, unsupported)
		}
	}
	if typ, ok, _ := ast.TypeForNonKeywordTypeName("int4"); !ok || typ != ast.TypeInt4 {
		t.Errorf("int4 resolved to %v", typ)
	}
	if _, ok, unsupported := ast.TypeForNonKeywordTypeName("mytype"); ok || unsupported {
		t.Error("an unknown name resolved")
	}
	for d := ast.IntervalDurationUnset; d <= ast.IntervalDurationSecond; d++ {
		if d.String() == "" {
			t.Errorf("duration %d has no name", d)
		}
	}
}

func TestNames(t *testing.T) {
	stmt, err := pgparser.ParseQuery("SELECT a, t.b, s.t.c, t.* FROM c.s.t", pgparser.Options{})
	if err != nil {
		t.Fatal(err)
	}
	clause := stmt.Select.(*ast.SelectClause)
	table := clause.From.Tables[0].(*ast.AliasedTableExpr).Expr.(*ast.TableName)
	if table.Table() != "t" || table.Schema() != "s" || table.Catalog() != "c" || table.Object() != "t" {
		t.Errorf("table name parts: %q %q %q", table.Catalog(), table.Schema(), table.Table())
	}
	name := ast.NewUnresolvedName("a", "b", "c", "d")
	if _, err := name.ToUnresolvedObjectName(); err == nil {
		t.Error("a four-part name became an object name")
	}
	obj, err := ast.NewUnresolvedName("t", "s").ToUnresolvedObjectName()
	if err != nil || obj.Object() != "s" || obj.Schema() != "t" || obj.Catalog() != "" {
		t.Errorf("object name: %+v, %v", obj, err)
	}
	if tn := obj.ToTableName(); tn.String() != "t.s" {
		t.Errorf("table name prints %q", tn.String())
	}
	if _, err := ast.NewUnresolvedObjectName(0, [3]string{}); err == nil {
		t.Error("an empty object name was accepted")
	}
	if _, err := ast.NewUnresolvedObjectName(2, [3]string{"a", ""}); err == nil {
		t.Error("an object name with an empty part was accepted")
	}
}

func TestFormatFlags(t *testing.T) {
	name := ast.Name("Mixed Case")
	if got := ast.AsStringWithFlags(&name, ast.FmtBareIdentifiers); got != "Mixed Case" {
		t.Errorf("bare identifier: %q", got)
	}
	if got := ast.AsString(&name); got != `"Mixed Case"` {
		t.Errorf("quoted identifier: %q", got)
	}
	ctx := ast.NewFmtCtx(ast.FmtSimple)
	ctx.WithFlags(ast.FmtBareIdentifiers, func() { ctx.FormatNode(&name) })
	ctx.Printf("-%d", 1)
	if got := ctx.CloseAndGetString(); got != "Mixed Case-1" {
		t.Errorf("WithFlags: %q", got)
	}
}

func loadCases(t *testing.T, out *[]string) {
	t.Helper()
	data, err := os.ReadFile(exprCases)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}
