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

package resolution_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	"github.com/trickstercache/trickster/v2/pkg/testutil/graphite/mockserver"
)

// counter is an Observer that counts events
type counter struct {
	mu      sync.Mutex
	probes  map[string]int
	lookups map[string]int
	ladders int
	layers  map[string]int
}

func newCounter() *counter {
	return &counter{probes: map[string]int{}, lookups: map[string]int{}, layers: map[string]int{}}
}

func (c *counter) Lookup(conf, src string) {
	c.mu.Lock()
	c.lookups[conf+"/"+src]++
	c.mu.Unlock()
}

func (c *counter) Probe(kind, result string) {
	c.mu.Lock()
	c.probes[kind+"/"+result]++
	c.mu.Unlock()
}

func (c *counter) Ladders(n int) { c.mu.Lock(); c.ladders = n; c.mu.Unlock() }
func (c *counter) Fallback(reason string) {
	c.mu.Lock()
	c.lookups["fallback/"+reason]++
	c.mu.Unlock()
}
func (c *counter) Misprediction() { c.mu.Lock(); c.lookups["misprediction"]++; c.mu.Unlock() }
func (c *counter) RegistryEntries(layer string, n int) {
	c.mu.Lock()
	c.layers[layer] = n
	c.mu.Unlock()
}

func (c *counter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, v := range c.probes {
		if !strings.HasPrefix(k, resolution.KindFind) {
			n += v
		}
	}
	return n
}

type harness struct {
	stub     *mockserver.Server
	registry *resolution.Registry
	learner  *resolution.Learner
	resolver *resolution.Resolver
	expander *resolution.Expander
	obs      *counter
	now      time.Time
}

func newHarness(t testing.TB, static [][2]string) *harness {
	t.Helper()
	srv := mockserver.New()
	t.Cleanup(srv.Close)
	now := time.Unix(1_787_350_000, 0)
	srv.Now = func() time.Time { return now }
	base, _ := url.Parse(srv.URL)
	origin := &resolution.Origin{Base: base, Client: http.DefaultClient, Timeout: 5 * time.Second}
	obs := newCounter()
	reg := resolution.NewRegistry(resolution.RegistryOptions{TTL: time.Hour, NegativeTTL: time.Second,
		NegativeTTLMax: 10 * time.Second, MaxEntries: 1000, Now: func() time.Time { return now }}, nil)
	reg.Observer = obs
	exp := &resolution.Expander{Origin: origin, Registry: reg, Observer: obs, TTL: time.Minute}
	st, err := resolution.NewStatic(static)
	if err != nil {
		t.Fatal(err)
	}
	learner := &resolution.Learner{Prober: &resolution.Prober{Origin: origin, Observer: obs},
		Expander: exp, Registry: reg, Observer: obs, Concurrency: 2, Budget: 96, Name: "test",
		Now: func() time.Time { return now }}
	t.Cleanup(learner.Close)
	res := &resolution.Resolver{Registry: reg, Expander: exp, Learner: learner, Static: st,
		Observer: obs}
	return &harness{stub: srv, registry: reg, learner: learner, resolver: res, expander: exp, obs: obs, now: now}
}

var ladders = map[string]string{
	"dev.fast.cpu.host01.percent":            "10s:6h,60s:7d,10m:5y",
	"dev.fast.cpu.host02.percent":            "10s:6h,60s:7d,10m:5y",
	"dev.medium.orders.us-east.count":        "60s:2d,5m:30d,1h:2y",
	"dev.coarse.users.active":                "5m:90d",
	"dev.drift.temperature.sensor01.celsius": "30s:12h,5m:14d,1h:1y",
	"odd.two":                                "15s:1h,60s:1d",
}

func (h *harness) addAll() {
	for p, r := range ladders {
		h.stub.Add(p, r)
	}
}

