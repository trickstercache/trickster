/*
 * Copyright 2026 The Trickster Authors
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

package ast

// DialectClause is syntax an engine adds to the PostgreSQL-family grammar, kept as written: a
// clause after a query's WINDOW clause, such as SAMPLE BY, or a suffix on a function call.
type DialectClause struct {
	// Keyword is the phrase that opened the clause, as the dialect configured it: lowercase, single-spaced.
	Keyword string
	// Body is the text after the opening phrase, without surrounding space.
	Body string
	// Text is the whole clause as written.
	Text string
}

// Format implements the NodeFormatter interface.
func (node *DialectClause) Format(ctx *FmtCtx) { ctx.WriteString(node.Text) }

// DurationVal is a duration literal such as 5m, in dialects that have them; it prints as written.
type DurationVal struct {
	Text string
}

func (*DurationVal) constantExpr() {}

// Format implements the NodeFormatter interface.
func (node *DurationVal) Format(ctx *FmtCtx) { ctx.WriteString(node.Text) }

func (node *DurationVal) String() string { return node.Text }

// Walk implements the Expr interface.
func (node *DurationVal) Walk(Visitor) Expr { return node }
