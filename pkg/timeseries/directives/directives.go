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

// Package directives reads the trickster-* directives clients write into query comments.
package directives

import (
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Prefix begins every directive, whose name and value follow as <name>:<value>
const Prefix = "trickster-"

// directive names, as written after Prefix
const (
	NameFastForward    = "fast-forward"
	NameVolatileWindow = "volatile-window"
	NameStepAlign      = "step-align"
	// NameBackfillTolerance sets the volatile window of a query that doesn't name one itself
	NameBackfillTolerance = "backfill-tolerance"
)

// Names are every directive's name
var Names = [...]string{NameFastForward, NameVolatileWindow, NameBackfillTolerance, NameStepAlign}

const (
	valueOff       = "off"
	valueOn        = "on"
	nameSeparator  = ':'
	tokenSeparator = " "
)

// Syntax names a query language's comment and quoting forms
type Syntax uint8

const (
	// SyntaxNone is a language with no comments, whose queries carry no directives
	SyntaxNone Syntax = iota
	// SyntaxPromQL is PromQL: # comments; ' and " strings with escapes, and raw ` strings
	SyntaxPromQL
	// SyntaxSQL is PostgreSQL, DataFusion and Druid SQL: -- and /* */ comments; ' and " quotes that
	// escape by doubling, and $$ or $tag$ strings
	SyntaxSQL
	// SyntaxMySQL is MySQL: "-- " (with the whitespace MySQL requires), # and /* */ comments; ', " and
	// ` quotes that escape with a backslash or by doubling
	SyntaxMySQL
	// SyntaxClickHouse is ClickHouse SQL: --, # and /* */ comments; ', " and ` quotes that escape
	// with a backslash or by doubling
	SyntaxClickHouse
	// SyntaxInfluxQL is InfluxQL: -- and /* */ comments; ' and " quotes that escape with a backslash
	SyntaxInfluxQL
	// SyntaxFlux is Flux: // comments; " strings that escape with a backslash
	SyntaxFlux
)

type grammar struct {
	dashDash, dashDashSpace, hash, slashSlash, block bool
	quotes                                           string
	backslash, doubled, rawBacktick, dollar          bool
	// escapeStrings is PostgreSQL's E'...', whose backslashes escape as they don't in plain strings
	escapeStrings bool
}

var grammars = [...]grammar{
	SyntaxPromQL: {hash: true, quotes: "'\"`", backslash: true, rawBacktick: true},
	SyntaxSQL: {
		dashDash: true, block: true, quotes: "'\"", doubled: true, dollar: true, escapeStrings: true,
	},
	SyntaxMySQL:      {dashDash: true, dashDashSpace: true, hash: true, block: true, quotes: "'\"`", backslash: true, doubled: true},
	SyntaxClickHouse: {dashDash: true, hash: true, block: true, quotes: "'\"`", backslash: true, doubled: true},
	SyntaxInfluxQL:   {dashDash: true, block: true, quotes: "'\"", backslash: true},
	SyntaxFlux:       {slashSlash: true, quotes: "\"", backslash: true},
}

func (s Syntax) grammar() *grammar {
	if s == SyntaxNone || int(s) >= len(grammars) {
		return nil
	}
	return &grammars[s]
}

// comment is one comment's position: it runs from start to end, and its text from body to bodyEnd
type comment struct {
	start, body, bodyEnd, end int
}

func scan(s string, g *grammar, yield func(comment)) {
	// every comment outside quoted text, in order
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case strings.IndexByte(g.quotes, c) >= 0:
			i = skipQuoted(s, i, g, g.escapeStrings && c == '\'' && escapeString(s, i))
		case c == '$' && g.dollar:
			i = skipDollarQuoted(s, i)
		case c == '-' && g.dashDash && i+1 < len(s) && s[i+1] == '-' &&
			(!g.dashDashSpace || i+2 == len(s) || s[i+2] <= ' '):
			i = lineComment(s, i, i+2, yield)
		case c == '#' && g.hash:
			i = lineComment(s, i, i+1, yield)
		case c == '/' && g.slashSlash && i+1 < len(s) && s[i+1] == '/':
			i = lineComment(s, i, i+2, yield)
		case c == '/' && g.block && i+1 < len(s) && s[i+1] == '*':
			closing := strings.Index(s[i+2:], "*/")
			if closing < 0 {
				// the origin rejects an unclosed comment, so its text is read to the end
				yield(comment{start: i, body: i + 2, bodyEnd: len(s), end: len(s)})
				return
			}
			bodyEnd := i + 2 + closing
			yield(comment{start: i, body: i + 2, bodyEnd: bodyEnd, end: bodyEnd + 2})
			i = bodyEnd + 2
		default:
			i++
		}
	}
}