func TestLearnDiscoversEveryLadder(t *testing.T) {
	h := newHarness(t, nil)
	h.addAll()
	for leaf, retentions := range ladders {
		t.Run(leaf, func(t *testing.T) {
			before := h.obs.total()
			l, err := h.learner.Learn(context.Background(), leaf, nil)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := resolution.ParseRetentions(retentions)
			if l.String() != want.String() {
				t.Errorf("learned %s want %s", l, want)
			}
			probes := h.obs.total() - before
			if probes > 60 {
				t.Errorf("%d probes is more than expected for %s", probes, l)
			}
			t.Logf("%s learned in %d probes", l, probes)
			// the registry now answers every age exactly, with no more probes
			before = h.obs.total()
			for _, age := range []time.Duration{time.Minute, 6 * time.Hour, 6*time.Hour + time.Second,
				7 * 24 * time.Hour, 40 * 24 * time.Hour, 10 * 365 * 24 * time.Hour} {
				r := h.resolver.Resolve(context.Background(), []string{leaf}, 0, age, false)
				s, _ := want.StepFor(age)
				if r.Confidence != resolution.Exact || r.Step != s || r.MaxRetention != want.MaxRetention() {
					t.Errorf("age %v: got %+v want step %v", age, r, s)
				}
			}
			if h.obs.total() != before {
				t.Error("a learned ladder must not probe again")
			}
		})
	}
	if st := h.registry.Stats(); st.CompleteLadders != 5 {
		// six leaves, two share a ladder: five distinct fingerprints
		t.Errorf("expected 5 distinct ladders, got %+v", st)
	}
}

func TestLearnConfirmsKnownLadders(t *testing.T) {
	h := newHarness(t, nil)
	h.addAll()
	ctx := context.Background()
	if _, err := h.learner.Learn(ctx, "dev.fast.cpu.host01.percent", nil); err != nil {
		t.Fatal(err)
	}
	full := h.obs.total()
	// a second leaf on the same ladder is confirmed, not rediscovered
	before := h.obs.total()
	l, err := h.learner.Learn(ctx, "dev.fast.cpu.host02.percent", nil)
	if err != nil || l.String() != "10s:6h,1m:1w,10m:5y" {
		t.Fatalf("learned %v err %v", l, err)
	}
	confirm := h.obs.total() - before
	if confirm >= full/3 {
		t.Errorf("confirming a known ladder took %d probes vs %d for discovery", confirm, full)
	}
	if st := h.registry.Stats(); st.CompleteLadders != 1 || st.Leaves != 2 {
		t.Errorf("expected one shared ladder for two leaves, got %+v", st)
	}
	// a leaf on a different ladder fails the confirmation and is discovered
	before = h.obs.total()
	l, err = h.learner.Learn(ctx, "dev.coarse.users.active", nil)
	if err != nil || l.String() != "5m:90d" {
		t.Fatalf("learned %v err %v", l, err)
	}
	if st := h.registry.Stats(); st.CompleteLadders != 2 {
		t.Errorf("expected two ladders, got %+v", st)
	}
	if kl := h.registry.KnownLadders(); len(kl) != 2 || kl[0].String() != "10s:6h,1m:1w,10m:5y" {
		t.Errorf("known ladders must list the most-bound first: %v", kl)
	}
	t.Logf("discovery %d probes, confirmation %d, failed confirmation + discovery %d", full, confirm, h.obs.total()-before)
}

