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

package options

import (
	"testing"
	"time"
)

// User-supplied threshold/timeout values must overlay onto the provider
// default. Without this, a user setting failure_threshold: 1 silently inherits
// the target.go fallback of 3, delaying the unavailable transition and
// widening the window in which an unhealthy member receives traffic.
func TestOverlayCarriesThresholdsAndTimeout(t *testing.T) {
	base := &Options{}
	custom := &Options{
		FailureThreshold:  1,
		RecoveryThreshold: 2,
		Timeout:           3 * time.Second,
	}
	base.Overlay(custom)

	if base.FailureThreshold != 1 {
		t.Errorf("FailureThreshold: expected 1 got %d", base.FailureThreshold)
	}
	if base.RecoveryThreshold != 2 {
		t.Errorf("RecoveryThreshold: expected 2 got %d", base.RecoveryThreshold)
	}
	if base.Timeout != 3*time.Second {
		t.Errorf("Timeout: expected 3s got %v", base.Timeout)
	}
}

// URL() composes Scheme + Host + Path + Query. Without Overlay carrying
// Scheme/Host, a user that configures `scheme: https` or `host: hc.example.com`
// in their backend's healthcheck block silently inherits the upstream's
// scheme/host and probes the wrong endpoint.
func TestOverlayCarriesSchemeAndHost(t *testing.T) {
	base := &Options{Scheme: "http", Host: "default.example.com"}
	custom := &Options{Scheme: "https", Host: "hc.example.com"}
	base.Overlay(custom)

	if base.Scheme != "https" {
		t.Errorf("Scheme: expected https got %s", base.Scheme)
	}
	if base.Host != "hc.example.com" {
		t.Errorf("Host: expected hc.example.com got %s", base.Host)
	}
}

// Non-positive custom values should not stomp existing base values.
func TestOverlayPreservesBaseWhenCustomZero(t *testing.T) {
	base := &Options{
		FailureThreshold:  5,
		RecoveryThreshold: 6,
		Timeout:           7 * time.Second,
	}
	base.Overlay(&Options{})

	if base.FailureThreshold != 5 {
		t.Errorf("FailureThreshold: expected 5 got %d", base.FailureThreshold)
	}
	if base.RecoveryThreshold != 6 {
		t.Errorf("RecoveryThreshold: expected 6 got %d", base.RecoveryThreshold)
	}
	if base.Timeout != 7*time.Second {
		t.Errorf("Timeout: expected 7s got %v", base.Timeout)
	}
}
