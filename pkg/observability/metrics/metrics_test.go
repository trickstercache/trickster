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

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHealthcheckProbeLatencyRegistered(t *testing.T) {
	if err := prometheus.Register(HealthcheckProbeLatency); err == nil {
		t.Fatal("expected HealthcheckProbeLatency to already be registered")
	} else if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
		t.Fatalf("expected AlreadyRegisteredError, got %T: %v", err, err)
	}
	HealthcheckProbeLatency.WithLabelValues("test-target").Observe(0.123)
}