func TestResolveUnknownThenLearned(t *testing.T) {
	h := newHarness(t, nil)
	h.addAll()
	leaf := "dev.fast.cpu.host01.percent"
	// cold: unknown, learning scheduled, nothing speculative recorded
	r := h.resolver.Resolve(context.Background(), []string{leaf}, 0, time.Hour, false)
	if r.Confidence != resolution.Unknown || r.Reason != "unknown_step" || r.Step != 0 {
		t.Fatalf("cold resolve: %+v", r)
	}
	h.learner.Wait() // waits for the scheduled run
	if _, conf, ok := h.registry.Leaf(leaf); !ok || conf != resolution.Exact {
		t.Fatalf("expected the leaf to be learned, got %v %t", conf, ok)
	}
	r = h.resolver.Resolve(context.Background(), []string{leaf}, 0, time.Hour, false)
	if r.Confidence != resolution.Exact || r.Step != 10*time.Second || r.Source != resolution.SourceRegistry {
		t.Errorf("warm resolve: %+v", r)
	}
	if h.obs.lookups["unknown/none"] != 1 || h.obs.lookups["exact/registry"] != 1 {
		t.Errorf("lookups %v", h.obs.lookups)
	}
	if h.obs.layers[resolution.LayerLeaf] != 1 || h.obs.ladders != 1 {
		t.Errorf("gauges: layers=%v ladders=%d", h.obs.layers, h.obs.ladders)
	}
}

func TestStaticConfirmAndDrift(t *testing.T) {
	h := newHarness(t, [][2]string{
		{`^dev\.fast\.`, "10s:6h,60s:7d,10m:5y"}, // correct
		{`^dev\.drift\.`, "60s:2d,5m:30d,1h:2y"}, // what storage-schemas.conf claims
	})
	h.addAll()
	ctx := context.Background()
	// a correct static ladder is Configured before confirmation...
	r := h.resolver.Resolve(ctx, []string{"dev.fast.cpu.host01.percent"}, 0, time.Hour, false)
	if r.Confidence != resolution.Configured || r.Step != 10*time.Second || r.Source != resolution.SourceStatic {
		t.Fatalf("configured resolve: %+v", r)
	}
	h.learner.Wait()
	// ...and Exact after, having been confirmed cheaply
	r = h.resolver.Resolve(ctx, []string{"dev.fast.cpu.host01.percent"}, 0, time.Hour, false)
	if r.Confidence != resolution.Exact || r.Step != 10*time.Second {
		t.Errorf("confirmed resolve: %+v", r)
	}
	if n := h.obs.total(); n > 12 {
		t.Errorf("confirming a correct static ladder took %d probes", n)
	}

	// the drift case: config says 60s at 1h, the files say 30s
	h2 := newHarness(t, [][2]string{{`^dev\.drift\.`, "60s:2d,5m:30d,1h:2y"}})
	h2.addAll()
	leaf := "dev.drift.temperature.sensor01.celsius"
	r = h2.resolver.Resolve(ctx, []string{leaf}, 0, time.Hour, false)
	if r.Confidence != resolution.Configured || r.Step != time.Minute {
		t.Fatalf("drift configured resolve: %+v", r)
	}
	h2.learner.Wait()
	r = h2.resolver.Resolve(ctx, []string{leaf}, 0, time.Hour, false)
	if r.Confidence != resolution.Exact || r.Step != 30*time.Second || r.MaxRetention != 365*24*time.Hour {
		t.Errorf("the probe must win over static config: %+v", r)
	}
	key, _, _ := h2.registry.Leaf(leaf)
	l, _ := h2.registry.Ladder(key)
	if l.String() != "30s:12h,5m:2w,1h:1y" {
		t.Errorf("learned %s", l)
	}
}

