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
	"fmt"
	"slices"
	"strings"
)

// This file ports graphite-web 1.1.10's target grammar (render/grammar_unsafe.py)
// as a scannerless recursive-descent parser. Pipes desugar at parse time as the
// evaluator does: `a.b | alias('x')` becomes `alias(a.b, 'x')`. Departures fail
// closed: unlike pyparsing, trailing garbage is an error rather than ignored.

// Node is an element of a parsed target expression
type Node interface {
	// appends the canonical form of the node to b
	format(b *strings.Builder)
}

// Call is a function call
type Call struct {
	Func   string
	Args   []Node
	KwArgs []KwArg
	// Raw is the source text of the call. graphite-web passes it verbatim to
	// the finder for seriesByTag rather than evaluating the arguments.
	Raw string
}

// KwArg is a named argument
type KwArg struct {
	Name  string
	Value Node
}

// Path is a metric path expression, possibly containing wildcards
// (* ? [a-z] {a,b}) and backslash escapes. Expr is the source text.
type Path struct {
	Expr string
}

// Number is a numeric literal; Text is the source text
type Number struct {
	Text string
}

// String is a quoted string literal. Value is not unescaped, as graphite-web
// strips only the quotes; Quote records which quote was used.
type String struct {
	Value string
	Quote byte
}

// Bool is a boolean literal
type Bool struct {
	Value bool
}

// None is the `none` literal
type None struct{}

// Inf is the `inf` literal
type Inf struct{}

// Template is a template(...) expression
type Template struct {
	Inner  Node
	Args   []Node
	KwArgs []KwArg
}

var (
	// ErrEmptyTarget is returned for an empty or whitespace-only target
	ErrEmptyTarget = errors.New("empty target expression")
	errSyntax      = errors.New("target syntax error")
)

// SyntaxError describes where a target expression failed to parse
type SyntaxError struct {
	Pos int
	Msg string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%s at position %d: %s", errSyntax.Error(), e.Pos, e.Msg)
}

func (e *SyntaxError) Unwrap() error { return errSyntax }

// ParseTarget parses a single render target expression
func ParseTarget(s string) (Node, error) {
	p := &parser{s: s}
	p.skipWS()
	if p.eof() {
		return nil, ErrEmptyTarget
	}
	n, err := p.expression()
	if err != nil {
		return nil, err
	}
	p.skipWS()
	if !p.eof() {
		return nil, p.errorf("unexpected %q", p.s[p.i])
	}
	if err := rejectAmbiguousArgs(n); err != nil {
		return nil, err
	}
	return n, nil
}

// Fails any call with a positional path argument of the form name=..., which
// graphite-web reads as a keyword argument and then rejects: fail closed.
func rejectAmbiguousArgs(n Node) error {
	var err error
	Walk(n, func(n Node) bool {
		c, ok := n.(*Call)
		if !ok {
			return true
		}
		for _, a := range c.Args {
			if p, ok := a.(*Path); ok && pathLooksLikeKwArg(p.Expr) {
				err = &SyntaxError{Msg: fmt.Sprintf("ambiguous positional argument %q in %s()", p.Expr, c.Func)}
				return false
			}
		}
		return true
	})
	return err
}

func pathLooksLikeKwArg(s string) bool {
	if s == "" || !isIdentStart(s[0]) {
		return false
	}
	i := 1
	for i < len(s) && isIdentChar(s[i]) {
		i++
	}
	return i < len(s) && s[i] == '='
}

// Format returns the canonical text of a parsed expression; two targets with
// the same canonical form produce the same response from graphite-web.
func Format(n Node) string {
	var b strings.Builder
	n.format(&b)
	return b.String()
}

func (n *Call) format(b *strings.Builder) {
	b.WriteString(n.Func)
	b.WriteByte('(')
	for i, a := range n.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		a.format(b)
	}
	kw := n.KwArgs
	if len(kw) > 1 {
		kw = append([]KwArg(nil), kw...)
		slices.SortStableFunc(kw, func(a, b KwArg) int { return strings.Compare(a.Name, b.Name) })
	}
	for i, k := range kw {
		if i > 0 || len(n.Args) > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k.Name)
		b.WriteByte('=')
		k.Value.format(b)
	}
	b.WriteByte(')')
}

func (n *Path) format(b *strings.Builder)   { b.WriteString(n.Expr) }
func (n *Number) format(b *strings.Builder) { b.WriteString(n.Text) }
func (n *None) format(b *strings.Builder)   { b.WriteString("none") }
func (n *Inf) format(b *strings.Builder)    { b.WriteString("inf") }

