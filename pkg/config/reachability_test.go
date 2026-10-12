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

package config

import (
	"maps"
	"testing"

	uropts "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func TestBackendReachabilityFollowsIndirectHTTPDispatch(t *testing.T) {
	c := &Config{
		Listeners: listener.Lookup{"edge": {Protocol: listener.ProtocolHTTP}, "other": {Protocol: listener.ProtocolHTTP}},
		Backends: bo.Lookup{
			"front": {
				ListenerNames: []string{"edge"},
				ALBOptions: &ao.Options{
					Pool:      ao.PoolMemberList{{Name: "pool"}, {Name: "missing"}},
					Discovery: &ao.DiscoveryOptions{TemplateBackend: "template"},
					UserRouter: &uropts.Options{
						DefaultBackend: "default-user",
						Users:          uropts.UserMappingOptionsByUser{"alice": {ToBackend: "user"}, "nil": nil},
					},
				},
				RuleName: "dispatch",
				Paths:    po.List{nil, {Mirrors: []*po.MirrorOptions{nil, {BackendName: "mirror"}}}},
			},
			"pool":         {ALBOptions: &ao.Options{Pool: ao.PoolMemberList{{Name: "front"}}}},
			"template":     {IsTemplate: true, ListenerNames: []string{"edge"}},
			"default-user": {}, "user": {}, "next": {}, "case": {}, "mirror": {},
			"unrelated": {ListenerNames: []string{"other"}},
			"nil":       nil,
		},
		Rules: ro.Lookup{"dispatch": {NextRoute: "next", CaseOptions: ro.CaseOptionsList{nil, {NextRoute: "case"}}}},
	}
	want := sets.New([]string{"front", "pool", "template", "default-user", "user", "next", "case", "mirror"})
	if got := c.BackendReachability(true, "edge"); !maps.Equal(got, want) {
		t.Fatalf("edge reaches %v; want %v", got, want)
	}
	want.Set("unrelated")
	if got := c.BackendReachability(true); !maps.Equal(got, want) {
		t.Fatalf("all HTTP listeners reach %v; want %v", got, want)
	}
	if got := c.BackendReachability(true, "missing"); len(got) != 0 {
		t.Fatalf("missing listener reaches %v", got)
	}
	c.Listeners["edge"].Protocol = listener.ProtocolTCP
	if got := c.BackendReachability(false, "edge"); !maps.Equal(got, sets.New([]string{"pool", "missing", "template", "front"})) {
		t.Fatalf("stream members = %v", got)
	}
	if got := (*Config)(nil).BackendReachability(true); len(got) != 0 {
		t.Fatalf("nil config reaches %v", got)
	}
}
