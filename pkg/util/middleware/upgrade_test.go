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

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsUpgradeRequest(t *testing.T) {
	tests := []struct {
		name     string
		conn     string
		upgrade  string
		expected bool
	}{
		{"websocket", "Upgrade", "websocket", true},
		{"token in a list", "keep-alive, Upgrade", "websocket", true},
		{"case insensitive", "upgrade", "h2c", true},
		{"no upgrade header", "Upgrade", "", false},
		{"no connection token", "keep-alive", "websocket", false},
		{"neither", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
			if tc.conn != "" {
				r.Header.Set("Connection", tc.conn)
			}
			if tc.upgrade != "" {
				r.Header.Set("Upgrade", tc.upgrade)
			}
			if got := IsUpgradeRequest(r); got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
	if IsUpgradeRequest(nil) {
		t.Error("nil request must not be an upgrade")
	}
}

func TestUpgradeSwitch(t *testing.T) {
	var tookPassthrough, tookNext bool
	passthrough := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { tookPassthrough = true })
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { tookNext = true })
	h := UpgradeSwitch(passthrough, next)

	r := httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !tookPassthrough || tookNext {
		t.Error("upgrade request should route to passthrough")
	}

	tookPassthrough, tookNext = false, false
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil))
	if tookPassthrough || !tookNext {
		t.Error("plain request should route to next")
	}

	// with no passthrough available the chain must be left intact
	tookNext = false
	UpgradeSwitch(nil, next).ServeHTTP(httptest.NewRecorder(), r)
	if !tookNext {
		t.Error("nil passthrough should fall through to next")
	}
}
