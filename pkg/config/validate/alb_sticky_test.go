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

// initializedSticky parses a sticky block as config loading would
func initializedSticky(t *testing.T, o sticky.Options) *sticky.Options {
	t.Helper()
	if err := o.Initialize(); err != nil {
		t.Fatal(err)
	}
	return &o
}

// an ALB's sticky block must suit every listener it serves: tokens need an http listener, and a
// table's key must be one each listener can read
func TestStickySuitsEveryListener(t *testing.T) {
	web := func(c *config.Config) {
		c.Listeners["web"] = listener.New("web")
		c.Listeners["web"].ListenPort = 18480
	}
	relay := func(c *config.Config) {
		c.Listeners["relay"] = listener.New("relay")
		c.Listeners["relay"].Protocol = listener.ProtocolTLS
		c.Listeners["relay"].ListenPort = 9443
	}
	for name, test := range map[string]struct {
		o         sticky.Options
		listeners []string
		want      string
	}{
		"default mode on both":    {sticky.Options{}, []string{"relay", "web"}, ""},
		"sni table on tls":        {sticky.Options{Table: sticky.TableOptions{Key: "sni"}}, []string{"relay"}, ""},
		"header table on http":    {sticky.Options{Mode: sticky.ModeTable, Table: sticky.TableOptions{Key: "header:X-Client"}}, []string{"web"}, ""},
		"cookie on a tls relay":   {sticky.Options{Mode: sticky.ModeCookie}, []string{"relay", "web"}, "cannot carry the tokens of alb backend \"lb\"'s sticky.mode \"cookie\""},
		"header with no http":     {sticky.Options{Mode: sticky.ModeHeader}, []string{"relay"}, "cannot carry the tokens"},
		"header table on tls":     {sticky.Options{Table: sticky.TableOptions{Key: "header:X-Client"}}, []string{"relay"}, "cannot read alb backend \"lb\"'s sticky.table.key \"header:X-Client\""},
		"sni table on http":       {sticky.Options{Mode: sticky.ModeTable, Table: sticky.TableOptions{Key: "sni"}}, []string{"relay", "web"}, "sticky.table.key \"sni\" cannot be read from a request, which http listener \"web\" serves"},
		"sni table, http default": {sticky.Options{Table: sticky.TableOptions{Key: "sni"}}, []string{"relay", "web"}, ""},
	} {
		c := config.NewConfig()
		web(c)
		relay(c)
		lb := bo.New()
		lb.Provider = providers.ALB
		lb.ListenerNames = test.listeners
		lb.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.Members("m1"), Sticky: initializedSticky(t, test.o)}
		member := bo.New()
		member.Provider = providers.ReverseProxyShort
		member.OriginURL = "tcp://member.example.com:9000"
		c.Backends = bo.Lookup{"lb": lb, "m1": member}
		err := Listeners(c)
		if (test.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), test.want)) {
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
	}
	// an ALB whose only listener is a stream one cannot issue tokens, whatever else it serves
	c := config.NewConfig()
	relay(c)
	lb := bo.New()
	lb.Provider = providers.ALB
	lb.ListenerNames = []string{"relay"}
	lb.ALBOptions = &ao.Options{MechanismName: "rr", Sticky: initializedSticky(t, sticky.Options{Mode: sticky.ModeCookie})}
	c.Backends = bo.Lookup{"lb": lb}
	if err := requestALBs(c, sets.New([]string{"lb"})); err == nil ||
		!strings.Contains(err.Error(), "only an http listener carries, and it serves none") {
		t.Errorf("error = %v", err)
	}
}

func TestStickyOnNativeListeners(t *testing.T) {
	for name, test := range map[string]struct {
		o    sticky.Options
		want string
	}{
		"user table":   {sticky.Options{Table: sticky.TableOptions{Key: "user"}}, ""},
		"default":      {sticky.Options{}, ""},
		"host table":   {sticky.Options{Table: sticky.TableOptions{Key: "host"}}, "cannot read alb backend \"replicas\"'s sticky.table.key \"host\""},
		"cookie token": {sticky.Options{Mode: sticky.ModeCookie}, "cannot carry the tokens"},
	} {
		c := replicaConfig("rr")
		c.Backends["replicas"].ALBOptions.Sticky = initializedSticky(t, test.o)
		err := Listeners(c)
		if (test.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), test.want)) {
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
	}
}

// two ALBs that set one cookie on one listener would each replace the other's token in a browser
func TestStickyCookiesAreDistinctPerListener(t *testing.T) {
	build := func(second sticky.Options, listeners ...string) *config.Config {
		c := config.NewConfig()
		c.Listeners["web2"] = listener.New("web2")
		c.Listeners["web2"].ListenPort = 18481
		c.Backends = bo.Lookup{}
		for name, o := range map[string]sticky.Options{"first": {}, "second": second} {
			b := bo.New()
			b.Provider = providers.ALB
			if name == "second" {
				b.ListenerNames = listeners
			}
			b.ALBOptions = &ao.Options{MechanismName: "rr", Sticky: initializedSticky(t, o)}
			c.Backends[name] = b
		}
		return c
	}
	for name, test := range map[string]struct {
		second    sticky.Options
		listeners []string
		want      string
	}{
		"same cookie":    {sticky.Options{}, nil, `alb backends "first" and "second" both set sticky cookie "trickster_sticky" on http listener "default"`},
		"own name":       {sticky.Options{Cookie: sticky.CookieOptions{Name: "second"}}, nil, ""},
		"own path":       {sticky.Options{Cookie: sticky.CookieOptions{Path: "/second"}}, nil, ""},
		"other listener": {sticky.Options{}, []string{"web2"}, ""},
		"header mode":    {sticky.Options{Mode: sticky.ModeHeader}, nil, ""},
		"table mode":     {sticky.Options{Mode: sticky.ModeTable}, nil, ""},
	} {
		err := requestALBs(build(test.second, test.listeners...), sets.New[string](nil))
		if (test.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), test.want)) {
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
	}
}
