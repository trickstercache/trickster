//go:build !race

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
package sticky

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"

	"github.com/stretchr/testify/require"
)

// the race detector makes a sync.Pool drop some of what it is given, so this runs without it
func TestQuietHitDoesNotAllocate(t *testing.T) {
	cookie := persistence(t, "{}")
	h, _ := serve(cookie, request(nil), clock, leaf)
	r := request(withCookie(options.DefaultCookieName, tokenIn(t, h, options.DefaultCookieName)))
	table := persistence(t, "mode: table\n")
	tr := request(nil)
	_, _ = serve(table, tr, clock, leaf)
	for name, p := range map[string]*HTTP{"cookie": cookie, "table": table} {
		req := r
		if name == "table" {
			req = tr
		}
		var s Session
		out := http.Header{}
		allocs := testing.AllocsPerRun(100, func() {
			p.Begin(req, &s, clock)
			s.Chosen = s.Pins
			p.Finish(out, req, &s)
		})
		require.Zero(t, allocs, "%s mode allocates on a quiet hit", name)
		require.Equal(t, leaf, s.Pins, name)
	}
}