func TestWildcardsAndLCM(t *testing.T) {
	h := newHarness(t, nil)
	h.addAll()
	ctx := context.Background()
	for _, leaf := range []string{"dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent", "odd.two"} {
		if _, err := h.learner.Learn(ctx, leaf, nil); err != nil {
			t.Fatal(err)
		}
	}
	r := h.resolver.Resolve(ctx, []string{"dev.fast.cpu.*.percent"}, 0, time.Hour, false)
	if r.Confidence != resolution.Derived || r.Step != 10*time.Second || len(r.Leaves) != 2 || r.ExpansionID == "" {
		t.Fatalf("wildcard: %+v", r)
	}
	id := r.ExpansionID
	// mixed steps normalize to the LCM
	r2 := h.resolver.Resolve(ctx, []string{"dev.fast.cpu.host01.percent", "odd.two"}, 0, 30*time.Minute, true)
	if r2.Confidence != resolution.Derived || r2.Step != 30*time.Second || r2.MaxRetention != 24*time.Hour {
		t.Errorf("lcm: %+v", r2)
	}
	// a bare expression over mixed ladders is not predictable: graphite
	// returns each series at its native step
	r2 = h.resolver.Resolve(ctx, []string{"dev.fast.cpu.host01.percent", "odd.two"}, 0, 30*time.Minute, false)
	if r2.Confidence != resolution.Unknown || r2.Reason != "unknown_step" {
		t.Errorf("bare mixed: %+v", r2)
	}
	// a fixed step (summarize) is Derived from the function
	r3 := h.resolver.Resolve(ctx, []string{"dev.fast.cpu.host01.percent"}, time.Hour, time.Hour, false)
	if r3.Confidence != resolution.Derived || r3.Step != time.Hour || r3.Source != resolution.SourceFunction {
		t.Errorf("fixed: %+v", r3)
	}
	// a new leaf under the wildcard changes the expansion token once the
	// cached expansion expires
	h.stub.Add("dev.fast.cpu.host03.percent", "10s:6h,60s:7d,10m:5y")
	if r := h.resolver.Resolve(ctx, []string{"dev.fast.cpu.*.percent"}, 0, time.Hour, false); r.ExpansionID != id {
		t.Error("expansion must be cached within its TTL")
	}
	h.registry.SetTarget("dev.fast.cpu.*.percent", nil, -time.Second) // expire it
	r4 := h.resolver.Resolve(ctx, []string{"dev.fast.cpu.*.percent"}, 0, time.Hour, false)
	if r4.ExpansionID == id || len(r4.Leaves) != 3 {
		t.Errorf("expansion token must change with the leaf set: %+v", r4)
	}
	// the new leaf is unknown until learned: Unknown, and nothing speculative
	if r4.Confidence != resolution.Unknown {
		t.Errorf("expected Unknown for an unlearned new leaf, got %+v", r4)
	}
	if _, _, ok := h.registry.Leaf("dev.fast.cpu.host03.percent"); ok {
		t.Error("no speculative leaf entry")
	}
	// no match at all
	r5 := h.resolver.Resolve(ctx, []string{"nothing.here.*"}, 0, time.Hour, false)
	if r5.Confidence != resolution.Unknown || r5.Reason != "missing_target" {
		t.Errorf("missing: %+v", r5)
	}
	// expansion failure is Unknown, not an error
	h.stub.Fail.Store(503)
	r6 := h.resolver.Resolve(ctx, []string{"dev.medium.*.*.count"}, 0, time.Hour, false)
	if r6.Confidence != resolution.Unknown || r6.Reason != "unknown_step" {
		t.Errorf("expansion failure: %+v", r6)
	}
	h.stub.Fail.Store(0)
	// existence checks
	if e := h.expander.Exists(ctx, "odd.two"); e != resolution.Exists {
		t.Error("exists")
	}
	if e := h.expander.Exists(ctx, "odd.three"); e != resolution.NotExists {
		t.Error("not exists")
	}
	h.stub.Fail.Store(500)
	if e := h.expander.Exists(ctx, "odd.two"); e != resolution.ExistsUnknown {
		t.Error("exists unknown on failure")
	}
	h.stub.Fail.Store(0)
}