func lineComment(s string, start, body int, yield func(comment)) int {
	end := len(s)
	if nl := strings.IndexByte(s[body:], '\n'); nl >= 0 {
		end = body + nl
	}
	yield(comment{start: start, body: body, bodyEnd: end, end: end})
	return end
}

func escapeString(s string, i int) bool {
	// the quote at i opens E'...': an E or e that isn't the end of a longer word
	return i > 0 && s[i-1]|0x20 == 'e' && (i < 2 || !isWordByte(s[i-2]))
}

func isWordByte(c byte) bool { return isLetter(c) || isDigit(c) || c == '_' || c == '$' }

func skipQuoted(s string, i int, g *grammar, backslash bool) int {
	// the index just past the quoted text opening at i
	q := s[i]
	escapes := (g.backslash || backslash) && (q != '`' || !g.rawBacktick)
	for j := i + 1; j < len(s); j++ {
		switch {
		case s[j] == '\\' && escapes:
			j++
		case s[j] == q:
			if g.doubled && j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

func skipDollarQuoted(s string, i int) int {
	// $$ or $tag$ opens a string closed by the same tag; any other $ (as in $1) is one character
	j := i + 1
	for j < len(s) && (s[j] == '_' || isLetter(s[j]) || (j > i+1 && isDigit(s[j]))) {
		j++
	}
	if j >= len(s) || s[j] != '$' {
		return i + 1
	}
	tag := s[i : j+1]
	closing := strings.Index(s[j+1:], tag)
	if closing < 0 {
		return len(s)
	}
	return j + 1 + closing + len(tag)
}

func isLetter(c byte) bool { return (c|0x20) >= 'a' && (c|0x20) <= 'z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

func isNameByte(c byte) bool { return isLetter(c) || c == '-' }

func isValueByte(c byte) bool { return isLetter(c) || isDigit(c) || c == '_' || c == '.' }

// token is one directive in a comment's text: <Prefix><name>:<value>, from start to end
type token struct {
	name, value string
	start, end  int
}

func tokens(s string, from, to int, yield func(token)) {
	for i := from; i < to; {
		at := strings.Index(s[i:to], Prefix)
		if at < 0 {
			return
		}
		at += i
		i = at + len(Prefix)
		if at > from && (isNameByte(s[at-1]) || isDigit(s[at-1]) || s[at-1] == '_') {
			// part of a longer word
			continue
		}
		n := i
		for n < to && isNameByte(s[n]) {
			n++
		}
		if n == i || n >= to || s[n] != nameSeparator {
			continue
		}
		v := n + 1
		for v < to && isValueByte(s[v]) {
			v++
		}
		yield(token{name: s[i:n], value: s[n+1 : v], start: at, end: v})
		i = v
	}
}

// Parse returns the directives in statement's comments; text outside them, string literals
// included, never matches, and when one appears more than once the last wins
func Parse(statement string, syntax Syntax) timeseries.Directives {
	var st state
	st.parse(statement, syntax)
	return st.directives()
}

// Read returns the directives lookup finds by name, for a query that carries them outside comments,
// as Druid's native context map does; they apply as Parse applies them
func Read(lookup func(name string) (string, bool)) timeseries.Directives {
	var st state
	st.read(lookup)
	return st.directives()
}

// ParseWith returns the directives in statement's comments and lookup's: a name the comments set
// wins, and trickster-volatile-window from either outranks the fallback
func ParseWith(statement string, syntax Syntax, lookup func(name string) (string, bool)) timeseries.Directives {
	var st, outside state
	st.parse(statement, syntax)
	outside.read(lookup)
	st.fill(outside)
	return st.directives()
}

// the names a state holds a valid value for
const (
	setWindow uint8 = 1 << iota
	setFallback
	setMode
	setFastForward
)

// state holds each name's value until every source is read, so the fallback yields to a window
// named in any of them
type state struct {
	window, fallback time.Duration
	mode             timeseries.StepAlignment
	ffDisable        bool
	set              uint8
}

func (st *state) parse(statement string, syntax Syntax) {
	g := syntax.grammar()
	if g == nil || !strings.Contains(statement, Prefix) {
		return
	}
	scan(statement, g, func(c comment) {
		tokens(statement, c.body, c.bodyEnd, func(t token) {
			st.apply(t.name, t.value)
		})
	})
}

func (st *state) read(lookup func(name string) (string, bool)) {
	for _, name := range Names {
		if value, ok := lookup(name); ok {
			st.apply(name, value)
		}
	}
}

func (st *state) fill(other state) {
	// each name other sets and st doesn't
	missing := other.set &^ st.set
	if missing&setWindow != 0 {
		st.window = other.window
	}
	if missing&setFallback != 0 {
		st.fallback = other.fallback
	}
	if missing&setMode != 0 {
		st.mode = other.mode
	}
	if missing&setFastForward != 0 {
		st.ffDisable = other.ffDisable
	}
	st.set |= missing
}

func (st state) directives() timeseries.Directives {
	// trickster-backfill-tolerance applies only to a query without trickster-volatile-window
	d := timeseries.Directives{VolatileWindow: st.window, StepAlignment: st.mode, FastForwardDisable: st.ffDisable}
	if st.set&setWindow == 0 {
		d.VolatileWindow = st.fallback
	}
	return d
}

func (st *state) apply(name, value string) {
	switch name {
	case NameFastForward:
		switch value {
		case valueOff, valueOn:
			st.ffDisable, st.set = value == valueOff, st.set|setFastForward
			return
		}
	case NameVolatileWindow:
		if w, ok := parseWindow(value); ok {
			st.window, st.set = w, st.set|setWindow
			return
		}
	case NameBackfillTolerance:
		if w, ok := parseWindow(value); ok {
			st.fallback, st.set = w, st.set|setFallback
			return
		}
	case NameStepAlign:
		if mode, err := timeseries.ParseStepAlignment(value); err == nil && mode != 0 {
			st.mode, st.set = mode, st.set|setMode
			return
		}
	}
	logger.Debug("ignoring an invalid query directive",
		logging.Pairs{keys.Detail: Prefix + name + string(nameSeparator) + value})
}

func parseWindow(value string) (time.Duration, bool) {
	// whole seconds or a Go duration
	if value != "" && strings.TrimLeft(value, "0123456789") == "" {
		seconds, err := strconv.ParseInt(value, 10, 32)
		return time.Duration(seconds) * time.Second, err == nil
	}
	w, err := time.ParseDuration(value)
	return w, err == nil && w >= 0
}

// Strip returns statement without its directives, for a key the same with or without them; a
// comment left holding only whitespace goes too, with the whitespace before it
func Strip(statement string, syntax Syntax) string {
	g := syntax.grammar()
	if g == nil || !strings.Contains(statement, Prefix) {
		return statement
	}
	var b strings.Builder
	last, changed := 0, false
	scan(statement, g, func(c comment) {
		cut, kept := -1, false
		prev := c.body
		tokens(statement, c.body, c.bodyEnd, func(t token) {
			if strings.TrimSpace(statement[prev:t.start]) != "" {
				kept = true
			}
			if cut < 0 {
				cut = t.start
			}
			prev = t.end
		})
		if cut < 0 {
			return
		}
		changed = true
		if !kept && strings.TrimSpace(statement[prev:c.bodyEnd]) == "" {
			start := c.start
			for start > last && isSpace(statement[start-1]) {
				start--
			}
			end := c.end
			if b.Len() == 0 && start == last {
				// nothing precedes the comment, so the whitespace after it separates nothing
				for end < len(statement) && isSpace(statement[end]) {
					end++
				}
			}
			b.WriteString(statement[last:start])
			last = end
			return
		}
		tokens(statement, c.body, c.bodyEnd, func(t token) {
			start := t.start
			for start > c.body && isSpace(statement[start-1]) {
				start--
			}
			b.WriteString(statement[last:start])
			last = t.end
		})
	})
	if !changed {
		return statement
	}
	b.WriteString(statement[last:])
	return b.String()
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// Format returns d as space-separated directive tokens, for a comment carrying them to a statement
// rewritten without the client's comments; it is empty when d sets nothing
func Format(d timeseries.Directives) string {
	if d.IsZero() {
		return ""
	}
	var parts [3]string
	n := 0
	if d.VolatileWindow > 0 {
		parts[n] = Prefix + NameVolatileWindow + string(nameSeparator) + d.VolatileWindow.String()
		n++
	}
	if d.StepAlignment.IsMode() {
		parts[n] = Prefix + NameStepAlign + string(nameSeparator) + d.StepAlignment.String()
		n++
	}
	if d.FastForwardDisable {
		parts[n] = Prefix + NameFastForward + string(nameSeparator) + valueOff
		n++
	}
	return strings.Join(parts[:n], tokenSeparator)
}
