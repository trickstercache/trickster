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
package pick

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/hrw"
	"github.com/trickstercache/trickster/v2/pkg/lb/lt"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

func newLT(t *testing.T, member http.Handler) (*handler, *pool.Target) {
	t.Helper()
	p, targets, _ := albpool.NewHealthy([]http.Handler{member})
	t.Cleanup(p.Stop)
	h := New(names.MechanismLT, lt.New(lt.Options{}),
		Options{GoodCodes: options.DefaultLTStatusCodes().Compile()}).(*handler)
	h.SetPool(p)
	return h, targets[0]
}

func get(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	if r == nil {
		r = httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	}
	h.ServeHTTP(w, r)
	return w
}

// the latency sample is the time to the member's first write, not to its last
func TestTimedDispatchSamplesFirstWrite(t *testing.T) {
	h, tgt := newLT(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
		time.Sleep(120 * time.Millisecond)
		_, _ = io.WriteString(w, "body")
	}))
	w := get(h, nil)
	if w.Code != http.StatusAccepted || w.Body.String() != "body" {
		t.Fatalf("response = %d %q", w.Code, w.Body.String())
	}
	st := tgt.Member().Stats()
	if got := st.Latency(); got < 30*time.Millisecond || got > 110*time.Millisecond {
		t.Errorf("sample = %v, want the ~30ms to the first write, not the ~150ms to the last", got)
	}
	if st.Inflight() != 0 || st.Failures() != 0 {
		t.Errorf("after the request: %d in flight, %d failures", st.Inflight(), st.Failures())
	}
}

func TestTimedDispatchJudgesTheAnswer(t *testing.T) {
	code := http.StatusOK
	h, tgt := newLT(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
	st := tgt.Member().Stats()
	// a gateway failure answered in microseconds is a penalty, not a fast sample
	for i, bad := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		code = bad
		get(h, nil)
		if st.Failures() != int32(i+1) || st.Latency() < lb.DefaultLatencyPenalty {
			t.Fatalf("after a %d: %d failures, latency %v", bad, st.Failures(), st.Latency())
		}
	}
	// other codes, client and server errors included, are the member answering
	for _, good := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
		code = good
		get(h, nil)
		if st.Failures() != 0 {
			t.Fatalf("a %d was counted as a failure", good)
		}
	}
	// a body with no explicit header is a 200
	body, _ := newLT(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	if w := get(body, nil); w.Code != http.StatusOK {
		t.Errorf("implicit status = %d", w.Code)
	}
}

// a client that went away says nothing about the member, whatever was half-written
func TestTimedDispatchIgnoresCanceledRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h, tgt := newLT(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.WriteHeader(http.StatusBadGateway)
	}))
	get(h, httptest.NewRequest(http.MethodGet, "http://example.com/", nil).WithContext(ctx))
	if st := tgt.Member().Stats(); st.Failures() != 0 || st.Latency() >= lb.DefaultLatencyPenalty || st.Inflight() != 0 {
		t.Errorf("a canceled request moved the stats: %d failures, %v, %d in flight",
			st.Failures(), st.Latency(), st.Inflight())
	}
}

func TestTimedDispatchSurvivesAPanic(t *testing.T) {
	h, tgt := newLT(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("member blew up") }))
	func() {
		defer func() { _ = recover() }()
		get(h, nil)
	}()
	if st := tgt.Member().Stats(); st.Inflight() != 0 || st.Failures() != 1 {
		t.Errorf("after a panic: %d in flight, %d failures", st.Inflight(), st.Failures())
	}
	// with no good codes configured, every answer is a good one
	p, targets, _ := albpool.NewHealthy([]http.Handler{http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})})
	defer p.Stop()
	lenient := New(names.MechanismLT, lt.New(lt.Options{}))
	lenient.SetPool(p)
	get(lenient, nil)
	if targets[0].Member().Stats().Failures() != 0 {
		t.Error("a response was judged without any good codes to judge it by")
	}
}