func TestLearnFailures(t *testing.T) {
	h := newHarness(t, nil)
	h.addAll()
	ctx := context.Background()
	// missing metric
	if _, err := h.learner.Learn(ctx, "no.such.metric", nil); !errors.Is(err, resolution.ErrMissingMetric) {
		t.Errorf("expected ErrMissingMetric, got %v", err)
	}
	if _, ok := h.registry.Negative("no.such.metric"); !ok {
		t.Error("failed learn must be negative-cached")
	}
	// the resolver honors the negative entry: no new run is scheduled
	before := h.stub.Renders.Load()
	r := h.resolver.Resolve(ctx, []string{"no.such.metric"}, 0, time.Hour, false)
	h.learner.Wait()
	if r.Confidence != resolution.Unknown || h.stub.Renders.Load() != before {
		t.Errorf("negative-cached leaf must not be re-probed: %+v renders=%d", r, h.stub.Renders.Load()-before)
	}
	// origin failure mid-run: error, partial ladder kept
	h2 := newHarness(t, nil)
	h2.addAll()
	h2.stub.Fail.Store(502)
	if _, err := h2.learner.Learn(ctx, "odd.two", nil); err == nil {
		t.Error("expected an error from a failing origin")
	}
	h2.stub.Fail.Store(0)
	// budget exhaustion: partial ladder recorded, ages it covers answered
	h3 := newHarness(t, nil)
	h3.addAll()
	h3.learner.Budget = 3
	l, err := h3.learner.Learn(ctx, "dev.fast.cpu.host01.percent", nil)
	if !errors.Is(err, resolution.ErrProbeBudget) || l == nil || l.State != resolution.StatePartial {
		t.Errorf("expected a partial ladder and ErrProbeBudget, got %v %v", l, err)
	}
	if s, ok := l.StepFor(time.Second); !ok || s != 10*time.Second {
		t.Errorf("partial ladder must answer observed ages: %v %t", s, ok)
	}
	r = h3.resolver.Resolve(ctx, []string{"dev.fast.cpu.host01.percent"}, 0, time.Second, false)
	if r.Confidence != resolution.Exact || r.Step != 10*time.Second || r.MaxRetention != -1 {
		t.Errorf("partial resolve: %+v", r)
	}
	if r := h3.resolver.Resolve(ctx, []string{"dev.fast.cpu.host01.percent"}, 0, 10*24*time.Hour, false); r.Confidence != resolution.Unknown {
		t.Errorf("partial ladder must not answer unobserved ages: %+v", r)
	}
	// context cancellation
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := h3.learner.Learn(cctx, "odd.two", nil); err == nil {
		t.Error("expected a cancellation error")
	}
}

func TestScheduleDedupAndCap(t *testing.T) {
	h := newHarness(t, nil)
	h.addAll()
	h.learner.Concurrency = 1
	if !h.learner.Schedule("dev.fast.cpu.host01.percent", nil) {
		t.Fatal("first schedule must start")
	}
	// the same leaf is deduplicated, and a second leaf exceeds the cap
	if h.learner.Schedule("dev.fast.cpu.host01.percent", nil) {
		t.Error("duplicate schedule must be refused")
	}
	if h.learner.Schedule("odd.two", nil) {
		t.Error("schedule beyond the concurrency cap must be refused")
	}
	h.learner.Wait()
	if _, _, ok := h.registry.Leaf("dev.fast.cpu.host01.percent"); !ok {
		t.Error("scheduled run must complete")
	}
	// after Close the learner is cancelled: runs end immediately
	h.learner.Close()
	if h.learner.Schedule("odd.two", nil) {
		h.learner.Wait()
	}
	if _, _, ok := h.registry.Leaf("odd.two"); ok {
		t.Error("a run after Close must not complete")
	}
}