func (n *Bool) format(b *strings.Builder) {
	if n.Value {
		b.WriteString("true")
		return
	}
	b.WriteString("false")
}

func (n *String) format(b *strings.Builder) {
	q := byte('\'')
	if strings.IndexByte(n.Value, '\'') >= 0 {
		q = '"'
		if strings.IndexByte(n.Value, '"') >= 0 {
			q = n.Quote
		}
	}
	b.WriteByte(q)
	b.WriteString(n.Value)
	b.WriteByte(q)
}

func (n *Template) format(b *strings.Builder) {
	b.WriteString("template(")
	n.Inner.format(b)
	for _, a := range n.Args {
		b.WriteString(", ")
		a.format(b)
	}
	for _, k := range n.KwArgs {
		b.WriteString(", ")
		b.WriteString(k.Name)
		b.WriteByte('=')
		k.Value.format(b)
	}
	b.WriteByte(')')
}

// Walk calls fn for n and every node beneath it, depth first, stopping when
// fn returns false
func Walk(n Node, fn func(Node) bool) bool {
	if !fn(n) {
		return false
	}
	switch t := n.(type) {
	case *Call:
		for _, a := range t.Args {
			if !Walk(a, fn) {
				return false
			}
		}
		for _, k := range t.KwArgs {
			if !Walk(k.Value, fn) {
				return false
			}
		}
	case *Template:
		if !Walk(t.Inner, fn) {
			return false
		}
		for _, a := range t.Args {
			if !Walk(a, fn) {
				return false
			}
		}
		for _, k := range t.KwArgs {
			if !Walk(k.Value, fn) {
				return false
			}
		}
	}
	return true
}

// LeafPaths returns every path expression in the tree, in source order
func LeafPaths(n Node) []string {
	var out []string
	Walk(n, func(n Node) bool {
		if p, ok := n.(*Path); ok {
			out = append(out, p.Expr)
		}
		return true
	})
	return out
}

// ---------------------------------------------------------------------------
// parser

const maxParseDepth = 64

type parser struct {
	s     string
	i     int
	depth int
}

func (p *parser) eof() bool { return p.i >= len(p.s) }

