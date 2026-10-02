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
package hrw_test

import (
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/hrw"
	"github.com/trickstercache/trickster/v2/pkg/lb/lbtest"
)

func TestConformance(t *testing.T) {
	lbtest.Run(t, hrw.New, lbtest.Options{})
}

func TestWeights(t *testing.T) {
	lbtest.RunWeighted(t, hrw.New, lbtest.WeightOptions{Keyed: true})
}

func BenchmarkSelect(b *testing.B) {
	lbtest.Bench(b, hrw.New)
}

func named(names []string, weights []int) []*lb.Member {
	members := make([]*lb.Member, len(names))
	for i, n := range names {
		w := 1
		if weights != nil {
			w = weights[i]
		}
		members[i] = lb.NewMember(lb.MemberOptions{Name: n, Weight: w})
	}
	return members
}

func balancerOver(t *testing.T, members []*lb.Member) *lb.Balancer {
	t.Helper()
	p, err := lb.NewPool(members, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return lb.NewBalancer(hrw.New(), lb.BalancerOptions{Pool: p})
}

func owner(t *testing.T, b *lb.Balancer, key string) string {
	t.Helper()
	pk, ok := b.Pick(lb.Flow{Key: lb.HashString(key), HasKey: true})
	if !ok {
		t.Fatal("no pick")
	}
	return pk.Member().Name()
}

func TestIdentity(t *testing.T) {
	s := hrw.New()
	if s.Name() != hrw.Name || s.Needs() != lb.NeedKey {
		t.Errorf("name %q needs %b", s.Name(), s.Needs())
	}
}

// the mapping is a fixed function of key and member names: every replica, and every restart
// of one, must send a client to the same member. A change here reshuffles deployed affinity.
func TestGoldenMapping(t *testing.T) {
	names := []string{"cache-a", "cache-b", "cache-c", "cache-d"}
	uniform := balancerOver(t, named(names, nil))
	weighted := balancerOver(t, named(names, []int{1, 2, 3, 4}))
	// the same members in another order, as a second replica might list them
	shuffled := balancerOver(t, named([]string{"cache-c", "cache-a", "cache-d", "cache-b"}, nil))
	golden := []struct{ key, uniform, weighted string }{
		{"10.0.0.1", "cache-a", "cache-a"},
		{"10.0.0.2", "cache-d", "cache-d"},
		{"10.0.0.4", "cache-a", "cache-d"},
		{"192.0.2.10", "cache-a", "cache-a"},
		{"2001:db8::1", "cache-d", "cache-d"},
		{"tenant-42", "cache-d", "cache-d"},
		{"tenant-43", "cache-a", "cache-a"},
		{"tenant-44", "cache-b", "cache-d"},
		{"", "cache-d", "cache-d"},
	}
	for _, g := range golden {
		if got := owner(t, uniform, g.key); got != g.uniform {
			t.Errorf("uniform owner of %q = %s, want %s", g.key, got, g.uniform)
		}
		if got := owner(t, weighted, g.key); got != g.weighted {
			t.Errorf("weighted owner of %q = %s, want %s", g.key, got, g.weighted)
		}
		if got := owner(t, shuffled, g.key); got != g.uniform {
			t.Errorf("owner of %q depends on member order: %s, want %s", g.key, got, g.uniform)
		}
	}
}

// losing a member moves only the keys it owned; gaining one takes only about its share
func TestMinimalDisruption(t *testing.T) {
	for name, weights := range map[string][]int{"uniform": nil, "weighted": {3, 1, 2, 1, 3}} {
		t.Run(name, func(t *testing.T) {
			names := []string{"m0", "m1", "m2", "m3", "m4"}
			all := named(names, weights)
			before := balancerOver(t, all)
			after := balancerOver(t, all[:4])
			const keys = 20000
			var moved, owned int
			for i := range keys {
				key := "client-" + strconv.Itoa(i)
				was, is := owner(t, before, key), owner(t, after, key)
				if was == "m4" {
					owned++
					continue
				}
				if was != is {
					moved++
				}
			}
			if moved != 0 {
				t.Errorf("%d keys that m4 never owned moved when it left", moved)
			}
			share := float64(all[4].Weight())
			var total float64
			for _, m := range all {
				total += float64(m.Weight())
			}
			if got, want := float64(owned)/keys, share/total; got < want-0.02 || got > want+0.02 {
				t.Errorf("m4 owned %.3f of the keys, want about %.3f", got, want)
			}
		})
	}
}

// a flow with nothing to key on has no affinity to keep, so it is spread, not piled on one member
func TestKeylessFlowsSpread(t *testing.T) {
	b := balancerOver(t, named([]string{"a", "b", "c"}, nil))
	seen := make(map[string]int)
	for range 3000 {
		pk, _ := b.Pick(lb.Flow{})
		seen[pk.Member().Name()]++
	}
	for _, n := range []string{"a", "b", "c"} {
		if seen[n] < 800 {
			t.Errorf("%s took %d of 3000 keyless flows", n, seen[n])
		}
	}
}

// unnamed members are told apart by position, so they do not all score alike
func TestUnnamedMembersShareKeys(t *testing.T) {
	b := balancerOver(t, []*lb.Member{
		lb.NewMember(lb.MemberOptions{}), lb.NewMember(lb.MemberOptions{}), lb.NewMember(lb.MemberOptions{}),
	})
	seen := make(map[*lb.Member]int)
	for i := range 3000 {
		pk, _ := b.Pick(lb.Flow{Key: lb.HashString(strconv.Itoa(i)), HasKey: true})
		seen[pk.Member()]++
	}
	if len(seen) != 3 {
		t.Errorf("keys reached %d of 3 unnamed members", len(seen))
	}
}
