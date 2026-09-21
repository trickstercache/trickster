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
// Package observe binds the load-balancing core's events to Trickster's logger and metrics.
package observe

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

// WorkerRefresh labels a panic recovered while a pool rebuilt its snapshot.
const WorkerRefresh = "refresh"

// balancerObserver meters one ALB's balancer events
type balancerObserver struct{ albName string }

// Balancer returns the observer for the named ALB's balancer.
func Balancer(albName string) lb.Observer {
	return balancerObserver{albName: albName}
}

func (o balancerObserver) Observe(ev lb.Event) {
	if ev.Kind != lb.EventEjected {
		return
	}
	logger.Warn("alb pool member ejected after repeated connect failures", logging.Pairs{
		"albName": o.albName, "member": ev.Member,
	})
	metrics.ALBMemberEjections.WithLabelValues(o.albName, ev.Member).Inc()
}

type poolObserver struct{}

// Pool returns the observer every ALB pool reports to.
func Pool() lb.Observer {
	return poolObserver{}
}

func (poolObserver) Observe(ev lb.Event) {
	if ev.Kind != lb.EventPanic {
		return
	}
	logger.Error("alb pool refresh panic", logging.Pairs{
		"worker": WorkerRefresh,
		"panic":  fmt.Sprintf("%v", ev.Panic),
		"stack":  string(ev.Stack),
	})
	metrics.ALBPoolRefreshPanicRecovered.WithLabelValues(WorkerRefresh).Inc()
}
