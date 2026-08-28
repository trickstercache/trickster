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

package parsing

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseTargetCanonical(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"a.b.c", "a.b.c"},
		{"  a.b.c  ", "a.b.c"},
		{"sumSeries( a.b , a.c )", "sumSeries(a.b, a.c)"},
		{"sumSeries(a.b,a.c)", "sumSeries(a.b, a.c)"},
		{"sumSeries (a.b)", "sumSeries(a.b)"},
		{"aliasByNode(dev.fast.requests.*.count, 3)", "aliasByNode(dev.fast.requests.*.count, 3)"},
		{`alias(sumSeries(dev.fast.requests.*.count), "total")`, "alias(sumSeries(dev.fast.requests.*.count), 'total')"},
		{`aliasSub(aliasByNode(dev.fast.latency.*.p99, 3), "(^.*$)", "\1 A")`,
			`aliasSub(aliasByNode(dev.fast.latency.*.p99, 3), '(^.*$)', '\1 A')`},
		{`alias(a.b, "it's")`, `alias(a.b, "it's")`},
		{`alias(a.b, 'say "hi"')`, `alias(a.b, 'say "hi"')`},
		{`alias(a.b, 'both \' and "')`, `alias(a.b, 'both \' and "')`},
		{`alias(a.b, "both ' and \"")`, `alias(a.b, "both ' and \"")`},
		{"summarize(a.b, \"1h\", \"sum\", true)", "summarize(a.b, '1h', 'sum', true)"},
		{"summarize(a.b, 1h, sum, alignToFrom=True)", "summarize(a.b, 1h, sum, alignToFrom=true)"},
		{"f(a.b, z=1, b=2)", "f(a.b, b=2, z=1)"},
		{"f(a.b, z = 1 , b=2)", "f(a.b, b=2, z=1)"},
		{"f(x=a.b)", "f(x=a.b)"},
		{"a=1", "a=1"},
		{"f(a.b, =1)", "f(a.b, =1)"},
		{"f(a.b, x=a=1)", "f(a.b, x=a=1)"},
		{"a.b | sumSeries() | alias('x')", "alias(sumSeries(a.b), 'x')"},
		{"a.b|scale(2)", "scale(a.b, 2)"},
		{"a.b | scale(2) | alias('y')", "alias(scale(a.b, 2), 'y')"},
		{"a.{b,c}.d", "a.{b,c}.d"},
		{"a.{b,c}x.d", "a.{b,c}x.d"},
		{"{a,b}.c", "{a,b}.c"},
		{"a.*.[a-z]?.c", "a.*.[a-z]?.c"},
		{`a\.b.c`, `a\.b.c`},
		{`a\(b.c`, `a\(b.c`},
		{"host-1.cpu:idle#x.$v.100%", "host-1.cpu:idle#x.$v.100%"},
		{"scale(a.b, -1.5)", "scale(a.b, -1.5)"},
		{"scale(a.b, 1e3)", "scale(a.b, 1e3)"},
		{"scale(a.b, 1.5E-2)", "scale(a.b, 1.5E-2)"},
		{"scale(a.b, 2 )", "scale(a.b, 2)"},
		{"summarize(a.b, 5min)", "summarize(a.b, 5min)"},
		{"transformNull(a.b, none)", "transformNull(a.b, none)"},
		{"removeAboveValue(a.b, INF)", "removeAboveValue(a.b, inf)"},
		{"f(a.b, TRUE, False)", "f(a.b, true, false)"},
		{"f(a.b, true.x)", "f(a.b, true.x)"},
		{"divideSeries(sumSeries(a.*), sumSeries(b.*))", "divideSeries(sumSeries(a.*), sumSeries(b.*))"},
		{"template(a.$b, b='c')", "template(a.$b, b='c')"},
		{`template(hosts.$1.cpu, "worker1")`, "template(hosts.$1.cpu, 'worker1')"},
		{"template(sumSeries(a.$x), x=1)", "template(sumSeries(a.$x), x=1)"},
		{"seriesByTag('name=cpu', 'host=~web.*')", "seriesByTag('name=cpu', 'host=~web.*')"},
		{"constantLine(5)", "constantLine(5)"},
		{"f()", "f()"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			n, err := ParseTarget(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := Format(n); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
			// canonical form must be a fixed point
			n2, err := ParseTarget(Format(n))
			if err != nil {
				t.Fatalf("canonical form failed to parse: %v", err)
			}
			if Format(n2) != Format(n) {
				t.Errorf("canonical form is not a fixed point: %q -> %q", Format(n), Format(n2))
			}
		})
	}
}

