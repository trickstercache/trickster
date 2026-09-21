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
package observe

import (
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lc"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMemberInflightIsCollectedAtScrapeTime(t *testing.T) {
	members := []*lb.Member{
		lb.NewMember(lb.MemberOptions{Name: "a"}),
		lb.NewMember(lb.MemberOptions{Name: "b"}),
		lb.NewMember(lb.MemberOptions{}),
	}
	p, err := lb.NewPool(members, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	b := lb.NewBalancer(lc.New(), lb.BalancerOptions{Pool: p})
	Track("inflight-alb", b)
	Track("", b)
	Track("untracked-rr", lb.NewBalancer(rr.New(), lb.BalancerOptions{Pool: p}))
	Track("no-balancer", nil)
	poolless := lb.NewBalancer(lc.New())
	Track("poolless-alb", poolless)
	defer Untrack("poolless-alb", poolless)

	var held []lb.Pick
	for range 6 {
		pk, _ := b.Pick(lb.Flow{})
		held = append(held, pk)
	}
	// six flows over three members, one of them unnamed and so not exported
	want := `
# HELP trickster_alb_member_inflight Current number of requests in flight to an ALB pool member, for mechanisms that track it.
# TYPE trickster_alb_member_inflight gauge
trickster_alb_member_inflight{alb_name="inflight-alb",member="a"} 2
trickster_alb_member_inflight{alb_name="inflight-alb",member="b"} 2
`
	if err := testutil.CollectAndCompare(memberCollector{}, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
	for _, pk := range held {
		pk.Done(lb.OutcomeOK)
	}

	// a reloaded ALB takes over its name; stopping the old one must not drop the new series
	next := lb.NewBalancer(lc.New(), lb.BalancerOptions{Pool: p})
	Track("inflight-alb", next)
	Untrack("inflight-alb", b)
	if got := testutil.CollectAndCount(memberCollector{}); got != 2 {
		t.Errorf("series after the old balancer stopped = %d, want 2", got)
	}
	Untrack("inflight-alb", next)
	if got := testutil.CollectAndCount(memberCollector{}); got != 0 {
		t.Errorf("series after untracking = %d", got)
	}
}
