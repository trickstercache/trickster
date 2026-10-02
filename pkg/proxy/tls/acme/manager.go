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

// Package acme obtains and renews serving certificates from ACME CAs (RFC 8555) and feeds them
// to the TLS listeners' certificate stores, entirely off the handshake path.
package acme

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	redisopts "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ready"
	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/challenge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/ondemand"

	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"
)

const (
	syncInterval = time.Minute
	startupPoll  = 250 * time.Millisecond

	eventCertObtained = "cert_obtained"
	eventCertFailed   = "cert_failed"
	eventCachedCert   = "cached_managed_cert"
	dataRenewal       = "renewal"
	dataIdentifier    = "identifier"
	dataError         = "error"
	dataCertPath      = "certificate_path"
	dataKeyPath       = "private_key_path"

	resultSuccess = "success"
	resultFailure = "failure"

	logKeyDomain   = "domain"
	logKeyIssuer   = "issuer"
	logKeyDetail   = "detail"
	logKeyListener = "listener"
	logKeyWaited   = "waited"
	logKeyNotAfter = "notAfter"

	eventObtainedText = "acme certificate obtained"
	eventRenewedText  = "acme certificate renewed"
)

// ErrUnmanagedDomain is returned when renewing a name no issuer manages
var ErrUnmanagedDomain = errors.New("no acme issuer manages that domain")

// CertSink receives the ACME-managed certificates each TLS listener serves
type CertSink interface {
	SetACMECerts(listenerName string, entries []*tr.Entry) error
}

// Manager reconciles a configuration's ACME section into managed certificates; Apply, Renew
// and Close are safe for concurrent use.
type Manager struct {
	sink      CertSink
	readiness *ready.State
	log       *zap.Logger
	factory   issuerFactory

	mtx     sync.Mutex
	epoch   *epoch
	issuers map[string]*issuer
	domains map[string]string
	od      *onDemand
	started bool

	table atomic.Pointer[table]
	kick  chan struct{}

	syncMtx       sync.Mutex
	current       map[string]*managedEntry
	pushed        map[string]string
	failures      map[string]string
	onDemandNames map[string]struct{}
}

type epoch struct {
	ctx          context.Context
	cancel       context.CancelFunc
	cache        *certmagic.Cache
	storage      certmagic.Storage
	closeStorage func() error
	storageOpts  *acmeopts.StorageOptions
	redisConn    *redisopts.Options
	done         chan struct{}
}

type table struct {
	configs         map[string]*certmagic.Config
	domainIssuer    map[string]string
	listenerDomains map[string][]string
	issuers         []*issuer
	onDemand        *onDemand
	onDemandIn      []string
}

func (t *table) solvers() []*issuer {
	if t.onDemand == nil {
		return t.issuers
	}
	return append(slices.Clip(t.issuers), t.onDemand.issuer)
}

type managedEntry struct {
	entry  *tr.Entry
	cmHash string
}

// New returns a Manager that feeds sink and, during a startup wait, holds readiness
func New(sink CertSink, readiness *ready.State) *Manager {
	return &Manager{
		sink:      sink,
		readiness: readiness,
		log:       newZapLogger(),
		factory:   newACMEIssuer,
		issuers:   make(map[string]*issuer),
		domains:   make(map[string]string),
		kick:      make(chan struct{}, 1),
		current:   make(map[string]*managedEntry),
		pushed:    make(map[string]string),
		failures:  make(map[string]string),

		onDemandNames: make(map[string]struct{}),
	}
}