func (p *parser) errorf(format string, args ...any) error {
	return &SyntaxError{Pos: p.i, Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) skipWS() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// True for characters that may appear unescaped in a path: any printable
// ASCII character except the grammar symbols (){},.'"\|
func isMetricChar(c byte) bool {
	if c <= ' ' || c > '~' {
		return false
	}
	switch c {
	case '(', ')', '{', '}', ',', '.', '\'', '"', '\\', '|':
		return false
	}
	return true
}

// True for characters that may follow a backslash in a path
func isEscapable(c byte) bool {
	switch c {
	case '(', ')', '{', '}', ',', '.', '\'', '"', '\\', '|', '=':
		return true
	}
	return false
}

// True for characters that may directly follow a complete argument
func isDelimiter(c byte) bool {
	switch c {
	case 0, ',', ')', '|', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// Scans an identifier at the cursor without consuming it
func (p *parser) ident() string {
	j := p.i
	if j >= len(p.s) || !isIdentStart(p.s[j]) {
		return ""
	}
	for j < len(p.s) && isIdentChar(p.s[j]) {
		j++
	}
	return p.s[p.i:j]
}

// Returns the index of the first non-whitespace byte at or after j
func (p *parser) afterWS(j int) int {
	for j < len(p.s) {
		switch p.s[j] {
		case ' ', '\t', '\n', '\r':
			j++
		default:
			return j
		}
	}
	return j
}

// Reports whether an identifier followed by "(" is at the cursor
func (p *parser) looksLikeCall() bool {
	id := p.ident()
	if id == "" {
		return false
	}
	j := p.afterWS(p.i + len(id))
	return j < len(p.s) && p.s[j] == '('
}

// Reports whether an identifier followed by "=" is at the cursor
func (p *parser) looksLikeKwArg() bool {
	id := p.ident()
	if id == "" {
		return false
	}
	j := p.afterWS(p.i + len(id))
	return j < len(p.s) && p.s[j] == '='
}

func (p *parser) expect(c byte) error {
	p.skipWS()
	if p.peek() != c {
		if p.eof() {
			return p.errorf("expected %q, got end of input", c)
		}
		return p.errorf("expected %q, got %q", c, p.s[p.i])
	}
	p.i++
	return nil
}

func (p *parser) expression() (Node, error) {
	entryDepth := p.depth
	p.depth++
	defer func() { p.depth = entryDepth }()
	if p.depth > maxParseDepth {
		return nil, p.errorf("expression nests more than %d levels", maxParseDepth)
	}
	p.skipWS()
	var n Node
	var err error
	switch {
	case p.ident() == "template" && p.looksLikeCall():
		n, err = p.template()
	case p.looksLikeCall():
		n, err = p.call()
	default:
		n, err = p.path()
	}
	if err != nil {
		return nil, err
	}
	// each pipe desugars into a Call nested around everything parsed so far,
	// so a pipe chain spends the same depth budget as parenthesized nesting
	for {
		j := p.afterWS(p.i)
		if j >= len(p.s) || p.s[j] != '|' {
			return n, nil
		}
		p.depth++
		if p.depth > maxParseDepth {
			return nil, p.errorf("expression nests more than %d levels", maxParseDepth)
		}
		p.i = j + 1
		p.skipWS()
		if !p.looksLikeCall() {
			return nil, p.errorf("expected a function call after '|'")
		}
		c, err := p.call()
		if err != nil {
			return nil, err
		}
		c.Args = append([]Node{n}, c.Args...)
		n = c
	}
}

func (p *parser) call() (*Call, error) {
	start := p.i
	name := p.ident()
	p.i += len(name)
	if err := p.expect('('); err != nil {
		return nil, err
	}
	c := &Call{Func: name}
	p.skipWS()
	if p.peek() != ')' {
		// positional args until a kwarg or ')'; pyparsing's kwarg lookahead
		// fires only on a complete kwarg, so f(a=) is f with the positional path a=
		for {
			if k, ok := p.tryKwArg(); ok {
				c.KwArgs = append(c.KwArgs, k)
				break
			}
			a, err := p.arg()
			if err != nil {
				return nil, err
			}
			c.Args = append(c.Args, a)
			p.skipWS()
			if p.peek() != ',' {
				break
			}
			p.i++
			p.skipWS()
		}
		// remaining kwargs
		for len(c.KwArgs) > 0 {
			p.skipWS()
			if p.peek() != ',' {
				break
			}
			p.i++
			p.skipWS()
			k, ok := p.tryKwArg()
			if !ok {
				return nil, p.errorf("positional argument after keyword argument")
			}
			c.KwArgs = append(c.KwArgs, k)
		}
	}
	if err := p.expect(')'); err != nil {
		return nil, err
	}
	c.Raw = p.s[start:p.i]
	return c, nil
}

// Attempts to parse name=arg at the cursor, restoring the cursor and
// reporting false when a complete keyword argument is not present
func (p *parser) tryKwArg() (KwArg, bool) {
	if !p.looksLikeKwArg() {
		return KwArg{}, false
	}
	save := p.i
	name := p.ident()
	p.i += len(name)
	if err := p.expect('='); err != nil {
		p.i = save
		return KwArg{}, false
	}
	v, err := p.arg()
	if err != nil {
		p.i = save
		return KwArg{}, false
	}
	return KwArg{Name: name, Value: v}, true
}

// Parses one argument: boolean | number | none | string | inf | expression
func (p *parser) arg() (Node, error) {
	p.skipWS()
	if p.eof() {
		return nil, p.errorf("expected an argument, got end of input")
	}
	c := p.peek()
	if c == '"' || c == '\'' {
		return p.str()
	}
	if n, ok := p.keyword(); ok {
		return n, nil
	}
	if n, ok := p.number(); ok {
		return n, nil
	}
	return p.expression()
}

// Matches the case-insensitive literals true/false/none/inf when followed
// by a delimiter
func (p *parser) keyword() (Node, bool) {
	id := p.ident()
	if id == "" || !isDelimiter(p.at(p.i+len(id))) {
		return nil, false
	}
	var n Node
	switch strings.ToLower(id) {
	case "true":
		n = &Bool{Value: true}
	case "false":
		n = &Bool{Value: false}
	case "none":
		n = &None{}
	case "inf":
		n = &Inf{}
	default:
		return nil, false
	}
	p.i += len(id)
	return n, true
}

func (p *parser) at(j int) byte {
	if j < len(p.s) {
		return p.s[j]
	}
	return 0
}

// Matches -?\d+(\.\d+)?([eE]-?\d+)? when followed by , ) or end of input
// (graphite-web's afterNumber rule); anything else, such as 1h, is a path
func (p *parser) number() (Node, bool) {
	j := p.i
	if p.at(j) == '-' {
		j++
	}
	d := leadingDigits(p.s[j:])
	if d == 0 {
		return nil, false
	}
	j += d
	if p.at(j) == '.' {
		f := leadingDigits(p.s[j+1:])
		if f == 0 {
			return nil, false
		}
		j += 1 + f
	}
	if c := p.at(j); c == 'e' || c == 'E' {
		k := j + 1
		if p.at(k) == '-' {
			k++
		}
		e := leadingDigits(p.s[k:])
		if e == 0 {
			return nil, false
		}
		j = k + e
	}
	switch p.at(p.afterWS(j)) {
	case 0, ',', ')':
	default:
		return nil, false
	}
	n := &Number{Text: p.s[p.i:j]}
	p.i = j
	return n, true
}

// Parses a quoted string; backslash escapes the next character but is kept
// in the value, as graphite-web keeps it
func (p *parser) str() (Node, error) {
	q := p.s[p.i]
	j := p.i + 1
	for j < len(p.s) {
		switch p.s[j] {
		case '\\':
			j += 2
			continue
		case q:
			n := &String{Value: p.s[p.i+1 : j], Quote: q}
			p.i = j + 1
			return n, nil
		case '\n', '\r':
			return nil, p.errorf("newline in string literal")
		}
		j++
	}
	return nil, p.errorf("unterminated string literal")
}

// Parses a pathExpression: elements separated by '.', each a sequence of
// partials and {a,b} enumerations
func (p *parser) path() (Node, error) {
	start := p.i
	for {
		if err := p.pathElement(); err != nil {
			return nil, err
		}
		if p.peek() != '.' {
			break
		}
		p.i++
	}
	return &Path{Expr: p.s[start:p.i]}, nil
}

func (p *parser) pathElement() error {
	n := 0
	for {
		switch {
		case p.peek() == '{':
			if err := p.matchEnum(); err != nil {
				return err
			}
		case p.partial():
		default:
			if n == 0 {
				if p.eof() {
					return p.errorf("expected a metric path, got end of input")
				}
				return p.errorf("unexpected %q", p.s[p.i])
			}
			return nil
		}
		n++
	}
}

// Consumes ( "\" symbol | metricChar+ )+ and reports whether anything
// was consumed
func (p *parser) partial() bool {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '\\' && isEscapable(p.at(p.i+1)):
			p.i += 2
		case isMetricChar(c):
			p.i++
		default:
			return p.i > start
		}
	}
	return p.i > start
}

// Parses "{" partial ("," partial)* "}"
func (p *parser) matchEnum() error {
	p.i++ // {
	for {
		if !p.partial() {
			return p.errorf("expected a metric path inside '{}'")
		}
		switch p.peek() {
		case ',':
			p.i++
		case '}':
			p.i++
			return nil
		default:
			return p.errorf("expected ',' or '}' in metric path")
		}
	}
}

// Parses template( (call | path) [, litargs | litkwargs] )
func (p *parser) template() (Node, error) {
	p.i += len("template")
	if err := p.expect('('); err != nil {
		return nil, err
	}
	p.skipWS()
	t := &Template{}
	var err error
	if p.looksLikeCall() {
		t.Inner, err = p.call()
	} else {
		t.Inner, err = p.path()
	}
	if err != nil {
		return nil, err
	}
	p.skipWS()
	for p.peek() == ',' {
		p.i++
		p.skipWS()
		if p.looksLikeKwArg() {
			name := p.ident()
			p.i += len(name)
			if err := p.expect('='); err != nil {
				return nil, err
			}
			v, err := p.literal()
			if err != nil {
				return nil, err
			}
			t.KwArgs = append(t.KwArgs, KwArg{Name: name, Value: v})
		} else {
			v, err := p.literal()
			if err != nil {
				return nil, err
			}
			t.Args = append(t.Args, v)
		}
		p.skipWS()
	}
	if err := p.expect(')'); err != nil {
		return nil, err
	}
	return t, nil
}

// Parses a number or string (template arguments)
func (p *parser) literal() (Node, error) {
	p.skipWS()
	if c := p.peek(); c == '"' || c == '\'' {
		return p.str()
	}
	if n, ok := p.number(); ok {
		return n, nil
	}
	return nil, p.errorf("expected a number or string")
}
