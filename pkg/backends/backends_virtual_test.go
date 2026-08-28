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

package backends

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
)

// Issue #996: a virtual backend (ALB, Rule) used as a pool member of another
// ALB must surface in the health checker's status registry so the /health
// endpoint can report the nested ALB as an available pool member instead of
// "nc" (uncheckedPoolMembers). Today StartHealthChecks skips virtuals
// entirely, so st[name] is nil at the health builder.
func TestStartHealthChecksRegistersVirtualBackends(t *testing.T) {
	cases := []struct {
		name     string
		provider string
	}{
		{"alb-inner", providers.ALB},
		{"rule-inner", providers.Rule},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			o := bo.New()
			o.Provider = c.provider
			cl, err := New(c.name, o, nil, lm.NewRouter(), nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			b := Backends{c.name: cl}
			hc, err := b.StartHealthChecks(nil)
			if err != nil {
				t.Fatalf("StartHealthChecks: %v", err)
			}
			st, ok := hc.Statuses()[c.name]
			if !ok || st == nil {
				t.Fatalf("expected statuses to contain virtual backend %q", c.name)
			}
			if got := st.Get(); got != healthcheck.StatusPassing {
				t.Errorf("expected StatusPassing (%d) got %d", healthcheck.StatusPassing, got)
			}
		})
	}
}