func TestObserveLearnsOnFirstResponse(t *testing.T) {
	h := newHarness(t, nil)
	h.addAll()
	ctx := context.Background()
	leaf := "dev.medium.orders.us-east.count"
	// a captured response at age 1h reported a 60s step
	h.resolver.Observe([]string{leaf}, time.Hour, time.Minute)
	r := h.resolver.Resolve(ctx, []string{leaf}, 0, time.Hour, false)
	if r.Confidence != resolution.Exact || r.Step != time.Minute {
		t.Errorf("observed age must resolve exactly: %+v", r)
	}
	if r := h.resolver.Resolve(ctx, []string{leaf}, 0, 10*24*time.Hour, false); r.Confidence == resolution.Exact && r.Step == time.Minute {
		t.Errorf("an unobserved age must not be guessed: %+v", r)
	}
	h.learner.Wait() // full discovery was scheduled by Observe
	r = h.resolver.Resolve(ctx, []string{leaf}, 0, 10*24*time.Hour, false)
	if r.Confidence != resolution.Exact || r.Step != 5*time.Minute {
		t.Errorf("after discovery: %+v", r)
	}
	// a response contradicting a complete ladder bumps the generation
	g := h.registry.Generation()
	h.resolver.Observe([]string{leaf}, time.Hour, 7*time.Minute)
	if h.registry.Generation() != g+1 {
		t.Error("contradiction must bump the generation")
	}
	h.learner.Wait()
	// a misprediction also bumps the generation and relearns the leaf
	g = h.registry.Generation()
	h.resolver.Mispredict([]string{leaf}, time.Minute, 10*time.Second)
	if h.registry.Generation() != g+1 || h.obs.lookups["misprediction"] != 1 {
		t.Error("misprediction must bump the generation and be observed")
	}
	h.learner.Wait()
	if _, _, ok := h.registry.Leaf(leaf); !ok {
		t.Error("misprediction must relearn the leaf")
	}
	// consistent observations on a complete ladder are a no-op
	g = h.registry.Generation()
	h.resolver.Observe([]string{leaf}, time.Hour, time.Minute)
	if h.registry.Generation() != g {
		t.Error("consistent observation must not bump")
	}
	// multi-leaf and zero-step observations are ignored
	h.resolver.Observe([]string{"a", "b"}, time.Hour, time.Minute)
	h.resolver.Observe([]string{"zz"}, time.Hour, 0)
	if _, _, ok := h.registry.Leaf("zz"); ok {
		t.Error("zero step must not be recorded")
	}
	// an inconsistent partial observation restarts the partial ladder
	h.resolver.Observe([]string{"p.q"}, time.Hour, time.Minute)
	h.resolver.Observe([]string{"p.q"}, 2*time.Hour, 10*time.Second)
	key, _, _ := h.registry.Leaf("p.q")
	l, _ := h.registry.Ladder(key)
	if l == nil || len(l.Observations) != 1 || l.Observations[0].Step != 10*time.Second {
		t.Errorf("expected a restarted partial ladder, got %v", l)
	}
	h.learner.Wait()
}

func TestProbeParsing(t *testing.T) {
	h := newHarness(t, nil)
	h.addAll()
	p := &resolution.Prober{Origin: h.learner.Prober.Origin}
	ctx := context.Background()
	// narrow inside retention returns the rung's step
	r := p.Narrow(ctx, "dev.coarse.users.active", time.Hour, h.now)
	if r.Result != resolution.ResultStep || r.Step != 5*time.Minute || r.Kind != resolution.KindNarrow {
		t.Errorf("narrow: %+v", r)
	}
	// narrow beyond retention is empty
	r = p.Narrow(ctx, "dev.coarse.users.active", 91*24*time.Hour, h.now)
	if r.Result != resolution.ResultEmpty {
		t.Errorf("narrow beyond retention: %+v", r)
	}
	// wide beyond retention is clamped
	r = p.Wide(ctx, "dev.coarse.users.active", 400*24*time.Hour, h.now, nil)
	if r.Result != resolution.ResultStep || h.now.Sub(r.Start) > 90*24*time.Hour || r.Kind != resolution.KindWide {
		t.Errorf("wide clamp: %+v", r)
	}
	// errors
	h.stub.Fail.Store(500)
	if r := p.Narrow(ctx, "dev.coarse.users.active", time.Hour, h.now); r.Result != resolution.ResultError || r.Err == nil {
		t.Errorf("error: %+v", r)
	}
	h.stub.Fail.Store(0)
	bad := &resolution.Prober{Origin: &resolution.Origin{}}
	if r := bad.Narrow(ctx, "x", time.Hour, h.now); r.Result != resolution.ResultError {
		t.Error("unconfigured origin must error")
	}
	ctxc, cancel := context.WithCancel(ctx)
	cancel()
	if r := p.Narrow(ctxc, "x", time.Hour, h.now); r.Result != resolution.ResultError {
		t.Error("cancelled context must error")
	}
}

