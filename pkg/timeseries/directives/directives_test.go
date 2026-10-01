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

package directives

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	dropDirective = "trickster-step-align:drop"
	drop          = timeseries.StepAlignmentDrop
)

var dropped = timeseries.Directives{StepAlignment: drop}

func TestParseReadsOnlyComments(t *testing.T) {
	for _, test := range []struct {
		name, statement string
		syntax          Syntax
		want            timeseries.Directives
	}{
		{"no directive", "up", SyntaxPromQL, timeseries.Directives{}},
		{"no comment syntax", "up # " + dropDirective, SyntaxNone, timeseries.Directives{}},
		{"an unknown syntax", "up # " + dropDirective, Syntax(200), timeseries.Directives{}},
		{"outside a comment", "up " + dropDirective, SyntaxPromQL, timeseries.Directives{}},

		{"PromQL #", "sum(rate(x[5m])) # " + dropDirective, SyntaxPromQL, dropped},
		{"PromQL at the start", "# " + dropDirective + "\nup", SyntaxPromQL, dropped},
		{"PromQL double-quoted", `up{job="# ` + dropDirective + `"}`, SyntaxPromQL, timeseries.Directives{}},
		{"PromQL single-quoted", `up{job='# ` + dropDirective + `'}`, SyntaxPromQL, timeseries.Directives{}},
		{"PromQL escaped quote", `up{job="a\" # ` + dropDirective + `"}`, SyntaxPromQL, timeseries.Directives{}},
		// a raw string has no escapes, so its backslash doesn't hide the closing quote
		{"PromQL raw string", "up{job=`a\\` # " + dropDirective, SyntaxPromQL, dropped},

		{"SQL --", "SELECT 1 -- " + dropDirective, SyntaxSQL, dropped},
		{"SQL /* */", "SELECT /* " + dropDirective + " */ 1", SyntaxSQL, dropped},
		{"SQL unclosed /*", "SELECT 1 /* " + dropDirective, SyntaxSQL, dropped},
		{"SQL literal", "SELECT '-- " + dropDirective + "'", SyntaxSQL, timeseries.Directives{}},
		{"SQL doubled quote", "SELECT 'it''s -- " + dropDirective + "'", SyntaxSQL, timeseries.Directives{}},
		{"SQL identifier", `SELECT "a -- ` + dropDirective + `" FROM t`, SyntaxSQL, timeseries.Directives{}},
		{"SQL $$ string", "SELECT $$ -- " + dropDirective + " $$", SyntaxSQL, timeseries.Directives{}},
		{"SQL $tag$ string", "SELECT $q$ -- " + dropDirective + " $q$", SyntaxSQL, timeseries.Directives{}},
		{"SQL unclosed $$", "SELECT $$ -- " + dropDirective, SyntaxSQL, timeseries.Directives{}},
		{"SQL parameters", "SELECT $1, $2 -- " + dropDirective, SyntaxSQL, dropped},
		{"SQL #", "SELECT 1 # " + dropDirective, SyntaxSQL, timeseries.Directives{}},
		// PostgreSQL strings don't escape with a backslash, so the comment after this one is real
		{"SQL backslash", `SELECT 'a\' -- ` + dropDirective, SyntaxSQL, dropped},
		// an escape string's backslash escapes its quote, so the directive text stays inside it
		{"SQL escape string", `SELECT E'it\'s -- ` + dropDirective + `'`, SyntaxSQL, timeseries.Directives{}},
		{"SQL lowercase escape string", `SELECT e'it\'s -- ` + dropDirective + `'`, SyntaxSQL, timeseries.Directives{}},
		{"SQL comment after an escape string", `SELECT E'it\'s' -- ` + dropDirective, SyntaxSQL, dropped},
		{"SQL escape string at the start", `E'\' -- ` + dropDirective, SyntaxSQL, timeseries.Directives{}},
		// a typed literal whose type ends in e is no escape string, so its backslash escapes nothing
		{"SQL typed literal ending in e", `SELECT time'a\' -- ` + dropDirective, SyntaxSQL, dropped},

		{"MySQL #", "SELECT 1 # " + dropDirective, SyntaxMySQL, dropped},
		{"MySQL -- ", "SELECT 1 -- " + dropDirective, SyntaxMySQL, dropped},
		{"MySQL --\\t", "SELECT 1 --\t" + dropDirective, SyntaxMySQL, dropped},
		{"MySQL -- at the end", "SELECT 1 --", SyntaxMySQL, timeseries.Directives{}},
		{"MySQL -- without a space", "SELECT 2--" + dropDirective, SyntaxMySQL, timeseries.Directives{}},
		{"MySQL backslash", `SELECT 'can\'t # ` + dropDirective + `'`, SyntaxMySQL, timeseries.Directives{}},
		{"MySQL backtick", "SELECT `a # " + dropDirective + "`", SyntaxMySQL, timeseries.Directives{}},
		{"MySQL /* */", "SELECT /* " + dropDirective + " */ 1", SyntaxMySQL, dropped},

		{"ClickHouse #", "SELECT 1 # " + dropDirective, SyntaxClickHouse, dropped},
		{"ClickHouse --", "SELECT 1 --" + dropDirective, SyntaxClickHouse, dropped},
		{"ClickHouse literal", `SELECT 'a\' -- ` + dropDirective + `'`, SyntaxClickHouse, timeseries.Directives{}},

		{"InfluxQL --", "SELECT mean(v) FROM cpu -- " + dropDirective, SyntaxInfluxQL, dropped},
		{"InfluxQL /* */", "SELECT /* " + dropDirective + " */ mean(v) FROM cpu", SyntaxInfluxQL, dropped},
		{
			"InfluxQL literal", "SELECT v FROM cpu WHERE h = '-- " + dropDirective + "'", SyntaxInfluxQL,
			timeseries.Directives{},
		},
		{"InfluxQL #", "SELECT v FROM cpu # " + dropDirective, SyntaxInfluxQL, timeseries.Directives{}},

		{"Flux //", "from(bucket: \"b\") // " + dropDirective, SyntaxFlux, dropped},
		{"Flux literal", `from(bucket: "b // ` + dropDirective + `")`, SyntaxFlux, timeseries.Directives{}},
		{"Flux --", "from(bucket: \"b\") -- " + dropDirective, SyntaxFlux, timeseries.Directives{}},
		{"Flux single slash", "x = 4 / 2 // " + dropDirective, SyntaxFlux, dropped},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Parse(test.statement, test.syntax); got != test.want {
				t.Errorf("Parse(%q) = %+v", test.statement, got)
			}
		})
	}
}

