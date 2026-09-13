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
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestKubeMetricsRegistered(t *testing.T) {
	for name, c := range map[string]prometheus.Collector{
		"reconciles":          KubeReconciles,
		"reconcileDuration":   KubeReconcileDuration,
		"reconcileErrors":     KubeReconcileErrors,
		"watchEvents":         KubeWatchEvents,
		"translateDuration":   KubeTranslateDuration,
		"applyDuration":       KubeApplyDuration,
		"generatedObjects":    KubeGeneratedObjects,
		"statusWriteFailures": KubeStatusWriteFailures,
		"leader":              KubeLeader,
		"lastSuccessfulSync":  KubeLastSuccessfulSync,
		"routeInfo":           KubeRouteInfo,
	} {
		err := prometheus.Register(c)
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			t.Errorf("expected %s to already be registered, got %v", name, err)
		}
	}
	KubeReconciles.WithLabelValues(KubeResultApplied).Inc()
	KubeWatchEvents.WithLabelValues("Ingress", "add").Inc()
	KubeRouteInfo.WithLabelValues("Ingress", "web", "shop", "kgw--ingress.shop.web_r0").Set(1)
}
