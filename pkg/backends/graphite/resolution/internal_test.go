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

package resolution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
)

func TestParseRawHeader(t *testing.T) {
	r, ok, err := parseRawHeader([]byte("a.b,100,160,10|1,2,3,None,5,6\nc.d,100,160,10|1\n"))
	if err != nil || !ok || r.target != "a.b" || r.start.Unix() != 100 || r.end.Unix() != 160 || r.step != 10*time.Second {
		t.Errorf("got %+v %t %v", r, ok, err)
	}
	if _, ok, err := parseRawHeader([]byte("  \n")); ok || err != nil {
		t.Error("empty body must be !ok without error")
	}
	// a target containing commas is handled by parsing from the right
	r, ok, _ = parseRawHeader([]byte("sumSeries(a.b, a.c),100,160,10|1"))
	if !ok || r.target != "sumSeries(a.b, a.c)" {
		t.Errorf("comma target: %+v", r)
	}
	for _, bad := range []string{"nopipe", "a|1", "a,b|1", "a,1,2,x|1", "a,1,x,3|1", "a,x,2,3|1", "a,1,2,0|1", "a,1,2,-5|1"} {
		if _, _, err := parseRawHeader([]byte(bad)); !errors.Is(err, errBadRaw) {
			t.Errorf("%q: expected errBadRaw, got %v", bad, err)
		}
	}
}

func TestWhisperDurationAndDigits(t *testing.T) {
	for in, want := range map[string]time.Duration{"10": 10 * time.Second, "10s": 10 * time.Second,
		"1m": time.Minute, "1min": time.Minute, "1MINUTE": time.Minute, "2h": 2 * time.Hour,
		"1d": 24 * time.Hour, "1w": 7 * 24 * time.Hour, "1y": 365 * 24 * time.Hour} {
		if got, err := whisperDuration(in); err != nil || got != want {
			t.Errorf("%s: got %v %v want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "s", "10x", "10mon", "99999999999999999999", "999999999999y"} {
		if _, err := whisperDuration(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
	if isAllDigits("") || isAllDigits("1a") || !isAllDigits("123") {
		t.Error("isAllDigits")
	}
}

func TestOriginGetErrors(t *testing.T) {
	var o *Origin
	if _, err := o.Get(context.Background(), "/x", nil); err == nil {
		t.Error("nil origin must error")
	}
	if _, err := (&Origin{}).Get(context.Background(), "/x", nil); err == nil {
		t.Error("unconfigured origin must error")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(200 * time.Millisecond)
		}
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(strings.Repeat("x", 500)))
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL + "/prefix/")
	o = &Origin{Base: base, Client: srv.Client(), Headers: http.Header{"X-Test": {"1"}}}
	_, err := o.Get(context.Background(), "/x", url.Values{"a": {"b"}})
	var es *errStatus
	if !errors.As(err, &es) || es.code != 502 || len(es.body) != 200 || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("expected a truncated 502 error, got %v", err)
	}
	o.Timeout = 50 * time.Millisecond
	if _, err := o.Get(context.Background(), "/slow", nil); err == nil {
		t.Error("expected a timeout")
	}
	bad, _ := url.Parse("http://[::1]:namedport")
	if _, err := (&Origin{Base: bad, Client: srv.Client()}).Get(context.Background(), "/x", nil); err == nil {
		t.Error("expected a request construction error")
	}
}

func TestExpanderBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	reg := NewRegistry(RegistryOptions{TTL: time.Hour, NegativeTTL: time.Second}, nil)
	e := &Expander{Origin: &Origin{Base: base, Client: srv.Client()}, Registry: reg, Observer: NopObserver{}, TTL: time.Minute}
	if _, _, err := e.Expand(context.Background(), "a.*"); err == nil {
		t.Error("expected a JSON error")
	}
	if e.Exists(context.Background(), "a.b") != ExistsUnknown {
		t.Error("expected ExistsUnknown")
	}
	// no observer is fine
	e.Observer = nil
	if _, _, err := e.Expand(context.Background(), "a.*"); err == nil {
		t.Error("expected a JSON error")
	}
}

