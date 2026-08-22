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
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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
