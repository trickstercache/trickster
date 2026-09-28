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

package pgwire

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	gateDeltaQuery = "SELECT date_bin(INTERVAL '5 minutes', ts, TIMESTAMP '2000-01-01') AS time, count(*) FROM trips " +
		"WHERE ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z' GROUP BY 1 ORDER BY 1"
	gateDeltaQueryLater = "SELECT date_bin(INTERVAL '5 minutes', ts, TIMESTAMP '2000-01-01') AS time, count(*) FROM trips " +
		"WHERE ts >= '2026-09-18T09:00:00Z' AND ts < '2026-09-18T12:00:00Z' GROUP BY 1 ORDER BY 1"
	gateObjectQuery     = "SELECT count(*) FROM trips"
	gateZoneNewYork     = "America/New_York"
	splitterTestMaxHeld = 1024
)

func gatedConfig(t *testing.T, f *fakeUpstream) Config {
	t.Helper()
	c := testConfig(f)
	// a backend name per test keeps its metric series to itself
	c.BackendName, c.Dialect, c.Analyzer = t.Name(), testProvider, testAnalyzer
	return c
}

func analysisCount(backend string, mode sqlanalyzer.CacheMode, reason sqlanalyzer.AnalysisReason) float64 {
	return testutil.ToFloat64(metrics.SQLQueryAnalysis.WithLabelValues(backend, testProvider, mode.String(), string(reason)))
}

func fakeNow() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }

func TestGateClassifiesRelayedStatements(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := gatedConfig(t, upstream)
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	backend := config.BackendName
	steps := []struct {
		sql    string
		mode   sqlanalyzer.CacheMode
		reason sqlanalyzer.AnalysisReason
	}{
		{gateDeltaQuery, sqlanalyzer.CacheModeDelta, sqlanalyzer.ReasonDeltaCacheable},
		{gateObjectQuery, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket},
		{"SELECT 1; SELECT 2", sqlanalyzer.CacheModeNone, reasonMultiStatement},
		{fakeQueryBegin, 0, ""},
		{gateDeltaQuery, sqlanalyzer.CacheModeNone, reasonInTransaction},
		{fakeQueryCommit, 0, ""},
		{fakeQueryPing, 0, ""},
		// a reported setting keeps the session cacheable: the origin announces it
		{fakeQuerySetZone + "'" + gateZoneNewYork + "'", 0, ""},
		{gateDeltaQuery, sqlanalyzer.CacheModeDelta, sqlanalyzer.ReasonDeltaCacheable},
		// a setting nobody models ends caching for the session
		{"SET app.tenant = '42'", 0, ""},
		{gateDeltaQuery, sqlanalyzer.CacheModeNone, reasonSessionState},
	}
	for i, step := range steps {
		before := analysisCount(backend, step.mode, step.reason)
		if _, err := queryRows(t, conn, step.sql); err != nil {
			t.Fatalf("step %d %q: every statement must still be relayed: %v", i, step.sql, err)
		}
		if step.reason == "" {
			continue
		}
		if got := analysisCount(backend, step.mode, step.reason) - before; got != 1 {
			t.Fatalf("step %d %q: expected one %s/%s, got %v", i, step.sql, step.mode, step.reason, got)
		}
	}
}

func TestGateIsOffForRelayOnlyBackends(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := testConfig(upstream)
	config.BackendName = t.Name()
	server, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	if rows, err := queryRows(t, conn, gateDeltaQuery); err != nil || rows == 0 {
		t.Fatalf("%d rows, %v", rows, err)
	}
	if server.analysis != nil ||
		analysisCount(config.BackendName, sqlanalyzer.CacheModeDelta, sqlanalyzer.ReasonDeltaCacheable) != 0 {
		t.Fatal("a backend with no analyzer must not inspect statements")
	}
}

