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

package compile

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
)

// memberTarget is what a rule reaches for one member: the front its paths register on and the
// origin the engine reads; one backend in service mode, a discovery ALB over a template in endpoint mode
type memberTarget struct {
	front        *backendDoc
	frontHandler string
	// origin and originName are set only when the origin is a template
	// distinct from the front
	origin     *backendDoc
	originName string
}

func newMemberTarget(doc *document, g ir.BackendGroup, m ir.BackendMember,
	eff effective, opts *kubecfg.Options,
) (*memberTarget, error) {
	origin := originBackend(m, eff)
	switch eff.routingMode {
	case kubecfg.RoutingModeService:
		return &memberTarget{front: origin, frontHandler: eff.handler()}, nil
	case kubecfg.RoutingModeEndpoint:
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedRoutingMode, eff.routingMode)
	}
	// the Service's ready endpoints are load balanced directly, so the origin becomes the template
	// each is cloned from: every setting but the address, which the discovered member supplies
	origin.OriginURL = ""
	origin.IsTemplate = true
	origin.PathRoutingDisabled = true
	origin.PathDefaultsDisabled = true
	origin.CacheKeyPrefix = CacheKeyPrefix(g.Source)
	if eff.healthMode == ao.HealthModeProbe {
		origin.HealthCheck = eff.probeHealthCheck()
	}
	tmplName := TemplateName(g.Source, g.RuleIndex, m.RefIndex)
	query := &queryDoc{
		Kind: do.KindEndpointSlices, Namespace: m.Service.Namespace,
		Service: m.Service.Name, Port: m.Service.PortName,
	}
	if m.Service.Scheme == ir.ProtocolHTTPS {
		query.Scheme = do.SchemeHTTPS
	}
	front := &backendDoc{
		Provider: providers.ALB,
		ALB: eff.endpointALB(&albDiscoveryDoc{
			DiscovererName:  doc.discoverer(opts),
			TemplateBackend: tmplName,
			HealthMode:      eff.healthMode,
			Query:           query,
		}, ao.KeySource.OnHTTP),
	}
	return &memberTarget{
		front: front, frontHandler: providers.ALB,
		origin: origin, originName: tmplName,
	}, nil
}

func baseBackend(provider string, eff effective) *backendDoc {
	return &backendDoc{
		Provider:          provider,
		AccessLog:         eff.accessLogFor(),
		TracingConfigName: eff.tracingName,
		ReqRewriterName:   eff.rewriterName,
		AuthenticatorName: eff.authenticatorName,
	}
}

func originBackend(m ir.BackendMember, eff effective) *backendDoc {
	// the endpoint routing mode turns it into a template
	b := baseBackend(eff.provider(), eff)
	b.OriginURL = ServiceURL(m.Service)
	// the Gateway API and Ingress both forward the client's Host; a URLRewrite hostname replaces it
	b.PreserveHost = true
	if m.TLS != nil {
		// the policy's hostname is the SNI and verification name while the connection still goes to
		// the Service; a CA bundle is the whole of the trust, chosen instead of the well-known roots
		b.TLS = &tlsDoc{ServerName: m.TLS.Hostname}
		if !m.TLS.System {
			b.TLS.CertificateAuthorityPEM = strings.Join(m.TLS.CACertificates, "\n")
			b.TLS.ExcludeSystemRoots = true
		}
	}
	if eff.caches() {
		// a non-cache provider ignores these, so emitting them there would
		// only describe behavior the backend does not have
		b.CacheName = eff.cacheName
		b.NegativeCacheName = eff.negativeCacheName
		if eff.policy != nil && eff.policy.MaxTTLMS > 0 {
			b.MaxTTL = (time.Duration(eff.policy.MaxTTLMS) *
				time.Millisecond).String()
		}
	}
	if eff.timeout > 0 {
		b.Timeout = eff.timeout.String()
	}
	// gRPC needs HTTP/2, which a plaintext origin only speaks by prior knowledge, as does a
	// Service port whose appProtocol says it serves h2c
	b.H2CPriorKnowledge = (eff.grpc || m.Service.H2C) && m.Service.Scheme != ir.ProtocolHTTPS
	return b
}

func mirrorTarget(doc *document, out map[string]*backendDoc, name string, g ir.BackendGroup,
	mf *ir.MirrorFilter, eff effective, opts *kubecfg.Options, listeners []string,
) (*mirrorDoc, error) {
	// the Service is reached as a member is, through the proxy handler so the copies cache
	// nothing, and registered on no listener
	eff.handlerName = handlerProxy
	eff.tsProvider = ""
	m := ir.BackendMember{Service: mf.Service, TLS: mf.TLS}
	t, err := newMemberTarget(doc, g, m, eff, opts)
	if err != nil {
		return nil, err
	}
	t.front.ListenerNames = listeners
	t.front.PathRoutingDisabled = true
	t.front.PathDefaultsDisabled = true
	t.front.Paths = catchAllPaths(t.frontHandler, "", nil)
	if t.origin != nil {
		// the template is named for the mirror, since several mirrors share a group
		tmplName := name + templateSuffix
		t.origin.Paths = catchAllPaths(handlerProxy, "", nil)
		out[tmplName] = t.origin
		t.front.ALB.Discovery.TemplateBackend = tmplName
	}
	out[name] = t.front
	return &mirrorDoc{BackendName: name, Percent: mf.Percent}, nil
}

// placeholderBackend binds a listener with no route: a reverse-proxy backend that is never
// dialed, whose one path answers 404 for whatever host and path arrive
func placeholderBackend(listener string) *backendDoc {
	body := "no route"
	return &backendDoc{
		Provider:             providers.ReverseProxyShort,
		OriginURL:            unresolvedOriginURL,
		ListenerNames:        []string{listener},
		PathRoutingDisabled:  true,
		PathDefaultsDisabled: true,
		Paths: []*pathDoc{{
			Path:         "/",
			MatchType:    string(matching.PathMatchNamePrefix),
			Handler:      handlerLocalResponse,
			Methods:      anyMethod,
			ResponseCode: 404,
			ResponseBody: &body,
		}},
	}
}

func invalidBackend() *backendDoc {
	// it is a reverse-proxy backend carrying a localresponse path rather than a
	// provider of its own; localresponse is a path handler.
	body := "unresolved backend reference"
	return &backendDoc{
		Provider:             providers.ReverseProxyShort,
		OriginURL:            unresolvedOriginURL,
		PathRoutingDisabled:  true,
		PathDefaultsDisabled: true,
		Paths: []*pathDoc{{
			Path:         "/",
			MatchType:    string(matching.PathMatchNamePrefix),
			Handler:      handlerLocalResponse,
			Methods:      anyMethod,
			ResponseCode: 500,
			ResponseBody: &body,
		}},
	}
}

// ServiceURL is the in-cluster URL of a Service port; a stream member's scheme is tcp or udp, and
// the listener relaying to it dials the host and port alone
func ServiceURL(s ir.ServiceTarget) string {
	scheme := s.Scheme
	if scheme == "" {
		scheme = "http"
	}
	u := url.URL{
		Scheme: scheme,
		Host: fmt.Sprintf("%s.%s.%s:%d",
			s.Name, s.Namespace, clusterDomain, s.Port),
	}
	return u.String()
}