// the wrapper must not hide what the real writer can do
func TestFirstWriteWriterPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	fw := getWriter(rec, lb.Pick{})
	defer putWriter(fw)
	if fw.Unwrap() != rec {
		t.Error("Unwrap does not return the wrapped writer")
	}
	n, err := fw.ReadFrom(strings.NewReader("streamed"))
	if err != nil || n != 8 || rec.Body.String() != "streamed" || !fw.wrote || fw.code != http.StatusOK {
		t.Errorf("ReadFrom: %d %v %q", n, err, rec.Body.String())
	}
	fw.Flush()
	if !rec.Flushed {
		t.Error("Flush did not reach the wrapped writer")
	}
	if err := http.NewResponseController(fw).Flush(); err != nil {
		t.Errorf("the response controller cannot see through the wrapper: %v", err)
	}
	// an informational response is not yet the member's answer
	early := getWriter(httptest.NewRecorder(), lb.Pick{})
	defer putWriter(early)
	early.WriteHeader(http.StatusEarlyHints)
	if early.wrote {
		t.Error("an informational response was taken for the first write")
	}
	early.WriteHeader(http.StatusSwitchingProtocols)
	if !early.wrote || early.code != http.StatusSwitchingProtocols {
		t.Error("a protocol switch was not taken for the answer")
	}
	// a writer with no ReadFrom or Flush of its own still works
	var plain plainWriter
	pw := getWriter(&plain, lb.Pick{})
	defer putWriter(pw)
	if n, err := pw.ReadFrom(strings.NewReader("copied")); err != nil || n != 6 || plain.body.String() != "copied" {
		t.Errorf("ReadFrom over a plain writer: %d %v %q", n, err, plain.body.String())
	}
	pw.Flush()
	pw.WriteHeader(http.StatusTeapot)
	if pw.code != http.StatusOK {
		t.Error("a header after the first write replaced the recorded status")
	}
}

// readerFromWriter has the copy fast path a real connection's writer has
type readerFromWriter struct {
	plainWriter
	fast bool
}

func (w *readerFromWriter) ReadFrom(r io.Reader) (int64, error) {
	w.fast = true
	return io.Copy(&w.body, r)
}

func TestFirstWriteWriterKeepsTheCopyFastPath(t *testing.T) {
	var under readerFromWriter
	fw := getWriter(&under, lb.Pick{})
	defer putWriter(fw)
	// a limited reader has no WriteTo, so the copy turns to the writer's ReadFrom
	if n, err := io.Copy(fw, io.LimitReader(strings.NewReader("sendfile"), 8)); err != nil || n != 8 || !under.fast {
		t.Errorf("copy: %d %v, fast path taken: %v", n, err, under.fast)
	}
}

type plainWriter struct{ body strings.Builder }

func (*plainWriter) Header() http.Header { return http.Header{} }

func (w *plainWriter) Write(b []byte) (int, error) { return w.body.Write(b) }

func (*plainWriter) WriteHeader(int) {}

// requests with one key reach one member; the mechanism reads the key it was configured for
func TestKeyedDispatchIsSticky(t *testing.T) {
	hs := make([]http.Handler, 5)
	for i := range hs {
		hs[i] = albpool.NamedHandler(string(rune('a' + i)))
	}
	p, _, _ := albpool.NewHealthy(hs)
	defer p.Stop()
	ks, err := options.ParseKeySource("header:X-Tenant")
	if err != nil {
		t.Fatal(err)
	}
	h := New(names.MechanismHRW, hrw.New(), Options{Key: ks, IPv6Prefix: 64})
	h.SetPool(p)
	owners := make(map[string]string)
	reached := make(map[string]bool)
	for round := range 3 {
		for _, tenant := range []string{"acme", "globex", "initech", "umbrella", "hooli", "stark", "wayne", "wonka"} {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			r.Header.Set("X-Tenant", tenant)
			got := get(h, r).Body.String()
			if round > 0 && owners[tenant] != got {
				t.Fatalf("%s moved from %s to %s", tenant, owners[tenant], got)
			}
			owners[tenant] = got
			reached[got] = true
		}
	}
	if len(reached) < 3 {
		t.Errorf("8 tenants reached only %d of 5 members", len(reached))
	}
	// a request with no key is still served
	if w := get(h, nil); w.Code != http.StatusOK {
		t.Errorf("keyless request = %d", w.Code)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, nil)
	if w.Code != http.StatusOK {
		t.Errorf("nil request = %d", w.Code)
	}
}