// Apply reconciles managed certificates with conf; call it after the listener group and the
// certificate monitor have applied conf, so challenge ports are already held by Trickster
func (m *Manager) Apply(conf *config.Config) (err error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	defer func() {
		// a startup that will never run the wait must not leave readiness held
		if !m.started && (err != nil || m.epoch == nil) {
			m.readiness.SetCertsIssued()
		}
	}()
	if conf == nil || !conf.ACME.IsEnabled() {
		m.teardownLocked()
		return nil
	}
	conn := redisConnection(conf)
	if ep := m.epoch; ep == nil || !ep.storageOpts.Equal(conf.ACME.Storage) || !ep.redisConn.Equal(conn) {
		m.teardownLocked()
		ep, epErr := m.newEpoch(conf.ACME.Storage, conn)
		if epErr != nil {
			return epErr
		}
		m.epoch = ep
	}
	p := newPlan(conf)
	next, recreated, err := m.nextIssuers(conf.ACME, p)
	if err != nil {
		return err
	}
	manage := make(map[string][]string)
	var unmanage []certmagic.SubjectIssuer
	for d, issName := range m.domains {
		if p.domainIssuer[d] != issName || recreated[issName] {
			unmanage = append(unmanage, certmagic.SubjectIssuer{Subject: d})
		}
	}
	for d, issName := range p.domainIssuer {
		if m.domains[d] != issName || recreated[issName] {
			manage[issName] = append(manage[issName], d)
		}
	}
	if err := m.applyOnDemandLocked(conf.ACME, p); err != nil {
		for name, iss := range next {
			if m.issuers[name] != iss {
				iss.cancel()
			}
		}
		return err
	}
	// replaced issuers stop only once the new set is committed, so a failed apply keeps the old one
	for name, old := range m.issuers {
		if next[name] != old {
			old.cancel()
		}
	}
	m.epoch.cache.RemoveManaged(unmanage)
	m.issuers, m.domains = next, p.domainIssuer
	m.publishLocked(p)
	for issName, domains := range manage {
		slices.Sort(domains)
		iss := next[issName]
		if err := iss.cfg.ManageAsync(iss.ctx, domains); err != nil {
			logger.Error("acme certificate management failed to start",
				logging.Pairs{logKeyIssuer: issName, logKeyDetail: err.Error()})
		}
	}
	m.forget(p.domainIssuer)
	challenge.SetSolver(m)
	if !m.started {
		m.started = true
		m.startupWait(time.Duration(conf.ACME.WaitOnStartup), slices.Sorted(maps.Keys(p.domainIssuer)))
	}
	m.kickSync()
	return nil
}

func (m *Manager) nextIssuers(o *acmeopts.Options, p *plan) (map[string]*issuer, map[string]bool, error) {
	next := make(map[string]*issuer, len(p.issuerPorts))
	recreated := make(map[string]bool)
	for _, name := range slices.Sorted(maps.Keys(p.issuerPorts)) {
		opts, ports := o.Issuers[name], p.issuerPorts[name]
		if opts == nil {
			continue
		}
		if old, ok := m.issuers[name]; ok && old.opts.Equal(opts) && old.ports == ports {
			next[name] = old
			continue
		}
		iss, err := m.newIssuer(name, opts, ports, false)
		if err != nil {
			for _, built := range next {
				if m.issuers[built.name] != built {
					built.cancel()
				}
			}
			return nil, nil, fmt.Errorf("acme issuer %q: %w", name, err)
		}
		next[name] = iss
		recreated[name] = true
	}
	return next, recreated, nil
}

func (m *Manager) newIssuer(name string, o *acmeopts.IssuerOptions, ports issuerPorts,
	onDemand bool,
) (*issuer, error) {
	tmpl := certmagic.Config{
		Storage:        m.epoch.storage,
		Logger:         m.log,
		KeySource:      certmagic.StandardKeyGenerator{KeyType: keyTypes[o.KeyType]},
		OnEvent:        m.onEvent(name),
		ShouldEmitFunc: shouldEmit,
	}
	if onDemand {
		tmpl.OnDemand = onDemandConfig(m)
	}
	cfg := certmagic.New(m.epoch.cache, tmpl)
	ci, ai, err := m.factory(cfg, o, ports, m.log)
	if err != nil {
		return nil, err
	}
	cfg.Issuers = []certmagic.Issuer{ci}
	ctx, cancel := context.WithCancel(m.epoch.ctx)
	return &issuer{
		name: name, opts: o.Clone(), ports: ports, cfg: cfg, acme: ai,
		ctx: ctx, cancel: cancel,
	}, nil
}

func (m *Manager) newEpoch(o *acmeopts.StorageOptions, conn *redisopts.Options) (*epoch, error) {
	storage, closeStorage, err := newStorage(o, conn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	ep := &epoch{
		ctx: ctx, cancel: cancel, storage: storage, closeStorage: closeStorage,
		storageOpts: o.Clone(), redisConn: conn, done: make(chan struct{}),
	}
	ep.cache = certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: m.configForCert,
		Logger:           m.log,
	})
	go m.syncLoop(ep)
	return ep, nil
}