func TestRegistryReadThroughEdgeCases(t *testing.T) {
	store := newFakeStore()
	r, _ := newTestRegistry(store)
	// a persisted partial-state ladder reads through as-is
	store.data["b1.graphite.resolution.ladder.p"] = []byte(
		`{"ladder":{"observations":[{"age":3600000000000,"step":10000000000}],"state":1},"expires":"2100-01-01T00:00:00Z","gen":0}`)
	l, ok := r.Ladder("p")
	if !ok || l.State != StatePartial || len(l.Observations) != 1 {
		t.Errorf("partial read-through: %v %t", l, ok)
	}
	// a persisted leaf with Unknown confidence is ignored
	store.data["b1.graphite.resolution.leaf.u"] = []byte(`{"key":"k","confidence":0,"expires":"2100-01-01T00:00:00Z","gen":0}`)
	if _, _, ok := r.Leaf("u"); ok {
		t.Error("unknown-confidence persisted leaf must be ignored")
	}
	// expired persisted entries are ignored
	store.data["b1.graphite.resolution.leaf.e"] = []byte(`{"key":"k","confidence":1,"expires":"2000-01-01T00:00:00Z","gen":0}`)
	if _, _, ok := r.Leaf("e"); ok {
		t.Error("expired persisted leaf must be ignored")
	}
	store.data["b1.graphite.resolution.ladder.e"] = []byte(`{"ladder":{"rungs":[{"max_age":60000000000,"step":10000000000}],"state":2},"expires":"2000-01-01T00:00:00Z","gen":0}`)
	if _, ok := r.Ladder("e"); ok {
		t.Error("expired persisted ladder must be ignored")
	}
	// a bad generation value in the store is ignored
	store.data["b1.graphite.resolution.gen"] = []byte("x")
	if r2, _ := newTestRegistry(store); r2.Generation() != 0 {
		t.Error("unparsable generation must be ignored")
	}
}

func TestRegistryEvictionExpiredLayers(t *testing.T) {
	r, c := newTestRegistry(nil)
	l, _ := ParseRetentions("10s:6h")
	_, _ = r.SetLadder("x", l)
	r.SetTarget("t.*", []string{"a"}, time.Minute)
	r.SetNegative("n")
	c.advance(2 * time.Hour)
	// inserting beyond capacity evicts the expired entries of each layer
	for i := range 5 {
		ll, _ := ParseRetentions(time.Duration(20+i*10).String() + "s:6h")
		_, _ = r.SetLadder("x", ll)
		r.SetTarget("t"+time.Duration(i).String()+".*", []string{"a"}, time.Hour)
		r.SetNegative("n" + time.Duration(i).String())
	}
	if _, ok := r.Ladder(l.Fingerprint()); ok {
		t.Error("expired ladder must be evicted")
	}
	if _, _, ok := r.Target("t.*"); ok {
		t.Error("expired target must be evicted")
	}
	if _, ok := r.Negative("n"); ok {
		t.Error("expired negative must be evicted")
	}
}

func TestLearnerDefaults(t *testing.T) {
	l := &Learner{}
	if l.now().IsZero() {
		t.Error("default clock")
	}
	if orInconsistent(nil, true) != ErrInconsistent || orInconsistent(errBadRaw, false) != errBadRaw {
		t.Error("orInconsistent")
	}
	// StepFor on an unknown-state ladder
	if _, ok := (&Ladder{}).StepFor(time.Hour); ok {
		t.Error("unknown ladder must not answer")
	}
}

func TestTracersAndNopObserver(t *testing.T) {
	// both helpers are reached from probe paths that run whether or not a
	// request is in flight, so both must be safe on a nil receiver
	var nilTracers *Tracers
	nilTracers.Set(&tracing.Tracer{Name: "ignored"})
	if nilTracers.Get() != nil {
		t.Error("a nil Tracers must stay nil")
	}
	tr := &Tracers{}
	if tr.Get() != nil {
		t.Error("nothing published yet")
	}
	tr.Set(nil)
	if tr.Get() != nil {
		t.Error("a nil tracer must not be published")
	}
	first := &tracing.Tracer{Name: "first"}
	tr.Set(first)
	tr.Set(&tracing.Tracer{Name: "second"})
	if tr.Get() != first {
		t.Error("the first tracer published wins")
	}

	// the no-op observer exists so that nothing has to nil-check an
	// Observer; every method must be callable and do nothing
	var o Observer = NopObserver{}
	o.Lookup(Exact.String(), SourceRegistry)
	o.Probe(KindNarrow, "step")
	o.Ladders(1)
	o.RegistryEntries(LayerLeaf, 1)
	o.Fallback("unknown_step")
	o.Misprediction()
}