func TestGateSkipsOversizedStatements(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := gatedConfig(t, upstream)
	config.MaxQuerySizeBytes = 2 * len(gateDeltaQuery)
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	if rows, err := queryRows(t, conn, "SELECT '"+strings.Repeat("x", 4*len(gateDeltaQuery))+"'"); err != nil || rows != 1 {
		t.Fatalf("an oversized statement must still be relayed: %d rows, %v", rows, err)
	}
	if analysisCount(config.BackendName, sqlanalyzer.CacheModeNone, reasonQuerySize) != 1 {
		t.Fatal("expected the oversized statement to be counted")
	}
	// the session could not read that statement, so it no longer vouches for its state
	if _, err := queryRows(t, conn, gateDeltaQuery); err != nil {
		t.Fatal(err)
	}
	if analysisCount(config.BackendName, sqlanalyzer.CacheModeNone, reasonSessionState) != 1 {
		t.Fatal("expected later statements to bypass analysis")
	}
}

func TestExtendedProtocolIsObservedNotCached(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := gatedConfig(t, upstream)
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
	defer cancel()
	if result := conn.ExecParams(ctx, gateDeltaQuery, nil, nil, nil, nil).Read(); result.Err != nil {
		t.Fatal(result.Err)
	}
	if analysisCount(config.BackendName, sqlanalyzer.CacheModeDelta, sqlanalyzer.ReasonDeltaCacheable) != 0 {
		t.Fatal("an extended-protocol statement must not be analyzed for caching")
	}
	if _, err := queryRows(t, conn, gateDeltaQuery); err != nil {
		t.Fatal(err)
	}
	if analysisCount(config.BackendName, sqlanalyzer.CacheModeDelta, sqlanalyzer.ReasonDeltaCacheable) != 1 {
		t.Fatal("a harmless extended statement must leave the session cacheable")
	}
	if result := conn.ExecParams(ctx, "SET app.tenant = '42'", nil, nil, nil, nil).Read(); result.Err != nil {
		t.Fatal(result.Err)
	}
	if _, err := queryRows(t, conn, gateDeltaQuery); err != nil {
		t.Fatal(err)
	}
	if analysisCount(config.BackendName, sqlanalyzer.CacheModeNone, reasonSessionState) != 1 {
		t.Fatal("an unmodeled setting sent through Parse must end caching for the session")
	}
}

