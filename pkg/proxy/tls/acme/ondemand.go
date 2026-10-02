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
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"hash/maphash"
	"net/http"
	"net/url"
	"time"

	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/ondemand"
	"github.com/trickstercache/trickster/v2/pkg/util/keytable"

	"github.com/caddyserver/certmagic"
	"golang.org/x/time/rate"
)

const (
	askTimeout = 10 * time.Second
	askParam   = "domain"

	decisionAllowed     = "allowed"
	decisionRefused     = "refused"
	decisionRefusedHeld = "refused_cached"
	decisionRateLimited = "rate_limited"
	decisionAskError    = "ask_error"
)

var (
	errOnDemandOff    = errors.New("on-demand issuance is not enabled")
	errRefused        = errors.New("on-demand issuance refused for this name")
	errRateLimited    = errors.New("on-demand issuance rate limit reached")
	errAskUnavailable = errors.New("on-demand ask endpoint did not answer")
)

type onDemand struct {
	opts      *acmeopts.OnDemandOptions
	issuer    *issuer
	listeners map[string]string
	refused   *keytable.Table[string]
	approved  *keytable.Table[string]
	limiter   *rate.Limiter
	client    *http.Client
	seed      maphash.Seed
}

func newOnDemand(o *acmeopts.OnDemandOptions, iss *issuer, listenerKeys map[string]string) *onDemand {
	ttl := time.Duration(o.DecisionTTL)
	perMinute := rate.Limit(float64(o.RateLimit) / time.Minute.Seconds())
	return &onDemand{
		opts:      o.Clone(),
		issuer:    iss,
		listeners: listenerKeys,
		refused:   keytable.New[string](keytable.Options{TTL: ttl, MaxEntries: o.NegativeCacheSize}),
		approved:  keytable.New[string](keytable.Options{TTL: ttl, MaxEntries: o.NegativeCacheSize}),
		limiter:   rate.NewLimiter(perMinute, max(o.RateLimit, 1)),
		client:    &http.Client{Timeout: askTimeout},
		seed:      maphash.MakeSeed(),
	}
}

// Enabled reports whether the listener with the given group key issues on demand
func (m *Manager) Enabled(listenerKey string) bool {
	t := m.table.Load()
	if t == nil || t.onDemand == nil {
		return false
	}
	_, ok := t.onDemand.listeners[listenerKey]
	return ok
}

// Certificate obtains or loads a certificate for a handshake whose name no stored certificate covers
func (m *Manager) Certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	t := m.table.Load()
	if t == nil || t.onDemand == nil {
		return nil, errOnDemandOff
	}
	cert, err := t.onDemand.issuer.cfg.GetCertificate(hello)
	if err != nil || cert == nil {
		return nil, err
	}
	if name, err := hostnames.Normalize(hello.ServerName, hostnames.RequirePrecise); err == nil {
		m.syncMtx.Lock()
		m.onDemandNames[name] = struct{}{}
		m.syncMtx.Unlock()
		m.kickSync()
	}
	return cert, nil
}

func (m *Manager) decide(ctx context.Context, name string) error {
	// refusals are remembered, every configured gate must pass, and starts are rate limited
	t := m.table.Load()
	if t == nil || t.onDemand == nil {
		return errOnDemandOff
	}
	od := t.onDemand
	nowNano := time.Now().UnixNano()
	key := maphash.String(od.seed, name)
	if v, ok := od.refused.Get(key, nowNano); ok && v == name {
		metrics.ACMEOnDemandDecisionsTotal.WithLabelValues(decisionRefusedHeld).Inc()
		return errRefused
	}
	if len(od.opts.AllowedDomains) > 0 && !allowedDomain(od.opts.AllowedDomains, name) {
		return od.refuse(key, name, nowNano)
	}
	if od.opts.Ask != "" {
		if v, ok := od.approved.Get(key, nowNano); !ok || v != name {
			ok, err := od.ask(ctx, name)
			if err != nil {
				metrics.ACMEOnDemandDecisionsTotal.WithLabelValues(decisionAskError).Inc()
				return err
			}
			if !ok {
				return od.refuse(key, name, nowNano)
			}
			od.approved.Put(key, name, nowNano)
		}
	}
	if !od.limiter.Allow() {
		metrics.ACMEOnDemandDecisionsTotal.WithLabelValues(decisionRateLimited).Inc()
		return errRateLimited
	}
	metrics.ACMEOnDemandDecisionsTotal.WithLabelValues(decisionAllowed).Inc()
	return nil
}

func (od *onDemand) refuse(key uint64, name string, nowNano int64) error {
	od.refused.Put(key, name, nowNano)
	metrics.ACMEOnDemandDecisionsTotal.WithLabelValues(decisionRefused).Inc()
	return errRefused
}

func (od *onDemand) ask(ctx context.Context, name string) (bool, error) {
	u, err := url.Parse(od.opts.Ask)
	if err != nil {
		return false, err
	}
	q := u.Query()
	q.Set(askParam, name)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, err
	}
	resp, err := od.client.Do(req)
	if err != nil {
		return false, errors.Join(errAskUnavailable, err)
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

func allowedDomain(allowed []string, name string) bool {
	for _, a := range allowed {
		if a == name || hostnames.IsWildcard(a) && hostnames.Overlap(a, name) {
			return true
		}
	}
	return false
}

func onDemandConfig(m *Manager) *certmagic.OnDemandConfig {
	return &certmagic.OnDemandConfig{DecisionFunc: m.decide}
}

func (m *Manager) applyOnDemandLocked(o *acmeopts.Options, p *plan) error {
	// requires m.mtx
	odOpts := o.OnDemand
	if odOpts == nil || len(p.onDemand) == 0 {
		m.retireOnDemandLocked()
		return nil
	}
	issOpts, ports := o.Issuers[odOpts.Issuer], p.issuerPorts[odOpts.Issuer]
	keys := make(map[string]string, len(p.onDemand))
	for _, ln := range p.onDemand {
		keys[listener.GroupKey(ln, listenerconfig.ProtocolHTTP, true)] = ln
	}
	if cur := m.od; cur != nil && cur.opts.Equal(odOpts) && cur.issuer.opts.Equal(issOpts) &&
		cur.issuer.ports == ports {
		// decisions, refusals and the rate limiter carry over an unchanged configuration
		next := *cur
		next.listeners = keys
		m.od = &next
		ondemand.SetProvider(m)
		return nil
	}
	iss, err := m.newIssuer(odOpts.Issuer, issOpts, ports, true)
	if err != nil {
		return fmt.Errorf("acme on-demand issuer: %w", err)
	}
	if m.od != nil {
		m.od.issuer.cancel()
	}
	m.od = newOnDemand(odOpts, iss, keys)
	ondemand.SetProvider(m)
	return nil
}

func (m *Manager) retireOnDemandLocked() {
	// requires m.mtx
	if m.od == nil {
		return
	}
	m.od.issuer.cancel()
	m.od = nil
	ondemand.SetProvider(nil)
	m.syncMtx.Lock()
	names := make([]certmagic.SubjectIssuer, 0, len(m.onDemandNames))
	for n := range m.onDemandNames {
		names = append(names, certmagic.SubjectIssuer{Subject: n})
		delete(m.current, n)
	}
	m.onDemandNames = make(map[string]struct{})
	m.syncMtx.Unlock()
	if m.epoch != nil {
		m.epoch.cache.RemoveManaged(names)
	}
}