func TestExpandBounds(t *testing.T) {
	now := time.Unix(1_787_350_000, 0)
	newExp := func(handler http.HandlerFunc, maxLeaves, maxBytes int) (*Expander, *Registry, *httptest.Server) {
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		base, _ := url.Parse(srv.URL)
		reg := NewRegistry(RegistryOptions{TTL: time.Hour, NegativeTTL: time.Minute,
			MaxEntries: 100, Now: func() time.Time { return now }}, nil)
		return &Expander{
			Origin:   &Origin{Base: base, Client: http.DefaultClient, Timeout: 5 * time.Second},
			Registry: reg, TTL: time.Minute, MaxLeaves: maxLeaves, MaxLeafBytes: maxBytes,
		}, reg, srv
	}
	leafBody := func(n int) string {
		names := make([]string, n)
		for i := range names {
			names[i] = fmt.Sprintf("a.leaf%05d", i)
		}
		b, _ := json.Marshal(map[string][]string{"results": names})
		return string(b)
	}

	t.Run("over leaf count", func(t *testing.T) {
		var calls atomic.Int64
		e, _, _ := newExp(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			fmt.Fprint(w, leafBody(11))
		}, 10, 0)
		if _, _, err := e.Expand(context.Background(), "a.*"); !errors.Is(err, ErrExpansionTooLarge) {
			t.Fatalf("expected ErrExpansionTooLarge, got %v", err)
		}
		// negative-cached: the repeat does not re-expand
		if _, _, err := e.Expand(context.Background(), "a.*"); !errors.Is(err, ErrExpansionTooLarge) {
			t.Fatalf("expected the cached refusal, got %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("over-limit expansion re-contacted the origin: %d calls", calls.Load())
		}
	})

	t.Run("over byte count", func(t *testing.T) {
		e, _, _ := newExp(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, leafBody(100))
		}, 0, 64)
		if _, _, err := e.Expand(context.Background(), "b.*"); !errors.Is(err, ErrExpansionTooLarge) {
			t.Fatalf("expected ErrExpansionTooLarge, got %v", err)
		}
	})

	t.Run("concurrent identical misses coalesce", func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int64
		e, _, _ := newExp(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			<-release
			fmt.Fprint(w, leafBody(3))
		}, 0, 0)
		var wg sync.WaitGroup
		ids := make([]string, 8)
		for g := range 8 {
			wg.Go(func() {
				_, id, err := e.Expand(context.Background(), "c.*")
				if err != nil {
					t.Error(err)
					return
				}
				ids[g] = id
			})
		}
		time.Sleep(50 * time.Millisecond) // let every goroutine reach the expander
		close(release)
		wg.Wait()
		if calls.Load() != 1 {
			t.Fatalf("concurrent misses did not coalesce: %d origin calls", calls.Load())
		}
		for g, id := range ids {
			if id == "" || id != ids[0] {
				t.Fatalf("goroutine %d got a different expansion id", g)
			}
		}
	})

	t.Run("read cap boundary", func(t *testing.T) {
		// a body of exactly the cap is complete and must parse; one byte
		// more is truncation that must be rejected, not parsed as a prefix
		doc := `{"results":["a.b"]}`
		exact := doc + strings.Repeat(" ", 4<<20-len(doc))
		e, _, _ := newExp(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, exact)
		}, 0, 0)
		if leaves, _, err := e.Expand(context.Background(), "d.*"); err != nil || len(leaves) != 1 {
			t.Fatalf("an exactly-at-cap body is complete and must parse: %v %v", leaves, err)
		}
		e2, _, _ := newExp(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, exact+`{"results":["a.c"]}`)
		}, 0, 0)
		if _, _, err := e2.Expand(context.Background(), "e.*"); err == nil {
			t.Fatal("an over-cap body must be rejected as truncation, not parsed as a prefix")
		}
	})
}