func TestRetentionEdgeFallbacks(t *testing.T) {
	for _, skew := range []int64{int64(10 * time.Minute), -int64(20 * time.Minute)} {
		h := newHarness(t, nil)
		h.addAll()
		h.stub.StartSkew.Store(skew)
		l, err := h.learner.Learn(context.Background(), "dev.fast.cpu.host01.percent", nil)
		if err != nil || l.String() != "10s:6h,1m:1w,10m:5y" {
			t.Errorf("skew %v: learned %v err %v", time.Duration(skew), l, err)
		}
		t.Logf("skew %v: %d probes", time.Duration(skew), h.obs.total())
	}
}

func TestStaticHintMismatches(t *testing.T) {
	ctx := context.Background()
	for _, hint := range []string{
		"10s:6h,60s:7d,10m:4y", // wrong maxRetention
		"10s:5h,60s:7d,10m:5y", // wrong boundary
		"30s:6h,60s:7d,10m:5y", // wrong finest step
		"10s:6h,60s:7d",        // missing rung: data beyond its claimed retention
	} {
		h := newHarness(t, nil)
		h.addAll()
		hl, _ := resolution.ParseRetentions(hint)
		l, err := h.learner.Learn(ctx, "dev.fast.cpu.host01.percent", hl)
		if err != nil || l.String() != "10s:6h,1m:1w,10m:5y" {
			t.Errorf("hint %s: learned %v err %v", hint, l, err)
		}
	}
	// a failing origin during confirmation is an error, not a relearn
	h := newHarness(t, nil)
	h.addAll()
	h.stub.Fail.Store(500)
	hl, _ := resolution.ParseRetentions("10s:6h,60s:7d,10m:5y")
	if _, err := h.learner.Learn(ctx, "dev.fast.cpu.host01.percent", hl); err == nil {
		t.Error("expected an error")
	}
}

func TestShortFinestRung(t *testing.T) {
	h := newHarness(t, nil)
	h.stub.Add("short.rung", "10s:1m,20s:2m,60s:1h")
	l, err := h.learner.Learn(context.Background(), "short.rung", nil)
	if err != nil || l.String() != "10s:1m,20s:2m,1m:1h" {
		t.Errorf("learned %v err %v", l, err)
	}
	if s, _ := l.StepFor(30 * time.Second); s != 10*time.Second {
		t.Error("a rung shorter than the sweep start must still be found")
	}
}

func TestImmediatelySaturatingLadder(t *testing.T) {
	// degenerate shape: one rung whose retention is shorter than the sweep's
	// first step outward, so the first probe already falls past maxRetention
	h := newHarness(t, nil)
	h.stub.Add("tiny.one", "10s:1m")
	l, err := h.learner.Learn(context.Background(), "tiny.one", nil)
	if err != nil || l == nil {
		t.Fatalf("learned %v err %v", l, err)
	}
	if l.State != resolution.StateComplete || l.String() != "10s:1m" {
		t.Errorf("learned %v (%v), want a complete 10s:1m", l, l.State)
	}
	if s, ok := l.StepFor(30 * time.Second); !ok || s != 10*time.Second {
		t.Errorf("inside retention: %v %t", s, ok)
	}
	// past maxRetention the ladder saturates: the origin answers at the
	// coarsest rung's step over the clamped window
	if s, ok := l.StepFor(2 * time.Minute); !ok || s != 10*time.Second {
		t.Errorf("past maxRetention: %v %t, want the coarsest step", s, ok)
	}
	if l.MaxRetention() != time.Minute {
		t.Errorf("maxRetention %v, want 1m", l.MaxRetention())
	}
}