func (m *Manager) publishLocked(p *plan) {
	t := &table{
		configs:         make(map[string]*certmagic.Config, len(p.domainIssuer)),
		domainIssuer:    p.domainIssuer,
		listenerDomains: p.listenerDomains,
		issuers:         make([]*issuer, 0, len(m.issuers)),
	}
	for d, issName := range p.domainIssuer {
		if iss := m.issuers[issName]; iss != nil {
			t.configs[d] = iss.cfg
		}
	}
	for _, name := range slices.Sorted(maps.Keys(m.issuers)) {
		t.issuers = append(t.issuers, m.issuers[name])
	}
	if m.od != nil {
		t.onDemand, t.onDemandIn = m.od, p.onDemand
	}
	m.table.Store(t)
}

func (m *Manager) teardownLocked() {
	// requires m.mtx; withdraws every certificate the manager served
	if m.epoch == nil {
		return
	}
	challenge.SetSolver(nil)
	ondemand.SetProvider(nil)
	for _, iss := range m.issuers {
		iss.cancel()
	}
	if m.od != nil {
		m.od.issuer.cancel()
		m.od = nil
	}
	ep := m.epoch
	ep.cancel()
	<-ep.done
	ep.cache.Stop()
	if err := ep.closeStorage(); err != nil {
		logger.Warn("acme storage did not close cleanly", logging.Pairs{logKeyDetail: err.Error()})
	}
	m.epoch = nil
	m.issuers = make(map[string]*issuer)
	m.domains = make(map[string]string)
	m.table.Store(nil)
	m.syncMtx.Lock()
	for ln := range m.pushed {
		_ = m.sink.SetACMECerts(ln, nil)
	}
	m.current = make(map[string]*managedEntry)
	m.pushed = make(map[string]string)
	m.onDemandNames = make(map[string]struct{})
	m.syncMtx.Unlock()
}

// Close stops certificate management and releases storage
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.teardownLocked()
}

// Renew starts a forced renewal of domain in the background
func (m *Manager) Renew(domain string) error {
	t := m.table.Load()
	if t == nil {
		return ErrUnmanagedDomain
	}
	d, err := acmeopts.NormalizeDomain(domain)
	if err != nil {
		return err
	}
	cfg, ok := t.configs[d]
	if !ok {
		return ErrUnmanagedDomain
	}
	m.mtx.Lock()
	iss := m.issuers[t.domainIssuer[d]]
	m.mtx.Unlock()
	if iss == nil {
		return ErrUnmanagedDomain
	}
	return cfg.RenewCertAsync(iss.ctx, d, true)
}

// Domains returns every managed domain with the issuer that manages it
func (m *Manager) Domains() map[string]string {
	if t := m.table.Load(); t != nil {
		return maps.Clone(t.domainIssuer)
	}
	return nil
}

func (m *Manager) configForCert(c certmagic.Certificate) (*certmagic.Config, error) {
	if t := m.table.Load(); t != nil {
		for _, n := range c.Names {
			if cfg, ok := t.configs[n]; ok {
				return cfg, nil
			}
		}
		// anything else in the cache was issued on demand
		if t.onDemand != nil {
			return t.onDemand.issuer.cfg, nil
		}
	}
	return nil, fmt.Errorf("no acme issuer manages %s", strings.Join(c.Names, ","))
}

func shouldEmit(event string) bool {
	return event == eventCertObtained || event == eventCertFailed || event == eventCachedCert
}

func (m *Manager) onEvent(issuerName string) func(context.Context, string, map[string]any) error {
	return func(ctx context.Context, event string, data map[string]any) error {
		domain, _ := data[dataIdentifier].(string)
		renewal, _ := data[dataRenewal].(bool)
		counter := metrics.ACMEOrdersTotal
		if renewal {
			counter = metrics.ACMERenewalsTotal
		}
		switch event {
		case eventCertObtained:
			counter.WithLabelValues(issuerName, resultSuccess).Inc()
			logIssued(issuerName, domain, renewal, m.loadIssued(ctx, issuerName, domain, data))
		case eventCertFailed:
			counter.WithLabelValues(issuerName, resultFailure).Inc()
			if err, ok := data[dataError].(error); ok {
				m.syncMtx.Lock()
				m.failures[domain] = err.Error()
				m.syncMtx.Unlock()
			}
			return nil
		}
		m.kickSync()
		return nil
	}
}

func logIssued(issuerName, domain string, renewal bool, leaf *x509.Certificate) {
	pairs := logging.Pairs{logKeyDomain: domain, logKeyIssuer: issuerName}
	if leaf != nil {
		pairs[logKeyNotAfter] = leaf.NotAfter
	}
	event := eventObtainedText
	if renewal {
		event = eventRenewedText
	}
	logger.Info(event, pairs)
}