func TestExpandCoalescingLifecycle(t *testing.T) {
	now := time.Unix(1_787_350_000, 0)
	newStallExp := func(release chan struct{}, calls *atomic.Int64, canceled *atomic.Int64) *Expander {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			select {
			case <-release:
				fmt.Fprint(w, `{"results":["a.b"]}`)
			case <-r.Context().Done():
				canceled.Add(1)
			}
		}))
		t.Cleanup(srv.Close)
		base, _ := url.Parse(srv.URL)
		reg := NewRegistry(RegistryOptions{TTL: time.Hour, NegativeTTL: time.Minute,
			MaxEntries: 100, Now: func() time.Time { return now }}, nil)
		return &Expander{
			Origin:   &Origin{Base: base, Client: http.DefaultClient, Timeout: 10 * time.Second},
			Registry: reg, TTL: time.Minute,
		}
	}

	t.Run("canceled waiter returns promptly", func(t *testing.T) {
		release := make(chan struct{})
		var calls, canceled atomic.Int64
		e := newStallExp(release, &calls, &canceled)
		go e.Expand(context.Background(), "a.*")
		for calls.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		wctx, wcancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, _, err := e.Expand(wctx, "a.*")
			done <- err
		}()
		time.Sleep(20 * time.Millisecond)
		wcancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a canceled waiter stayed parked on the shared operation")
		}
		close(release)
	})

	t.Run("canceled leader does not fail a live waiter", func(t *testing.T) {
		release := make(chan struct{})
		var calls, canceled atomic.Int64
		e := newStallExp(release, &calls, &canceled)
		lctx, lcancel := context.WithCancel(context.Background())
		leaderDone := make(chan error, 1)
		go func() {
			_, _, err := e.Expand(lctx, "b.*")
			leaderDone <- err
		}()
		for calls.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		waiterDone := make(chan error, 1)
		go func() {
			_, _, err := e.Expand(context.Background(), "b.*")
			waiterDone <- err
		}()
		time.Sleep(20 * time.Millisecond)
		lcancel()
		if err := <-leaderDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("leader: expected context.Canceled, got %v", err)
		}
		close(release)
		if err := <-waiterDone; err != nil {
			t.Fatalf("waiter must still receive the result: %v", err)
		}
	})

	t.Run("all callers gone cancels the origin call", func(t *testing.T) {
		release := make(chan struct{})
		var calls, canceled atomic.Int64
		e := newStallExp(release, &calls, &canceled)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			e.Expand(ctx, "c.*")
			close(done)
		}()
		for calls.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
		<-done
		deadline := time.Now().Add(2 * time.Second)
		for canceled.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if canceled.Load() == 0 {
			t.Fatal("the shared origin call outlived its last caller")
		}
	})
}

func TestExpandLateArrivalAfterAbandonment(t *testing.T) {
	now := time.Unix(1_787_350_000, 0)
	release := make(chan struct{})
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			// the first (soon-abandoned) call stalls until its shared
			// context is canceled
			<-r.Context().Done()
			return
		}
		select {
		case <-release:
			fmt.Fprint(w, `{"results":["a.b"]}`)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	reg := NewRegistry(RegistryOptions{TTL: time.Hour, NegativeTTL: time.Minute,
		MaxEntries: 100, Now: func() time.Time { return now }}, nil)
	e := &Expander{
		Origin:   &Origin{Base: base, Client: http.DefaultClient, Timeout: 10 * time.Second},
		Registry: reg, TTL: time.Minute,
	}

	// the leader starts and then abandons the shared operation
	lctx, lcancel := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := e.Expand(lctx, "a.*")
		leaderDone <- err
	}()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	lcancel()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader: expected context.Canceled, got %v", err)
	}

	// a live caller arriving now — including inside the window before the
	// abandoned worker exits — must get a fresh live operation
	close(release)
	if _, _, err := e.Expand(context.Background(), "a.*"); err != nil {
		t.Fatalf("a live caller joined a canceled operation: %v", err)
	}

	// hammer the zero-waiter/worker-exit window: canceled and live callers
	// interleave, and a live caller must never see a spurious cancellation
	for i := range 50 {
		expr := fmt.Sprintf("hammer%d.*", i%5)
		cctx, ccancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() {
			e.Expand(cctx, expr)
		})
		ccancel()
		wg.Go(func() {
			if _, _, err := e.Expand(context.Background(), expr); err != nil &&
				errors.Is(err, context.Canceled) {
				t.Errorf("live caller received a spurious cancellation: %v", err)
			}
		})
		wg.Wait()
	}
}
