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
	"maps"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"

	"github.com/prometheus/client_golang/prometheus"
)

// memberInflightDesc describes the in-flight gauge. It is filled at scrape time from each
// tracked balancer's members, so dispatch pays nothing for it and a member that leaves its
// pool takes its series with it.
var memberInflightDesc = prometheus.NewDesc(
	"trickster_alb_member_inflight",
	"Current number of requests in flight to an ALB pool member, for mechanisms that track it.",
	[]string{keys.ALB_Name, keys.Member}, nil,
)

var (
	trackedMtx sync.Mutex
	tracked    = make(map[string]*lb.Balancer)
)

// Track exports the in-flight count of every member of the named ALB's balancer. It does
// nothing for a strategy that keeps no in-flight count.
func Track(albName string, b *lb.Balancer) {
	if b == nil || albName == "" || !b.Needs().Has(lb.NeedInflight) {
		return
	}
	trackedMtx.Lock()
	tracked[albName] = b
	trackedMtx.Unlock()
}

// Untrack stops exporting the named ALB's members, unless another balancer has since taken
// the name, as the ALB of a reloaded config does.
func Untrack(albName string, b *lb.Balancer) {
	trackedMtx.Lock()
	if tracked[albName] == b {
		delete(tracked, albName)
	}
	trackedMtx.Unlock()
}

type memberCollector struct{}

func (memberCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- memberInflightDesc
}

func (memberCollector) Collect(ch chan<- prometheus.Metric) {
	trackedMtx.Lock()
	balancers := make(map[string]*lb.Balancer, len(tracked))
	maps.Copy(balancers, tracked)
	trackedMtx.Unlock()
	for name, b := range balancers {
		p := b.Pool()
		if p == nil {
			continue
		}
		for _, m := range p.Configured() {
			if m.Name() == "" {
				continue
			}
			ch <- prometheus.MustNewConstMetric(memberInflightDesc, prometheus.GaugeValue,
				float64(m.Stats().Inflight()), name, m.Name())
		}
	}
}

func init() {
	prometheus.MustRegister(memberCollector{})
}
