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
package stream

import (
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
)

// refusedKey counts the flows an upstream refused
const refusedKey = ""

// spec is one pool member as a test describes it
type spec struct {
	backend backends.Backend
	weight  int
	status  *healthcheck.Status
}

func up(b backends.Backend, weight int) spec {
	return spec{backend: b, weight: weight,
		status: healthcheck.NewStatus(b.Name(), "", "", healthcheck.StatusPassing, time.Time{}, nil)}
}

func down(b backends.Backend, weight int) spec {
	return spec{backend: b, weight: weight,
		status: healthcheck.NewStatus(b.Name(), "", "", healthcheck.StatusFailing, time.Time{}, nil)}
}

// origin is a backend dialed at addr; an empty addr leaves it with nothing to dial
func origin(t testing.TB, name, addr string) backends.Backend {
	t.Helper()
	o := bo.New()
	if addr != "" {
		o.OriginURL = "tcp://" + addr
	}
	if err := o.Initialize(name); err != nil {
		t.Fatal(err)
	}
	b, err := backends.New(name, o, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newALB builds and starts a real load balancer backend over the members, which may be
// load balancers themselves
func newALB(t testing.TB, name, mechanism string, members ...spec) backends.Backend {
	t.Helper()
	return newALBWith(t, name, mechanism, nil, members...)
}

// newALBWith is newALB with the load balancer's options adjusted before it is built
func newALBWith(t testing.TB, name, mechanism string, adjust func(*ao.Options), members ...spec) backends.Backend {
	t.Helper()
	o := bo.New()
	o.Provider = providers.ALB
	o.ALBOptions = &ao.Options{MechanismName: mechanism, HealthyFloor: int(healthcheck.StatusUnchecked)}
	if adjust != nil {
		adjust(o.ALBOptions)
	}
	clients := backends.Backends{}
	statuses := healthcheck.StatusLookup{}
	for _, m := range members {
		o.ALBOptions.Pool = append(o.ALBOptions.Pool, ao.PoolMember{Name: m.backend.Name(), Weight: m.weight})
		clients[m.backend.Name()] = m.backend
		statuses[m.backend.Name()] = m.status
	}
	if err := o.Initialize(name); err != nil {
		t.Fatal(err)
	}
	cl, err := alb.NewClient(name, o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	clients[name] = cl
	c := cl.(*alb.Client)
	if err := c.ValidateAndStartPool(clients, statuses); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.StopPool)
	return cl
}

func pickN(u l4.Upstream, n int) []string {
	seq := make([]string, n)
	for i := range seq {
		if r, ok := u.Pick(l4.Flow{}); ok {
			seq[i] = r.Addr()
			r.Dialed(time.Millisecond, nil)
			r.Closed(nil)
		}
	}
	return seq
}

func tally(seq []string) map[string]int {
	counts := make(map[string]int)
	for _, addr := range seq {
		counts[addr]++
	}
	return counts
}

// assertEveryWindowExact fails unless every run of total consecutive selections matches want,
// wherever the run starts: apportionment is pinned, the rotation's phase is not
func assertEveryWindowExact(t *testing.T, seq []string, want map[string]int) {
	t.Helper()
	var total int
	for _, n := range want {
		total += n
	}
	for start := 0; start+total <= len(seq); start++ {
		if got := tally(seq[start : start+total]); !maps.Equal(got, want) {
			t.Fatalf("selections %d-%d apportioned %v, want %v", start, start+total-1, got, want)
		}
	}
}
