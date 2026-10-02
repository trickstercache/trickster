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

package acme

import (
	"maps"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
)

type issuerPorts struct {
	host string
	http int
	tls  int
}

type plan struct {
	domainIssuer    map[string]string
	listenerDomains map[string][]string
	issuerPorts     map[string]issuerPorts
	onDemand        []string
}

func newPlan(conf *config.Config) *plan {
	p := &plan{
		domainIssuer:    make(map[string]string),
		listenerDomains: make(map[string][]string),
		issuerPorts:     make(map[string]issuerPorts),
	}
	if conf == nil || !conf.ACME.IsEnabled() {
		return p
	}
	for _, name := range slices.Sorted(maps.Keys(conf.Backends)) {
		b := conf.Backends[name]
		if b == nil || b.IsTemplate || b.TLS == nil || b.TLS.ACME == nil {
			continue
		}
		o := b.TLS.ACME
		domains := o.ResolvedDomains
		if len(domains) == 0 {
			var err error
			// validation resolves domains first, so an error here leaves the backend unmanaged
			if domains, err = o.ResolveDomains(b.Hosts); err != nil {
				continue
			}
		}
		for _, d := range domains {
			p.domainIssuer[d] = o.Issuer
		}
		for _, ln := range b.ListenerNames {
			lo := conf.Listeners[ln]
			if !servesTLS(lo) {
				continue
			}
			p.listenerDomains[ln] = append(p.listenerDomains[ln], domains...)
			p.addPorts(o.Issuer, lo)
		}
	}
	if od := conf.ACME.OnDemand; od != nil {
		for _, ln := range od.Listeners {
			lo := conf.Listeners[ln]
			if !servesTLS(lo) {
				continue
			}
			p.onDemand = append(p.onDemand, ln)
			p.addPorts(od.Issuer, lo)
		}
		slices.Sort(p.onDemand)
		p.onDemand = slices.Compact(p.onDemand)
	}
	for ln, domains := range p.listenerDomains {
		slices.Sort(domains)
		p.listenerDomains[ln] = slices.Compact(domains)
	}
	return p
}

func (p *plan) addPorts(issuer string, lo *listenerconfig.Options) {
	// the first ports seen are the ones the challenge solvers expect a listener to hold
	ports, seen := p.issuerPorts[issuer]
	if !seen && (lo.ListenPort == 0 || lo.ListenAddress == lo.TLSListenAddress) {
		ports.host = lo.TLSListenAddress
	}
	if ports.http == 0 {
		ports.http = lo.ListenPort
	}
	if ports.tls == 0 {
		ports.tls = lo.TLSListenPort
	}
	p.issuerPorts[issuer] = ports
}

func servesTLS(lo *listenerconfig.Options) bool {
	return lo != nil && lo.ServeTLS && lo.TLSListenPort > 0 &&
		(lo.Protocol == "" || lo.Protocol == listenerconfig.ProtocolHTTP)
}
