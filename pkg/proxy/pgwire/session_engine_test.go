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

package pgwire

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/jackc/pgx/v5/pgconn"
)

type sessionTestEngine struct {
	testEngine
	probe     SessionDefaultsProbe
	semantics TimeSemantics
	settings  SessionSettings
}

func (e sessionTestEngine) SessionDefaultsProbe() SessionDefaultsProbe { return e.probe }
func (e sessionTestEngine) TimeSemantics() TimeSemantics               { return e.semantics }
func (e sessionTestEngine) SessionSettings() SessionSettings           { return e.settings }

func TestEngineSessionProbe(t *testing.T) {
	for name, probe := range map[string]SessionDefaultsProbe{
		"none":   {},
		"custom": {SQL: "SELECT 1", Names: []string{"sample"}},
	} {
		t.Run(name, func(t *testing.T) {
			upstream := newFakeUpstream(t, cleartextUpstream)
			config := terminatedConfig(upstream, testClientPass)
			config.Engine = sessionTestEngine{probe: probe}
			conn, defaults, err := config.loginUpstream(t.Context(), "", nil, true)
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Conn.Close()
			if slices.Contains(upstream.received(), unannouncedSettingsSQL) {
				t.Fatal("used PostgreSQL's probe for an engine override")
			}
			if probe.SQL == "" {
				if len(defaults) != 0 || len(upstream.received()) != 0 {
					t.Fatal("an empty probe must not query or invent defaults")
				}
			} else if defaults["sample"] != "1" || !slices.Contains(upstream.received(), probe.SQL) {
				t.Fatalf("custom probe not applied: %v", defaults)
			}
		})
	}
	for _, query := range []string{fakeQueryError, fakeQueryMany, fakeQueryPing} {
		t.Run(query, func(t *testing.T) {
			upstream := newFakeUpstream(t, cleartextUpstream)
			config := terminatedConfig(upstream, testClientPass)
			config.Engine = sessionTestEngine{probe: SessionDefaultsProbe{SQL: query, Names: []string{"sample"}}}
			ctx, cancel := context.WithTimeout(t.Context(), fakeTimeout)
			defer cancel()
			conn, _, err := config.loginUpstream(ctx, "", nil, true)
			if err == nil {
				_ = conn.Conn.Close()
				t.Fatal("a failed or malformed probe must fail the login")
			}
		})
	}
}