func TestParseValues(t *testing.T) {
	window := func(d time.Duration) timeseries.Directives { return timeseries.Directives{VolatileWindow: d} }
	for _, test := range []struct {
		name, comment string
		want          timeseries.Directives
	}{
		{"whole seconds", "trickster-volatile-window:90", window(90 * time.Second)},
		{"a duration", "trickster-volatile-window:5m", window(5 * time.Minute)},
		{"a fractional duration", "trickster-volatile-window:1.5m", window(90 * time.Second)},
		{"zero", "trickster-volatile-window:0", timeseries.Directives{}},
		{"a negative duration", "trickster-volatile-window:-5m", timeseries.Directives{}},
		{"not a duration", "trickster-volatile-window:soon", timeseries.Directives{}},
		{"too many seconds", "trickster-volatile-window:99999999999", timeseries.Directives{}},
		{"the last window wins", "trickster-volatile-window:30 trickster-volatile-window:60", window(time.Minute)},
		{"the fallback window", "trickster-backfill-tolerance:30", window(30 * time.Second)},
		{"the fallback window as a duration", "trickster-backfill-tolerance:2m", window(2 * time.Minute)},
		{"an invalid fallback window", "trickster-backfill-tolerance:soon", timeseries.Directives{}},
		{
			"a volatile window after the fallback", "trickster-backfill-tolerance:30 trickster-volatile-window:60",
			window(time.Minute),
		},
		{
			"a volatile window before the fallback", "trickster-volatile-window:60 trickster-backfill-tolerance:30",
			window(time.Minute),
		},
		{"the last fallback wins", "trickster-backfill-tolerance:30 trickster-backfill-tolerance:45", window(45 * time.Second)},
		// a window named at all outranks the fallback, even one of zero
		{"a zero window with the fallback", "trickster-volatile-window:0 trickster-backfill-tolerance:30", timeseries.Directives{}},
		{"every mode name", "trickster-step-align:partial_end", timeseries.Directives{
			StepAlignment: timeseries.StepAlignmentPartialEnd,
		}},
		{"a mode in capitals", "trickster-step-align:DROP", dropped},
		{"an unknown mode", "trickster-step-align:exact", timeseries.Directives{}},
		{"the last mode wins", "trickster-step-align:off trickster-step-align:drop", dropped},
		{"fast forward off", "trickster-fast-forward:off", timeseries.Directives{FastForwardDisable: true}},
		{"fast forward back on", "trickster-fast-forward:off trickster-fast-forward:on", timeseries.Directives{}},
		{"fast forward junk", "trickster-fast-forward:maybe", timeseries.Directives{}},
		{"an unknown directive", "trickster-cache:never", timeseries.Directives{}},
		{"no value separator", "trickster-step-align drop", timeseries.Directives{}},
		{"no name", "trickster-:drop", timeseries.Directives{}},
		{"part of a longer word", "xtrickster-step-align:drop", timeseries.Directives{}},
		{
			"every directive", "trickster-volatile-window:2m, trickster-step-align:partial; trickster-fast-forward:off",
			timeseries.Directives{
				VolatileWindow: 2 * time.Minute, StepAlignment: timeseries.StepAlignmentPartial,
				FastForwardDisable: true,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Parse("SELECT 1 /* "+test.comment+" */", SyntaxSQL); got != test.want {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestRead(t *testing.T) {
	read := func(values map[string]string) timeseries.Directives {
		return Read(func(name string) (string, bool) {
			v, ok := values[name]
			return v, ok
		})
	}
	for _, test := range []struct {
		name   string
		values map[string]string
		want   timeseries.Directives
	}{
		{"none", nil, timeseries.Directives{}},
		{
			"every directive",
			map[string]string{NameVolatileWindow: "30", NameStepAlign: "partial", NameFastForward: "off"},
			timeseries.Directives{
				VolatileWindow: 30 * time.Second, StepAlignment: timeseries.StepAlignmentPartial,
				FastForwardDisable: true,
			},
		},
		{"the fallback window", map[string]string{NameBackfillTolerance: "45"}, timeseries.Directives{
			VolatileWindow: 45 * time.Second,
		}},
		{
			"a volatile window with the fallback",
			map[string]string{NameBackfillTolerance: "45", NameVolatileWindow: "30"},
			timeseries.Directives{VolatileWindow: 30 * time.Second},
		},
		{"invalid values", map[string]string{NameVolatileWindow: "soon", NameStepAlign: ""}, timeseries.Directives{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := read(test.values); got != test.want {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestParseWith(t *testing.T) {
	window := func(d time.Duration) timeseries.Directives { return timeseries.Directives{VolatileWindow: d} }
	for _, test := range []struct {
		name, comment string
		outside       map[string]string
		want          timeseries.Directives
	}{
		{"neither", "", nil, timeseries.Directives{}},
		{"outside alone", "", map[string]string{NameStepAlign: "drop"}, dropped},
		{"a comment over outside", "trickster-step-align:drop", map[string]string{NameStepAlign: "partial"}, dropped},
		{
			"a window outside over a comment's fallback", "trickster-backfill-tolerance:45",
			map[string]string{NameVolatileWindow: "30"},
			window(30 * time.Second),
		},
		{
			"a comment's window over a fallback outside", "trickster-volatile-window:30",
			map[string]string{NameBackfillTolerance: "45"},
			window(30 * time.Second),
		},
		{
			"a comment's fallback over one outside", "trickster-backfill-tolerance:45",
			map[string]string{NameBackfillTolerance: "60"},
			window(45 * time.Second),
		},
		{
			"fast forward off outside", "", map[string]string{NameFastForward: "off"},
			timeseries.Directives{FastForwardDisable: true},
		},
		{
			"fast forward back on in a comment", "trickster-fast-forward:on",
			map[string]string{NameFastForward: "off"},
			timeseries.Directives{},
		},
		{
			"an invalid comment value yields to outside", "trickster-step-align:exact",
			map[string]string{NameStepAlign: "drop"},
			dropped,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ParseWith("SELECT 1 -- "+test.comment, SyntaxSQL, func(name string) (string, bool) {
				v, ok := test.outside[name]
				return v, ok
			})
			if got != test.want {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestStrip(t *testing.T) {
	for _, test := range []struct {
		name, statement, want string
		syntax                Syntax
	}{
		{"no directive", "up # note", "up # note", SyntaxPromQL},
		{"no comment syntax", "up trickster-step-align:drop", "up trickster-step-align:drop", SyntaxNone},
		{"a comment holding only a directive", "up # " + dropDirective, "up", SyntaxPromQL},
		{"a directive before a newline", "up # " + dropDirective + "\n+ 1", "up\n+ 1", SyntaxPromQL},
		{"a directive beside a note", "up # note " + dropDirective, "up # note", SyntaxPromQL},
		{"a directive before a note", "SELECT 1 /* " + dropDirective + " note */", "SELECT 1 /* note */", SyntaxSQL},
		{"a comment mid-statement", "SELECT 1 /* " + dropDirective + " */ FROM t", "SELECT 1 FROM t", SyntaxSQL},
		{"two directives", "SELECT 1 /* " + dropDirective + " trickster-fast-forward:off */", "SELECT 1", SyntaxSQL},
		{"two comments", "SELECT 1 -- " + dropDirective + "\n-- keep\n", "SELECT 1\n-- keep\n", SyntaxSQL},
		{"a directive-like literal", "SELECT '" + dropDirective + "'", "SELECT '" + dropDirective + "'", SyntaxSQL},
		{"an invalid directive", "up # trickster-step-align:exact", "up", SyntaxPromQL},
		{"a comment at the start", "# " + dropDirective + "\nup", "up", SyntaxPromQL},
		{"two comments at the start", "# " + dropDirective + "\n  # trickster-fast-forward:off\n up", "up", SyntaxPromQL},
		{"a comment after an expression and a newline", "up\n# " + dropDirective, "up", SyntaxPromQL},
		{"a comment between lines", "sum(up)\n# " + dropDirective + "\n+ 1", "sum(up)\n+ 1", SyntaxPromQL},
		{"a kept comment at the start", "# note\n# " + dropDirective + "\nup", "# note\nup", SyntaxPromQL},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Strip(test.statement, test.syntax); got != test.want {
				t.Errorf("Strip(%q) = %q, want %q", test.statement, got, test.want)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	if got := Format(timeseries.Directives{}); got != "" {
		t.Fatalf("no directives formatted as %q", got)
	}
	all := timeseries.Directives{
		VolatileWindow: 90 * time.Second, StepAlignment: timeseries.StepAlignmentPartialEnd, FastForwardDisable: true,
	}
	want := "trickster-volatile-window:1m30s trickster-step-align:partial_end trickster-fast-forward:off"
	if got := Format(all); got != want {
		t.Fatalf("got %q", got)
	}
	// each parses back as it was
	for _, d := range []timeseries.Directives{all, dropped, {FastForwardDisable: true}, {VolatileWindow: time.Second}} {
		if got := Parse("SELECT 1 /* "+Format(d)+" */", SyntaxClickHouse); got != d {
			t.Errorf("%+v parsed back as %+v", d, got)
		}
	}
}

func TestParseAllocations(t *testing.T) {
	for _, statement := range []string{
		"SELECT count(*) FROM t WHERE ts >= '2026-01-01' -- a note",
		"SELECT 1 /* trickster-volatile-window:90 trickster-step-align:drop trickster-fast-forward:off */",
	} {
		if n := testing.AllocsPerRun(100, func() { _ = Parse(statement, SyntaxSQL) }); n != 0 {
			t.Errorf("%q: %v allocations", statement, n)
		}
		if n := testing.AllocsPerRun(100, func() { _ = Strip("up # note", SyntaxPromQL) }); n != 0 {
			t.Errorf("Strip without a directive: %v allocations", n)
		}
	}
}

func BenchmarkParse(b *testing.B) {
	const statement = "SELECT time_bucket('1 minute', ts) AS t, avg(v) FROM metrics " +
		"WHERE ts >= '2026-09-28T00:00:00Z' AND ts < '2026-09-28T06:00:00Z' GROUP BY t ORDER BY t"
	for _, test := range []struct{ name, statement string }{
		{"none", statement},
		{"directives", statement + " /* trickster-step-align:partial_end trickster-volatile-window:2m */"},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = Parse(test.statement, SyntaxSQL)
			}
		})
	}
}