func (m *Manager) loadIssued(ctx context.Context, issuerName, domain string,
	data map[string]any,
) *x509.Certificate {
	// the library caches a renewal only after this event, so storage is read now
	certKey, _ := data[dataCertPath].(string)
	keyKey, _ := data[dataKeyPath].(string)
	t := m.table.Load()
	if t == nil || domain == "" || certKey == "" || keyKey == "" {
		return nil
	}
	cfg, onDemand := t.configs[domain], false
	if cfg == nil && t.onDemand != nil {
		cfg, onDemand = t.onDemand.issuer.cfg, true
	}
	if cfg == nil {
		return nil
	}
	certPEM, err := cfg.Storage.Load(ctx, certKey)
	if err != nil {
		return nil
	}
	keyPEM, err := cfg.Storage.Load(ctx, keyKey)
	if err != nil {
		return nil
	}
	cert, err := tr.ValidatePair(certPEM, keyPEM)
	if err != nil {
		logger.Warn("acme certificate in storage failed validation",
			logging.Pairs{logKeyDomain: domain, logKeyDetail: err.Error()})
		return nil
	}
	m.syncMtx.Lock()
	m.offer(domain, entryKey(issuerName, domain), cert, "", true)
	delete(m.failures, domain)
	if onDemand {
		m.onDemandNames[domain] = struct{}{}
	}
	m.syncMtx.Unlock()
	return cert.Leaf
}

func entryKey(issuerName, domain string) string {
	return tr.SourceKindACME + ":" + issuerName + ":" + domain
}

func (m *Manager) offer(domain, key string, cert tls.Certificate, cmHash string, issued bool) {
	// requires syncMtx; a just-issued cert wins a NotBefore tie, as a quick renewal can share one
	cur := m.current[domain]
	if cur != nil && cmHash != "" && cur.cmHash == cmHash {
		return
	}
	if cur != nil && cur.entry.Key == key {
		held := cur.entry.Certificate.Leaf
		if issued && held.NotBefore.After(cert.Leaf.NotBefore) ||
			!issued && !newer(cert.Leaf, held) {
			return
		}
	}
	m.current[domain] = &managedEntry{entry: tr.NewEntry(key, tr.SourceKindACME, cert), cmHash: cmHash}
}

func newer(a, b *x509.Certificate) bool {
	if !a.NotBefore.Equal(b.NotBefore) {
		return a.NotBefore.After(b.NotBefore)
	}
	return a.NotAfter.After(b.NotAfter)
}

func (m *Manager) forget(desired map[string]string) {
	m.syncMtx.Lock()
	defer m.syncMtx.Unlock()
	for d := range m.current {
		_, onDemand := m.onDemandNames[d]
		if _, ok := desired[d]; !ok && !onDemand {
			delete(m.current, d)
			delete(m.failures, d)
		}
	}
}

func (m *Manager) kickSync() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) syncLoop(ep *epoch) {
	defer close(ep.done)
	// beyond the events, a periodic sync picks up renewals another cluster member performed
	t := time.NewTicker(syncInterval)
	defer t.Stop()
	for {
		select {
		case <-ep.ctx.Done():
			return
		case <-t.C:
		case <-m.kick:
		}
		m.sync(ep.cache)
	}
}

func (m *Manager) sync(cache *certmagic.Cache) {
	t := m.table.Load()
	if t == nil {
		return
	}
	m.syncMtx.Lock()
	defer m.syncMtx.Unlock()
	for d, issName := range t.domainIssuer {
		m.offerCached(cache, d, entryKey(issName, d))
	}
	var onDemand []string
	if t.onDemand != nil {
		onDemand = slices.Sorted(maps.Keys(m.onDemandNames))
		for _, d := range onDemand {
			m.offerCached(cache, d, entryKey(t.onDemand.issuer.name, d))
		}
	}
	served := make(map[string]struct{}, len(t.listenerDomains)+len(t.onDemandIn))
	for ln, domains := range t.listenerDomains {
		served[ln] = struct{}{}
		if slices.Contains(t.onDemandIn, ln) {
			domains = append(slices.Clip(domains), onDemand...)
		}
		m.push(ln, domains)
	}
	for _, ln := range t.onDemandIn {
		if _, ok := served[ln]; !ok {
			served[ln] = struct{}{}
			m.push(ln, onDemand)
		}
	}
	for ln := range m.pushed {
		if _, ok := served[ln]; !ok {
			_ = m.sink.SetACMECerts(ln, nil)
			delete(m.pushed, ln)
		}
	}
}