func gateTestSession(t *testing.T, params map[string]string) *session {
	t.Helper()
	server, err := NewServer(Config{
		BackendName: t.Name(), Dialect: testProvider, CacheKeyPrefix: "prefix",
		Upstream: Upstream{Address: testUnusedAddress}, Analyzer: testAnalyzer, MaxQuerySizeBytes: 1024,
		MaxMessageSizeBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &session{front: server, server: server, user: testClientUser, database: testDatabase}
	s.tracker = newSessionTracker(s.user, s.database, params)
	s.txStatus.Store(txStatusIdle)
	return s
}

func TestCacheKeyIdentity(t *testing.T) {
	base := gateTestSession(t, nil)
	now := fakeNow()
	delta := testAnalyzer.Analyze(gateDeltaQuery, now)
	later := testAnalyzer.Analyze(gateDeltaQueryLater, now)
	object := testAnalyzer.Analyze(gateObjectQuery, now)
	if delta.Mode != sqlanalyzer.CacheModeDelta || object.Mode != sqlanalyzer.CacheModeObject {
		t.Fatalf("unexpected fixtures: %v %v", delta.Mode, object.Mode)
	}
	key := base.cacheKey(delta, gateDeltaQuery)
	if !strings.HasPrefix(key, t.Name()+".prefix.pgwire.dpc.") {
		t.Fatalf("unexpected key layout %q", key)
	}
	if key != base.cacheKey(later, gateDeltaQueryLater) {
		t.Fatal("a delta key must not depend on the requested time range")
	}
	if !strings.Contains(base.cacheKey(object, gateObjectQuery), ".pgwire.opc.") ||
		base.cacheKey(object, gateObjectQuery) == base.cacheKey(object, gateObjectQuery+" ") {
		t.Fatal("an object key is the statement's exact text")
	}
	if key != gateTestSession(t, nil).cacheKey(delta, gateDeltaQuery) {
		t.Fatal("equal sessions must share a key")
	}

	for name, mutate := range map[string]func(*session){
		"user":     func(s *session) { s.tracker.user = "someone_else" },
		"database": func(s *session) { s.tracker.database = "other" },
		"reported": func(s *session) { s.tracker.parameterStatus(fakeParamTimeZone, gateZoneNewYork) },
		"client setting": func(s *session) {
			class := classify("SET extra_float_digits = 0", false)
			s.tracker.observe(&class, true, true, false)
			s.tracker.ready(false)
		},
		"backend": func(s *session) { s.server.config.BackendName = "other" },
		"dialect": func(s *session) { s.server.config.Dialect = "other" },
	} {
		other := gateTestSession(t, nil)
		mutate(other)
		if other.cacheKey(delta, gateDeltaQuery) == key {
			t.Fatalf("%s must partition the cache", name)
		}
	}
	for name, params := range map[string]map[string]string{
		"startup options":   {paramOptions: "-c extra_float_digits=0"},
		"modeled startup":   {"extra_float_digits": "2"},
		"unknown startup":   {"app.tenant": "42"},
		"startup datestyle": {"DateStyle": "German"},
	} {
		if gateTestSession(t, params).cacheKey(delta, gateDeltaQuery) == key {
			t.Fatalf("%s must partition the cache", name)
		}
	}
	for name, params := range map[string]map[string]string{
		"application name":  {paramApplicationName: "grafana"},
		"user and database": {paramUser: testClientUser, paramDatabase: testDatabase},
		"protocol option":   {protocolOptionPrefix + "x": "1"},
	} {
		if gateTestSession(t, params).cacheKey(delta, gateDeltaQuery) != key {
			t.Fatalf("%s must not partition the cache", name)
		}
	}
}

func TestSessionTracker(t *testing.T) {
	observe := func(tr *sessionTracker, sql string, txIdle, settled, extended, failed bool) {
		class := classify(sql, tr.lexicalOptions())
		tr.observe(&class, txIdle, settled, extended)
		tr.ready(failed)
	}
	for name, test := range map[string]struct {
		run    func(*sessionTracker)
		unsafe string
	}{
		"reads and neutral statements": {run: func(tr *sessionTracker) {
			observe(tr, "SELECT 1", true, true, false, false)
			observe(tr, "INSERT INTO t VALUES (1)", true, true, false, false)
			observe(tr, "SET statement_timeout = 5000", true, true, false, false)
			observe(tr, "SET LOCAL app.tenant = 1", false, true, false, false)
			observe(tr, "SET TRANSACTION READ ONLY", false, true, false, false)
		}},
		"modeled client setting": {run: func(tr *sessionTracker) {
			observe(tr, "SET extra_float_digits = 0", true, true, false, false)
			observe(tr, "SET ROLE analyst", true, true, false, false)
			observe(tr, "RESET ROLE", true, true, false, false)
			observe(tr, "DISCARD ALL", true, true, false, false)
		}},
		"reported setting in any context": {run: func(tr *sessionTracker) {
			tr.parameterStatus(fakeParamTimeZone, "UTC")
			observe(tr, "SET TIME ZONE 'x'", false, false, true, false)
		}},
		"unmodeled setting":      {func(tr *sessionTracker) { observe(tr, "SET app.tenant = 1", true, true, false, false) }, unsafeSetting},
		"unmodeled statement":    {func(tr *sessionTracker) { observe(tr, "CREATE TEMP TABLE x (i int)", true, true, false, false) }, unsafeStatement},
		"set_config":             {func(tr *sessionTracker) { observe(tr, "SELECT set_config('a','b',false)", true, true, false, false) }, unsafeStatement},
		"setting in transaction": {func(tr *sessionTracker) { observe(tr, "SET extra_float_digits = 0", false, true, false, false) }, unsafeSetInTx},
		"pipelined setting":      {func(tr *sessionTracker) { observe(tr, "SET extra_float_digits = 0", true, false, false, false) }, unsafePipelinedSet},
		"setting beside others": {func(tr *sessionTracker) {
			observe(tr, "SET extra_float_digits = 0; SELECT 1", true, true, false, false)
		}, unsafePipelinedSet},
		"setting through parse":    {func(tr *sessionTracker) { observe(tr, "SET extra_float_digits = 0", true, true, true, false) }, unsafeExtendedSet},
		"reset all in transaction": {func(tr *sessionTracker) { observe(tr, "RESET ALL", false, true, false, false) }, unsafeSetInTx},
	} {
		tr := newSessionTracker(testClientUser, testDatabase, nil)
		test.run(tr)
		if ok, reason := tr.cacheable(); ok != (test.unsafe == "") || reason != test.unsafe {
			t.Errorf("%s: cacheable = %t (%q), want reason %q", name, ok, reason, test.unsafe)
		}
	}
}

func TestSessionTrackerAppliesOnlySuccessfulChanges(t *testing.T) {
	tr := newSessionTracker(testClientUser, testDatabase, map[string]string{"app.tenant": "42"})
	initial := tr.sessionIdentity()
	class := classify("SET extra_float_digits = 0", false)
	tr.observe(&class, true, true, false)
	if ok, _ := tr.cacheable(); ok {
		t.Fatal("a session with an unconfirmed change must not be cacheable yet")
	}
	tr.ready(true)
	if ok, _ := tr.cacheable(); !ok || tr.sessionIdentity() != initial {
		t.Fatal("a failed SET must change nothing")
	}
	tr.observe(&class, true, true, false)
	tr.ready(false)
	changed := tr.sessionIdentity()
	if changed == initial {
		t.Fatal("a successful SET must change the identity")
	}
	reset := classify("RESET ALL", false)
	tr.observe(&reset, true, true, false)
	tr.ready(false)
	if tr.sessionIdentity() != initial {
		t.Fatal("RESET ALL must return to the startup identity, keeping startup settings")
	}
	tr.disable(unsafeFunctionCall)
	tr.disable(unsafeStatement)
	if _, reason := tr.cacheable(); reason != unsafeFunctionCall {
		t.Fatalf("the first reason must stick, got %q", reason)
	}
	tr.parameterStatus("application_name", "ignored")
	tr.parameterStatus(varStandardConformingStrings, settingOff)
	if !tr.lexicalOptions() {
		t.Fatal("standard_conforming_strings = off must switch on backslash escapes")
	}
}

func TestObserveParseRejectsMalformedBodies(t *testing.T) {
	for _, body := range [][]byte{[]byte("no-terminator"), []byte("name\x00unterminated query")} {
		s := gateTestSession(t, nil)
		s.observeParse(body)
		if ok, reason := s.tracker.cacheable(); ok || reason != unsafeStatement {
			t.Fatalf("%q: expected the session to stop caching, got %t %q", body, ok, reason)
		}
	}
	s := gateTestSession(t, nil)
	s.observeParameterStatus([]byte("no-terminator"))
	s.observeParameterStatus([]byte(fakeParamTimeZone + "\x00" + gateZoneNewYork + "\x00"))
	if s.tracker.reported[varTimeZone] != gateZoneNewYork {
		t.Fatal("expected the announced zone to be recorded")
	}
}

func splitterStream() (stream []byte, queries, parses []string) {
	queries = []string{gateDeltaQuery, "", strings.Repeat("y", 900)}
	parses = []string{"SELECT $1"}
	stream = appendFrame(stream, msgQuery, []byte(queries[0]+"\x00"))
	stream = appendFrame(stream, 'B', bytes.Repeat([]byte{1}, 40))
	stream = appendFrame(stream, msgParse, []byte("stmt\x00"+parses[0]+"\x00\x00\x00"))
	stream = appendFrame(stream, msgSync, nil)
	stream = appendFrame(stream, msgQuery, []byte(queries[1]+"\x00"))
	// larger than the hold limit: relayed without being read
	stream = appendFrame(stream, msgQuery, bytes.Repeat([]byte{'x'}, 2000))
	stream = appendFrame(stream, msgQuery, []byte(queries[2]+"\x00"))
	stream = appendFrame(stream, msgTerminate, nil)
	return stream, queries, parses
}

func TestClientSplitterIsChunkInvariant(t *testing.T) {
	stream, _, _ := splitterStream()
	for chunk := 1; chunk <= len(stream); chunk++ {
		s := gateTestSession(t, nil)
		var forwarded, held bytes.Buffer
		splitter := &clientSplitter{
			s: s, maxBody: 4096, maxHeld: splitterTestMaxHeld,
			forward: func(b []byte) error {
				forwarded.Write(b)
				// a held message is always forwarded whole, in one call; the
				// oversized query can also be flushed alone, so size tells them apart
				if len(b) > frameHeaderLen && len(b) <= splitterTestMaxHeld && (b[0] == msgQuery || b[0] == msgParse) &&
					int(uint32(b[1])<<24|uint32(b[2])<<16|uint32(b[3])<<8|uint32(b[4]))+1 == len(b) {
					held.WriteByte(b[0])
				}
				return nil
			},
		}
		for offset := 0; offset < len(stream); offset += chunk {
			if err := splitter.relay(stream[offset:min(offset+chunk, len(stream))]); err != nil {
				t.Fatalf("chunk %d: %v", chunk, err)
			}
		}
		if !bytes.Equal(forwarded.Bytes(), stream) {
			t.Fatalf("chunk %d: the relayed stream differs from the client's", chunk)
		}
		if !splitter.atBoundary() {
			t.Fatalf("chunk %d: expected to end on a boundary", chunk)
		}
		if got := held.String(); got != "QPQQ" {
			t.Fatalf("chunk %d: held %q, want the three small queries and the parse", chunk, got)
		}
		// Q, Sync, Q, oversized Q, Q each end in one ReadyForQuery
		if got := s.outstanding.Load(); got != 5 {
			t.Fatalf("chunk %d: counted %d requests, want 5", chunk, got)
		}
		if ok, reason := s.tracker.cacheable(); ok || reason != unsafeOversizedText {
			t.Fatalf("chunk %d: the unread statement must end caching, got %t %q", chunk, ok, reason)
		}
	}
}

func TestClientSplitterErrors(t *testing.T) {
	errForward := errors.New("write failed")
	stream, _, _ := splitterStream()
	for name, test := range map[string]struct {
		input   []byte
		failAt  int
		wantErr error
	}{
		"bad length in place":    {input: []byte{msgQuery, 0, 0, 0, 3, 0, 0}, wantErr: errFrameLength},
		"oversized message":      {input: []byte{'d', 0, 0, 0x20, 0}, wantErr: errFrameLength},
		"write fails on span":    {input: stream, failAt: 1, wantErr: errForward},
		"write fails on release": {input: stream[:len(gateDeltaQuery)+6], failAt: 1, wantErr: errForward},
	} {
		calls := 0
		splitter := &clientSplitter{
			s: gateTestSession(t, nil), maxBody: 4096, maxHeld: 1024,
			forward: func([]byte) error {
				calls++
				if test.failAt > 0 && calls >= test.failAt {
					return errForward
				}
				return nil
			},
		}
		if err := splitter.relay(test.input); !errors.Is(err, test.wantErr) {
			t.Fatalf("%s: got %v, want %v", name, err, test.wantErr)
		}
	}
	// the same failures when the header arrives one byte at a time
	for name, input := range map[string][]byte{
		"bad length split":  {msgQuery, 0, 0, 0, 3},
		"write fails split": stream,
	} {
		splitter := &clientSplitter{
			s: gateTestSession(t, nil), maxBody: 4096, maxHeld: 1024,
			forward: func([]byte) error { return errForward },
		}
		var err error
		for i := 0; i < len(input) && err == nil; i++ {
			err = splitter.relay(input[i : i+1])
		}
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	functionCall := &clientSplitter{s: gateTestSession(t, nil), maxBody: 4096, maxHeld: 1024, forward: func([]byte) error { return nil }}
	if err := functionCall.relay(appendFrame(nil, msgFunctionCall, []byte{0, 0, 0, 1})); err != nil {
		t.Fatal(err)
	}
	if _, reason := functionCall.s.tracker.cacheable(); reason != unsafeFunctionCall {
		t.Fatalf("a fast-path function call must end caching, got %q", reason)
	}
	big := &clientSplitter{s: gateTestSession(t, nil), maxBody: 1 << 20, maxHeld: 1 << 20, forward: func([]byte) error { return nil }}
	message := appendFrame(nil, msgQuery, append(bytes.Repeat([]byte{' '}, heldBufferKeepBytes*2), 0))
	if err := big.relay(message[:100]); err != nil {
		t.Fatal(err)
	}
	if err := big.relay(message[100:]); err != nil || big.held != nil {
		t.Fatalf("a large reassembly buffer must be dropped after use: %v, cap %d", err, cap(big.held))
	}
}

func TestFrameScannerCapturesRequestedBodies(t *testing.T) {
	var stream []byte
	stream = appendFrame(stream, msgParameterStatus, []byte("TimeZone\x00UTC\x00"))
	stream = appendFrame(stream, msgDataRow, []byte("ignored"))
	stream = appendFrame(stream, msgParameterStatus, nil)
	stream = appendFrame(stream, msgParameterStatus, bytes.Repeat([]byte{'x'}, maxCapturedBody+1))
	stream = appendFrame(stream, msgParameterStatus, []byte("b\x00c\x00"))
	for chunk := 1; chunk <= 64; chunk++ {
		observer := &recordingObserver{capture: msgParameterStatus}
		scanner := newFrameScanner(observer, pgMaxMessageBody)
		for offset := 0; offset < len(stream); offset += chunk {
			if err := scanner.scan(stream[offset:min(offset+chunk, len(stream))]); err != nil {
				t.Fatal(err)
			}
		}
		if len(observer.bodies) != 2 || observer.bodies[0] != "TimeZone\x00UTC\x00" || observer.bodies[1] != "b\x00c\x00" {
			t.Fatalf("chunk %d: captured %q", chunk, observer.bodies)
		}
	}
}

func TestGateBypassesPipelinedStatements(t *testing.T) {
	s := gateTestSession(t, nil)
	s.outstanding.Store(1)
	if outcome := s.gateQuery([]byte(gateDeltaQuery + "\x00")); outcome.eligible {
		t.Fatal("a statement behind another request must not be eligible")
	}
	if analysisCount(t.Name(), sqlanalyzer.CacheModeNone, reasonPipelined) != 1 {
		t.Fatal("expected the pipelined statement to be counted")
	}
	s.outstanding.Store(0)
	if outcome := s.gateQuery([]byte(gateDeltaQuery + "\x00")); !outcome.eligible ||
		outcome.analysis.Mode != sqlanalyzer.CacheModeDelta || outcome.key != s.cacheKey(outcome.analysis, gateDeltaQuery) {
		t.Fatalf("expected an eligible delta statement, got %+v", outcome)
	}
	s.server.analysis.count(sqlanalyzer.CacheModeNone, "")
	if analysisCount(t.Name(), sqlanalyzer.CacheModeNone, reasonUnknown) != 1 {
		t.Fatal("an empty reason must be labeled unknown")
	}
}

func TestClientSplitterHoldsEmptyBodies(t *testing.T) {
	var forwarded bytes.Buffer
	splitter := &clientSplitter{
		s: gateTestSession(t, nil), maxBody: 4096, maxHeld: splitterTestMaxHeld,
		forward: func(b []byte) error {
			forwarded.Write(b)
			return nil
		},
	}
	// a Query with no body at all is malformed, but it must still relay intact
	message := appendFrame(nil, msgQuery, nil)
	for i := range message {
		if err := splitter.relay(message[i : i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(forwarded.Bytes(), message) || !splitter.atBoundary() {
		t.Fatalf("relayed %v, want %v", forwarded.Bytes(), message)
	}
}

type sessionedAnalyzer struct {
	mtx   sync.Mutex
	views []SessionView
}

func (a *sessionedAnalyzer) Analyze(statement string, now time.Time) sqlanalyzer.Analysis {
	return testAnalyzer.Analyze(statement, now)
}

func (a *sessionedAnalyzer) ForSession(view SessionView) sqlanalyzer.DialectAnalyzer {
	a.mtx.Lock()
	defer a.mtx.Unlock()
	a.views = append(a.views, view)
	return testAnalyzer
}

func TestGateTellsTheAnalyzerAboutTheSession(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := gatedConfig(t, upstream)
	analyzer := &sessionedAnalyzer{}
	config.Analyzer = analyzer
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	for _, sql := range []string{gateDeltaQuery, fakeQuerySetZone + "'" + gateZoneNewYork + "'", gateDeltaQuery} {
		if _, err := queryRows(t, conn, sql); err != nil {
			t.Fatal(err)
		}
	}
	analyzer.mtx.Lock()
	defer analyzer.mtx.Unlock()
	if len(analyzer.views) != 2 || !analyzer.views[0].UTC || analyzer.views[1].UTC {
		t.Fatalf("expected a UTC view and then a local one, got %+v", analyzer.views)
	}
}

func TestGateBypassesBackslashEscapeSessions(t *testing.T) {
	// the analyzer reads string constants by the standard rules only
	s := gateTestSession(t, nil)
	s.tracker.parameterStatus(varStandardConformingStrings, settingOff)
	if outcome := s.gateQuery([]byte(gateDeltaQuery + "\x00")); outcome.eligible ||
		analysisCount(t.Name(), sqlanalyzer.CacheModeNone, reasonSessionState) != 1 {
		t.Fatalf("got %+v", outcome)
	}
}

func TestUnannouncedSettingsComeFromTheSessionsDefaults(t *testing.T) {
	// a role or database default is invisible on the wire, so it is read once at origin login
	s := gateTestSession(t, nil)
	floatAxis := func() error {
		_, err := newTimeAxisDecoder(TimeAxisEpochFloat, timeseries.DateTimeUnixSecs, TimeSemantics{}, s.tracker.setting)
		return err
	}
	apply := func(sql string) {
		class := classify(sql, false)
		s.tracker.observe(&class, true, true, false)
		s.tracker.ready(false)
	}
	if floatAxis() == nil {
		t.Fatal("a float time axis must fail closed while the effective extra_float_digits is unknown")
	}
	unknown := s.tracker.sessionIdentity()
	s.tracker.sessionDefaults(map[string]string{varExtraFloatDigits: "-14", varByteaOutput: "hex"})
	if floatAxis() == nil {
		t.Fatal("a lossy role default must fail closed")
	}
	lossy := s.tracker.sessionIdentity()
	apply("SET extra_float_digits = 1")
	if err := floatAxis(); err != nil {
		t.Fatalf("the client's own setting overrides the default: %v", err)
	}
	apply("RESET extra_float_digits")
	if floatAxis() == nil {
		t.Fatal("RESET returns to the session's default, not to PostgreSQL's")
	}
	s.tracker.sessionDefaults(map[string]string{varExtraFloatDigits: fakeDefaultFloatDigits, varByteaOutput: "hex"})
	if err := floatAxis(); err != nil {
		t.Fatal(err)
	}
	// sessions that render values differently never share cached answers
	if exact := s.tracker.sessionIdentity(); exact == lossy || exact == unknown || lossy == unknown {
		t.Fatal("the session's defaults must be part of its cache identity")
	}
}
