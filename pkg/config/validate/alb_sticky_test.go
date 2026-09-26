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
package validate

import (
	"strings"
	"testing"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	sticky "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func TestStickyWarnsOfAPerProcessKeyOnHTTP(t *testing.T) {
	stickyALB := func(o *sticky.Options, listeners ...string) *config.Config {
		if err := o.Initialize(); err != nil {
			t.Fatal(err)
		}
		c := config.NewConfig()
		c.Listeners["relay"] = listener.New("relay")
		c.Listeners["relay"].Protocol = listener.ProtocolTCP
		b := bo.New()
		b.Provider = providers.ALB
		b.ListenerNames = listeners
		b.ALBOptions = &ao.Options{MechanismName: "rr", Sticky: o}
		c.Backends = bo.Lookup{"lb": b}
		return c
	}
	warned := func(c *config.Config) bool {
		for _, w := range c.LoaderWarnings {
			if strings.Contains(w, "sticky.secret") && strings.Contains(w, `alb "lb"`) {
				return true
			}
		}
		return false
	}
	key := sticky.Options{Secret: "0123456789abcdef0123456789abcdef"}
	for name, test := range map[string]struct {
		o         sticky.Options
		listeners []string
		streams   []string
		want      bool
	}{
		"default mode on http":   {sticky.Options{}, nil, nil, true},
		"cookie on http":         {sticky.Options{Mode: sticky.ModeCookie}, nil, nil, true},
		"header on http":         {sticky.Options{Mode: sticky.ModeHeader}, nil, nil, true},
		"default mode on both":   {sticky.Options{}, []string{"relay", "default"}, []string{"lb"}, true},
		"table on http":          {sticky.Options{Mode: sticky.ModeTable}, nil, nil, false},
		"a configured key":       {key, nil, nil, false},
		"default mode on stream": {sticky.Options{}, []string{"relay"}, []string{"lb"}, false},
	} {
		o := test.o
		c := stickyALB(&o, test.listeners...)
		if err := requestALBs(c, sets.New(test.streams)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := warned(c); got != test.want {
			t.Errorf("%s: warned = %v, want %v (%v)", name, got, test.want, c.LoaderWarnings)
		}
	}
}