func (m *Manager) offerCached(cache *certmagic.Cache, d, key string) {
	// requires syncMtx
	for _, c := range cache.AllMatchingCertificates(d) {
		if c.Leaf != nil && slices.Contains(c.Names, d) {
			m.offer(d, key, c.Certificate, c.Hash(), false)
		}
	}
}

func (m *Manager) push(ln string, domains []string) {
	// requires syncMtx; a listener is pushed to only when its certificates changed
	entries := make([]*tr.Entry, 0, len(domains))
	var fp strings.Builder
	for _, d := range domains {
		if cur := m.current[d]; cur != nil {
			entries = append(entries, cur.entry)
			fp.WriteString(cur.entry.Key + "=" + cur.entry.ContentHash + ";")
		}
	}
	if prev, ok := m.pushed[ln]; ok && prev == fp.String() {
		return
	}
	if err := m.sink.SetACMECerts(ln, entries); err != nil {
		// the listener has not reached the monitor yet; the next sync retries
		return
	}
	m.pushed[ln] = fp.String()
}

func (m *Manager) missing(domains []string) []string {
	now := time.Now()
	m.syncMtx.Lock()
	defer m.syncMtx.Unlock()
	var out []string
	for _, d := range domains {
		cur := m.current[d]
		if cur == nil || !now.Before(cur.entry.Certificate.Leaf.NotAfter) {
			out = append(out, d)
		}
	}
	return out
}

func (m *Manager) startupWait(wait time.Duration, domains []string) {
	if m.readiness == nil {
		return
	}
	if wait <= 0 || len(domains) == 0 {
		m.readiness.SetCertsIssued()
		return
	}
	m.readiness.SetCertsPending()
	start := time.Now()
	go func() {
		defer m.readiness.SetCertsIssued()
		deadline := time.NewTimer(wait)
		defer deadline.Stop()
		poll := time.NewTicker(startupPoll)
		defer poll.Stop()
		for {
			select {
			case <-deadline.C:
				metrics.ACMEStartupWaitTimeoutsTotal.Inc()
				metrics.ACMEStartupWaitSeconds.Set(wait.Seconds())
				m.logMissing(m.missing(domains))
				return
			case <-poll.C:
				if len(m.missing(domains)) == 0 {
					waited := time.Since(start)
					metrics.ACMEStartupWaitSeconds.Set(waited.Seconds())
					logger.Info("acme startup certificates issued; readiness released",
						logging.Pairs{logKeyWaited: waited.String()})
					return
				}
			}
		}
	}()
}

func (m *Manager) logMissing(domains []string) {
	t := m.table.Load()
	m.syncMtx.Lock()
	defer m.syncMtx.Unlock()
	for _, d := range domains {
		pairs := logging.Pairs{logKeyDomain: d}
		if t != nil {
			pairs[logKeyIssuer] = t.domainIssuer[d]
		}
		if f := m.failures[d]; f != "" {
			pairs[logKeyDetail] = f
		}
		logger.Error("acme startup wait elapsed before a certificate was issued; "+
			"readiness released and issuance continues", pairs)
	}
}

// ServeHTTPChallenge answers a pending http-01 challenge for any issuer sharing this storage
func (m *Manager) ServeHTTPChallenge(w http.ResponseWriter, r *http.Request) bool {
	t := m.table.Load()
	if t == nil {
		return false
	}
	for _, iss := range t.solvers() {
		if iss.acme != nil && iss.acme.HandleHTTPChallenge(w, r) {
			return true
		}
	}
	return false
}

// TLSALPNCertificate returns the validation certificate for a pending tls-alpn-01 challenge
func (m *Manager) TLSALPNCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	t := m.table.Load()
	if t == nil {
		return nil, ErrUnmanagedDomain
	}
	lastErr := ErrUnmanagedDomain
	for _, iss := range t.solvers() {
		if iss.acme == nil || !iss.opts.HasChallenge(acmeopts.ChallengeTLSALPN01) {
			continue
		}
		cert, err := iss.cfg.GetCertificate(hello)
		if err == nil && cert != nil {
			return cert, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