func TestSettingsProbeResults(t *testing.T) {
	row := func(values ...string) *pgconn.Result {
		r := &pgconn.Result{Rows: [][][]byte{make([][]byte, len(values))}}
		for i, v := range values {
			r.Rows[0][i] = []byte(v)
		}
		return r
	}
	for name, results := range map[string][]*pgconn.Result{
		"one select": {row("UTC", "ISO")}, "two show statements": {row("UTC"), row("ISO")},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := settingsFromResults(results, []string{"timezone", "datestyle"})
			if err != nil || !reflect.DeepEqual(got, map[string]string{"timezone": "UTC", "datestyle": "ISO"}) {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
	for name, results := range map[string][]*pgconn.Result{
		"empty": {}, "nil result": {nil}, "no rows": {{}}, "empty row": {row()},
		"too few": {row("UTC")}, "too many": {row("UTC", "ISO", "extra")},
		"null":      {{Rows: [][][]byte{{nil, []byte("ISO")}}}},
		"extra row": {{Rows: [][][]byte{{[]byte("UTC"), []byte("ISO")}, {[]byte("UTC"), []byte("ISO")}}}},
		"error":     {{Err: errors.New("failed")}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := settingsFromResults(results, []string{"timezone", "datestyle"}); err == nil {
				t.Fatal("accepted a malformed settings result")
			}
		})
	}
	for _, names := range [][]string{{}, {"a", "a"}, {"a", ""}} {
		if _, err := settingsFromResults([]*pgconn.Result{row("UTC", "ISO")}, names); err == nil {
			t.Fatalf("accepted names %v", names)
		}
	}
}

func trackerEngine() sessionTestEngine {
	return sessionTestEngine{
		semantics: TimeSemantics{AssumedTimeZone: "UTC", AssumedDateStyle: "ISO", AssumedStandardConformingStrings: "on"},
		settings: SessionSettings{
			Tracked: map[string]struct{}{varTimeZone: {}, "datestyle": {}, varStandardConformingStrings: {}},
			Neutral: map[string]struct{}{paramApplicationName: {}},
			Aliases: map[string]string{"time_zone": varTimeZone}, LocalPersists: true,
		},
	}
}

func TestAssumedSessionSettings(t *testing.T) {
	engine := trackerEngine()
	assumed := newSessionTracker("user", "db", nil, engine)
	announced := newSessionTracker("user", "db", nil)
	announced.parameterStatus("TimeZone", "UTC")
	announced.parameterStatus("DateStyle", "ISO")
	announced.parameterStatus(varStandardConformingStrings, "on")
	if !assumed.utc() || assumed.lexicalOptions() || assumed.sessionIdentity() != announced.sessionIdentity() {
		t.Fatal("assumptions must have the same identity as equal announcements")
	}
	initial := assumed.sessionIdentity()
	assumed.parameterStatus("TimeZone", "Asia/Kolkata")
	if assumed.utc() || assumed.sessionIdentity() == initial {
		t.Fatal("real announcements must override assumptions")
	}
	assumed.sessionDefaults(map[string]string{"datestyle": "SQL", varStandardConformingStrings: "off"})
	if style, _ := assumed.setting("datestyle"); style != "SQL" || !assumed.lexicalOptions() {
		t.Fatal("probed values must override assumptions")
	}
	startup := newSessionTracker("user", "db", map[string]string{"time_zone": "Asia/Kolkata"}, engine)
	if startup.utc() {
		t.Fatal("startup alias must override an assumed zone")
	}
	if ok, _ := startup.cacheable(); !ok {
		t.Fatal("a known startup setting disabled the cache")
	}
	startup.sessionDefaults(map[string]string{varTimeZone: "UTC"})
	if !startup.utc() {
		t.Fatal("the effective probe must override a requested startup value")
	}
	engine.semantics.AssumedTimeZone = ""
	engine.settings.UnconfirmedStartup = true
	unconfirmed := newSessionTracker("user", "db", map[string]string{"TimeZone": "UTC"}, engine)
	if unconfirmed.utc() {
		t.Fatal("an unconfirmed startup parameter established the effective timezone")
	}
	other := newSessionTracker("user", "db", map[string]string{"TimeZone": "Asia/Kolkata"}, engine)
	if other.sessionIdentity() == unconfirmed.sessionIdentity() {
		t.Fatal("unconfirmed startup parameters must still partition the cache")
	}
	unconfirmed.parameterStatus("TimeZone", "UTC")
	if !unconfirmed.utc() {
		t.Fatal("a real announcement must confirm the startup timezone")
	}
}

func TestEngineClientSettings(t *testing.T) {
	for _, sql := range []string{"SET time_zone = 'Asia/Kolkata'", "SET LOCAL time_zone = 'Asia/Kolkata'", "SET TIME ZONE 'Asia/Kolkata'"} {
		t.Run(sql, func(t *testing.T) {
			tr := newSessionTracker("user", "db", nil, trackerEngine())
			// Some origins announce only the initial value and never updates.
			tr.parameterStatus("TimeZone", "UTC")
			tr.sessionDefaults(map[string]string{varTimeZone: "UTC"})
			initial := tr.sessionIdentity()
			class := classify(sql, false)
			tr.observe(&class, true, true, false)
			if ok, _ := tr.cacheable(); ok {
				t.Fatal("pending SET can be cached")
			}
			tr.ready(true)
			if !tr.utc() || tr.sessionIdentity() != initial {
				t.Fatal("failed SET changed the session")
			}
			tr.observe(&class, true, true, false)
			tr.ready(false)
			if tr.utc() || tr.sessionIdentity() == initial {
				t.Fatal("successful SET was lost")
			}
			if ok, reason := tr.cacheable(); !ok {
				t.Fatalf("successful modeled SET disabled cache: %s", reason)
			}
			reset := classify("RESET time_zone", false)
			tr.observe(&reset, true, true, false)
			tr.ready(false)
			if !tr.utc() {
				t.Fatal("RESET lost the initial probed value")
			}
		})
	}
	tr := newSessionTracker("user", "db", nil, trackerEngine())
	class := classify("SET time_zone = 'GMT'", false)
	tr.observe(&class, true, true, false)
	tr.parameterStatus("TimeZone", "UTC")
	tr.ready(false)
	if zone, _ := tr.setting(varTimeZone); zone != "UTC" {
		t.Fatal("client text overrode a real announcement")
	}
	for _, flags := range [][3]bool{{false, true, false}, {true, false, false}, {true, true, true}} {
		tr := newSessionTracker("user", "db", nil, trackerEngine())
		tr.observe(&class, flags[0], flags[1], flags[2])
		if ok, _ := tr.cacheable(); ok {
			t.Fatalf("uncertain SET context %v was cacheable", flags)
		}
	}
	tr = newSessionTracker("user", "db", nil, trackerEngine())
	class = classify("SET standard_conforming_strings = off", false)
	tr.observe(&class, true, true, false)
	tr.ready(false)
	if !tr.lexicalOptions() {
		t.Fatal("client-only string setting did not update the lexer")
	}
	class = classify("RESET standard_conforming_strings", true)
	tr.observe(&class, true, true, false)
	tr.ready(false)
	if tr.lexicalOptions() {
		t.Fatal("RESET did not restore the assumed lexer mode")
	}
}

func TestEngineLosslessFloatText(t *testing.T) {
	unknown := timeAxisSettings(nil)
	if _, err := newTimeAxisDecoder(TimeAxisEpochFloat, timeseries.DateTimeUnixSecs, TimeSemantics{}, unknown); err == nil {
		t.Fatal("an unprobed PostgreSQL float must still fail closed")
	}
	decoder, err := newTimeAxisDecoder(TimeAxisEpochFloat, timeseries.DateTimeUnixSecs, TimeSemantics{LosslessFloatText: true}, unknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.decode([]byte("1700000100.0")); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"1700000100.5", "NaN", "Infinity", "9007199254740994"} {
		if _, err := decoder.decode([]byte(value)); err == nil {
			t.Fatalf("accepted unsafe epoch %s", value)
		}
	}
}