func TestParseTargetErrors(t *testing.T) {
	for _, in := range []string{
		"", "   ", "sumSeries(", "sumSeries(a.b))", "a.b,", "f(a=1, b)", "f(a=1, b=)", "f(a.b, k=1, x=)",
		"A=|A()", "A=0|A(0)", "f(a.b, x=)", "f(x=g(a.b, y=))", "alias(a.b, 'x)",
		"alias(a.b, \"x\ny\")", "a..b", ".a", "a.", "a.b c", "|sumSeries", "a.b |", "a.b | x", "a.b | sumSeries",
		"f(,)", "f(a.b,)", "a . b", "a.ü", "a.{b,}", "a.{b", "a.{}", "f(a.b", "(a.b)",
		"f(a.b, 1.)", "a.b)", "template(", "template(a.b, f(x))", "a.b|", "f(a.b, 'x' 'y')",
		"template(a.b, x=f(y))", "template(a.b, 1", "template a.b", `a\`,
	} {
		t.Run(in, func(t *testing.T) {
			n, err := ParseTarget(in)
			if err == nil {
				t.Fatalf("expected error, got %q", Format(n))
			}
			if in != "" && in != "   " {
				var se *SyntaxError
				if !errors.As(err, &se) || !errors.Is(err, errSyntax) {
					t.Errorf("expected a SyntaxError, got %T %v", err, err)
				}
				if se.Error() == "" {
					t.Error("empty error message")
				}
			} else if !errors.Is(err, ErrEmptyTarget) {
				t.Errorf("expected ErrEmptyTarget, got %v", err)
			}
		})
	}
}

func TestParseTargetAST(t *testing.T) {
	n, err := ParseTarget(`summarize(a.{b,c}.*, "1h", sum, alignToFrom=true) | alias("x")`)
	if err != nil {
		t.Fatal(err)
	}
	alias, ok := n.(*Call)
	if !ok || alias.Func != "alias" || len(alias.Args) != 2 {
		t.Fatalf("unexpected root: %#v", n)
	}
	if s, ok := alias.Args[1].(*String); !ok || s.Value != "x" || s.Quote != '"' {
		t.Errorf("unexpected alias arg: %#v", alias.Args[1])
	}
	sum, ok := alias.Args[0].(*Call)
	if !ok || sum.Func != "summarize" || len(sum.Args) != 3 || len(sum.KwArgs) != 1 {
		t.Fatalf("unexpected piped call: %#v", alias.Args[0])
	}
	if sum.Raw != `summarize(a.{b,c}.*, "1h", sum, alignToFrom=true)` {
		t.Errorf("unexpected raw: %q", sum.Raw)
	}
	if p, ok := sum.Args[0].(*Path); !ok || p.Expr != "a.{b,c}.*" {
		t.Errorf("unexpected path: %#v", sum.Args[0])
	}
	if s, ok := sum.Args[1].(*String); !ok || s.Value != "1h" {
		t.Errorf("unexpected interval: %#v", sum.Args[1])
	}
	if p, ok := sum.Args[2].(*Path); !ok || p.Expr != "sum" {
		t.Errorf("unquoted func name must parse as a path: %#v", sum.Args[2])
	}
	if b, ok := sum.KwArgs[0].Value.(*Bool); !ok || !b.Value || sum.KwArgs[0].Name != "alignToFrom" {
		t.Errorf("unexpected kwarg: %#v", sum.KwArgs[0])
	}
	if leaves := LeafPaths(n); !reflect.DeepEqual(leaves, []string{"a.{b,c}.*", "sum"}) {
		t.Errorf("unexpected leaves: %v", leaves)
	}

	n, err = ParseTarget("scale(a.b, -2.5e1)")
	if err != nil {
		t.Fatal(err)
	}
	if num, ok := n.(*Call).Args[1].(*Number); !ok || num.Text != "-2.5e1" {
		t.Errorf("unexpected number: %#v", n.(*Call).Args[1])
	}

	n, err = ParseTarget("template(a.$1, 'x', k=2)")
	if err != nil {
		t.Fatal(err)
	}
	tpl, ok := n.(*Template)
	if !ok || len(tpl.Args) != 1 || len(tpl.KwArgs) != 1 {
		t.Fatalf("unexpected template: %#v", n)
	}
	if _, ok := tpl.Inner.(*Path); !ok {
		t.Errorf("unexpected template inner: %#v", tpl.Inner)
	}

	// Walk stops when fn returns false
	count := 0
	Walk(n, func(Node) bool { count++; return false })
	if count != 1 {
		t.Errorf("expected walk to stop after 1 node, visited %d", count)
	}
	for _, in := range []string{"f(a.b, x=g(c.d))", "template(f(a.b), 1)"} {
		n, _ = ParseTarget(in)
		count = 0
		Walk(n, func(Node) bool { count++; return count < 2 })
		if count != 2 {
			t.Errorf("%s: expected walk to stop after 2 nodes, visited %d", in, count)
		}
	}
	// a full walk visits every node, including template args and kwargs
	n, _ = ParseTarget("template(f(a.b, k=g(c.d)), 1, x='y')")
	count = 0
	Walk(n, func(Node) bool { count++; return true })
	if count != 7 {
		t.Errorf("expected 7 nodes, visited %d", count)
	}
	if leaves := LeafPaths(n); !reflect.DeepEqual(leaves, []string{"a.b", "c.d"}) {
		t.Errorf("unexpected leaves: %v", leaves)
	}
	if describe(&Path{Expr: "a"}) != "expression" || describe(&Call{Func: "f"}) != "f" {
		t.Error("unexpected describe")
	}
	if s := StepUnknown.String() + StepInherit.String() + StepFixed.String() + StepShift.String(); s != "unknowninheritfixedshift" {
		t.Error(s)
	}
}

func FuzzParseTarget(f *testing.F) {
	for _, s := range []string{"a.b.c", "sumSeries(a.b, a.c)", `aliasSub(a.b, "(^.*$)", "\1 A")`,
		"a.b | sumSeries() | alias('x')", "a.{b,c}.*", `a\.b`, "summarize(a.b, 1h, sum, alignToFrom=true)",
		"template(a.$1, 'x')", "f(", ")", "{", "\"", "'", "|", "f(a.b, -1.5e3)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := ParseTarget(s)
		if err != nil {
			return
		}
		// the canonical form must re-parse to itself
		c := Format(n)
		n2, err := ParseTarget(c)
		if err != nil {
			t.Fatalf("canonical form %q of %q does not parse: %v", c, s, err)
		}
		if Format(n2) != c {
			t.Fatalf("canonical form not a fixed point: %q -> %q", c, Format(n2))
		}
		Classify(n)
	})
}

func TestParseTargetDepthLimit(t *testing.T) {
	nest := func(depth int) string {
		return strings.Repeat("absolute(", depth) + "a.b" + strings.Repeat(")", depth)
	}
	if _, err := ParseTarget(nest(20)); err != nil {
		t.Fatalf("20 levels must parse: %v", err)
	}
	if _, err := ParseTarget(nest(maxParseDepth + 10)); err == nil {
		t.Fatal("expected a depth error")
	}
	// far beyond the cap: this is the input that would overflow the stack
	if _, err := ParseTarget(nest(200_000)); err == nil {
		t.Fatal("expected a depth error for a pathological input")
	}

	// pipes desugar into the same nesting and must spend the same budget:
	// every downstream walk recurses one AST level per pipe
	pipes := func(depth int) string {
		return "a.b" + strings.Repeat("|absolute()", depth)
	}
	n, err := ParseTarget(pipes(20))
	if err != nil {
		t.Fatalf("20 pipes must parse: %v", err)
	}
	// the walks that recurse over the piped AST stay usable at legal depth
	Classify(n)
	_ = Format(n)
	if _, err := ParseTarget(pipes(maxParseDepth + 10)); err == nil {
		t.Fatal("expected a depth error for a long pipe chain")
	}
	if _, err := ParseTarget(pipes(200_000)); err == nil {
		t.Fatal("expected a depth error for a pathological pipe chain")
	}
	// mixing the two constructions cannot evade the cap either
	mixed := strings.Repeat("absolute(", 40) + pipes(40) + strings.Repeat(")", 40)
	if _, err := ParseTarget(mixed); err == nil {
		t.Fatal("expected a depth error for mixed nesting and pipes")
	}
}
