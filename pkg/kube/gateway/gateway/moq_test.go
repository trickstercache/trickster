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

package gateway

import (
	"path/filepath"
	"testing"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/class"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func translateWith(t *testing.T, name string, edit func(*cache), mutate ...func(*kubecfg.Options),
) (*ir.IR, []Problem) {
	t.Helper()
	c := load(t, filepath.Join("testdata", name+".yaml"))
	if edit != nil {
		edit(c)
	}
	idx := policyIndex(c)
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t, mutate...), KnownNames: known,
		Policies: idx,
	})
	return model, append(problems, idx.Problems()...)
}

func TestTranslateMoQ(t *testing.T) {
	problems := golden(t, "moq")
	containing(t, problems, `listener "no-tls"`, "requires a tls block")
	containing(t, problems, `listener "passthrough"`, "tls mode")
	containing(t, problems, `listener "udp-clash"`, "port 4443 already serves moq")
	containing(t, problems, "UDPRoute/media/any", "backendRef 0")
	containing(t, problems, "UDPRoute/media/late", "already served")

	model, _, _ := translateFixture(t, "moq")
	byName := make(map[string]ir.Route)
	for _, r := range model.Routes {
		byName[r.Source.Name] = r
	}
	live, vod := byName["live"], byName["vod"]
	require.Equal(t, ir.ProtocolMoQ, live.Protocol)
	require.Equal(t, []string{"live.example.com"}, live.Hostnames, "the listener's hostname is the route's")
	require.Equal(t, []string{"**.vod.example.com"}, vod.Hostnames, "a wildcard is served for any depth")
	for _, l := range model.Listeners {
		if l.Section == "live" {
			require.NotEmpty(t, l.CertRefs, "a moq listener serves its certificates")
		}
	}
	for _, g := range model.Backends {
		for _, m := range g.Members {
			if !m.Invalid {
				require.Equal(t, schemeMoQT, m.Service.Scheme, "a MoQ origin is dialed by native QUIC")
			}
		}
	}
}

func TestTranslateMoQWebTransport(t *testing.T) {
	problems := golden(t, "moq-webtransport")
	const needsHTTP3 = "provider moq needs an https listener serving HTTP/3"
	containing(t, problems, "HTTPRoute/media/plain", needsHTTP3)
	containing(t, problems, "HTTPRoute/media/legacy", needsHTTP3)
	containing(t, problems, "HTTPRoute/media/mixed", "mix moq origins with others")
	containing(t, problems, "HTTPRoute/media/tcp-origin", "has no udp port 8080")
	containing(t, problems, "TricksterCachePolicy/media/live-relay", "spec.timeout is ignored for provider moq")
	containing(t, problems, "HTTPRoute/media/vod", "filters, timeouts, retry and session persistence", "ignored")
	require.Len(t, problems, 6, "%v", details(problems))

	o := options(t)
	c := load(t, filepath.Join("testdata", "moq-webtransport.yaml"))
	model, report, _ := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: o, KnownNames: known, Policies: policyIndex(c),
	})
	for _, l := range model.Listeners {
		if l.Protocol == ir.ProtocolHTTPS {
			require.Equal(t, l.Port == 443, l.HTTP3, "the class decides HTTP/3 for %s", l.Name)
		}
	}
	for _, g := range model.Backends {
		for _, m := range g.Members {
			if !m.Invalid && g.Source.Kind == ir.KindHTTPRoute {
				require.Equal(t, "h3", m.Service.PortName, "a MoQ origin is reached on its UDP port")
			}
		}
	}
	refused := map[string]bool{}
	for _, r := range report.Routes {
		for _, p := range r.Parents {
			for _, cond := range p.Conditions {
				if cond.Type == string(gwapiv1.RouteConditionResolvedRefs) && !cond.Status &&
					cond.Reason == string(gwapiv1.RouteReasonUnsupportedProtocol) {
					refused[r.Source.Name] = true
				}
			}
		}
	}
	require.Equal(t, map[string]bool{"plain": true, "legacy": true, "mixed": true}, refused)
}

func TestTranslateHTTP3Parameter(t *testing.T) {
	// the oldest Gateway's class decides HTTP/3 for a shared port, and the configured default covers a class naming none
	_, problems := translateWith(t, "moq-webtransport", func(c *cache) {
		for _, gw := range c.gateways {
			if gw.Name == "legacy" {
				gw.Spec.Listeners[0].Port = 443
				gw.Spec.Listeners[0].Hostname = new(gwapiv1.Hostname("legacy.example.com"))
			}
		}
	})
	containing(t, problems, "Gateway/infra/legacy", "the http3 parameter is not applied", "port 443")

	https := func(model *ir.IR) (on, all int) {
		for _, l := range model.Listeners {
			if l.Protocol == ir.ProtocolHTTPS {
				all++
				if l.HTTP3 {
					on++
				}
			}
		}
		return on, all
	}
	model, _ := translateWith(t, "listeners", nil)
	if on, all := https(model); all == 0 || on != all {
		t.Errorf("https listeners serve HTTP/3 by default: %d of %d", on, all)
	}
	off := false
	model, _ = translateWith(t, "listeners", nil, func(o *kubecfg.Options) { o.Defaults.HTTP3 = &off })
	if on, all := https(model); all == 0 || on != 0 {
		t.Errorf("the configured default turns HTTP/3 off: %d of %d still serve it", on, all)
	}
}

func TestTranslateMoQPolicyOnUDPListener(t *testing.T) {
	// a plain UDP listener relays datagrams, so a moq policy on a route it serves is reported rather than applied
	_, problems := translateWith(t, "tcp", func(c *cache) {
		c.cachePols = append(c.cachePols, decodePolicy(t, `apiVersion: trickstercache.org/v1alpha1
kind: TricksterCachePolicy
metadata:
  name: relay
  namespace: data
spec:
  targetRefs:
    - kind: UDPRoute
      name: dns
  provider: moq
`))
	})
	containing(t, problems, "UDPRoute/data/dns", "provider moq is not applied", "only a trickstercache.org/moq listener")
}

func TestTranslateMoQAuthenticator(t *testing.T) {
	// a MoQ session carries no HTTP credentials, so a class with an authenticator refuses its MoQ routes rather
	// than serving them unauthenticated
	model, problems, _ := translateFixture(t, "moq", func(o *kubecfg.Options) { o.Defaults.AuthenticatorName = "gateway-auth" })
	for _, r := range model.Routes {
		require.NotEqual(t, ir.ProtocolMoQ, r.Protocol, "route %s is served", r.Source.Name)
	}
	containing(t, problems, "UDPRoute/media/live", `authenticator "gateway-auth" judges HTTP requests only`)
}
