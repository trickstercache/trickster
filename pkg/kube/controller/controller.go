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

// Package controller runs the Kubernetes Gateway/Ingress controller: it watches, translates,
// compiles an overlay for the daemon's reload path, pushes certificates and writes status
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/events"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/class"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/compile"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/gateway"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ingress"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/kube/gatewayapi"
	"github.com/trickstercache/trickster/v2/pkg/kube/leader"
	"github.com/trickstercache/trickster/v2/pkg/kube/status"
	"github.com/trickstercache/trickster/v2/pkg/kube/watch"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/dynamic"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
)

var (
	// ErrNoOptions indicates New was called without a kubernetes section
	ErrNoOptions = errors.New("no kubernetes options provided")
	// ErrNoPublisher indicates New was called with nowhere to send an overlay
	ErrNoPublisher = errors.New("no overlay publisher provided")
	// ErrRetired is returned by a Publisher when the controller offering the
	// overlay is no longer the one that owns the data plane
	ErrRetired = errors.New("controller generation is retired")
)

// Span names for one reconcile pass and its stages
const (
	spanReconcile    = "kgw.reconcile"
	spanTranslate    = "kgw.translate"
	spanCompile      = "kgw.compile"
	spanApply        = "kgw.apply"
	spanCertificates = "kgw.certificates"
	spanStatus       = "kgw.status"
)

// Kinds of generated objects the model gauge counts
const (
	gaugeListeners    = "listeners"
	gaugeRoutes       = "routes"
	gaugeBackends     = "backends"
	gaugeCertificates = "certificates"
	gaugePolicies     = "policies"
)

// eventCertificateRejected is the reason on the Event a certificate the store refused produces
const eventCertificateRejected = "CertificateRejected"

// Publisher applies each overlay the controller produces, reporting whether the running
// configuration changed; it is the daemon's reload path and the only way to the data plane
type Publisher interface {
	Publish(*config.Overlay) (bool, error)
}

// CertSink is the listeners' runtime certificate store; a generated TLS listener starts with no
// certificate and fails every handshake until one arrives here
type CertSink interface {
	// TLSListeners names the listeners that can hold a runtime certificate
	TLSListeners() []string
	// MemoryCerts reports the source keys a listener is currently serving, and false when there
	// is no such listener; a listener's entries live only as long as the listener does
	MemoryCerts(listenerName string) ([]string, bool)
	SetMemoryCert(listenerName, sourceKey string, certPEM, keyPEM []byte) error
	RemoveMemoryCert(listenerName, sourceKey string) error
}

// Config carries the controller's dependencies
type Config struct {
	// Options is the validated kubernetes configuration section
	Options *kubecfg.Options
	// Publisher applies the overlays this controller produces
	Publisher Publisher
	// Certs receives the certificate material generated TLS listeners serve;
	// nil serves no certificates, which a plaintext-only install is free to do
	Certs CertSink
	// CertState is the inventory of what predecessors put into the stores, so a narrowed scope
	// can withdraw certificates it no longer claims; nil starts empty
	CertState *CertState
	// KnownNames reports what the running configuration defines that a route may name, for
	// rejecting one that names something absent; nil accepts any name
	KnownNames func() ir.ConfiguredNames
	// Tracer returns the tracer reconcile spans are reported to; nil, or a nil result, traces nothing
	Tracer func() *tracing.Tracer
	// Identity is this replica's name in the leader election; empty derives
	// one from the hostname
	Identity string
	// Client, GatewayClient and DynamicClient override the clients built from Options, which
	// is how tests supply fakes; the dynamic client reads the cache policy resource
	Client        *kube.Client
	GatewayClient gwclient.Interface
	DynamicClient dynamic.Interface
	// ProviderPaths returns the paths a time series provider predefines, which the compiler
	// serves a governed route through and a route may not declare itself; nil serves no provider
	ProviderPaths compile.ProviderPaths
	// Recorder overrides the Event recorder built from the client, which is
	// how tests capture Events
	Recorder *events.Recorder
}

// Controller is one running instance of the Kubernetes controller
type Controller struct {
	cfg     Config
	claimer *class.Claimer
	watcher *watch.Watcher
	// gatewayResources are the Gateway API kinds the cluster serves, read once when the
	// controller probes for the API; nil when a client was supplied and the probe did not run
	gatewayResources []string
	// alphaResources are the experimental Gateway API kinds the cluster serves, probed the same
	// way; an empty list is a cluster without the experimental channel
	alphaResources []string
	// status writes conditions and addresses back to the cluster; nil when the instance is read-only
	status *status.Writer
	// recorder publishes Events, one term at a time; nil when the instance is read-only
	recorder *events.Recorder
	// verdicts caches whether each TLS Secret's material can be served
	verdicts *certVerdicts
	// elector decides whether this replica is the one that writes; nil when the election is off,
	// in which case a writing instance always writes
	elector *leader.Elector
	// statusJob holds the latest report awaiting a status write; a newer pass replaces an unwritten
	// one, so the status worker never works through a backlog of superseded reports
	statusJob atomic.Pointer[statusJob]
	// statusSignal wakes the status worker; it carries at most one wake-up
	statusSignal chan struct{}
	// statusDone closes when the status worker has exited
	statusDone chan struct{}

	// dirty carries at most one pending reconcile, emptied before translating, so changes
	// during a pass produce one more pass and never a concurrent one
	dirty  chan struct{}
	runCtx context.Context
	cancel context.CancelFunc
	// electCtx is the election's lifetime, separate from the workers' so the Lease is released
	// only after they drain; a Start after Stop finds it already ended and contends for nothing
	electCtx    context.Context
	electCancel context.CancelFunc
	// done closes when the worker has exited, so Stop can wait for a
	// reconcile that is already running
	done chan struct{}
	// first closes after the first reconcile completes, which is what Start
	// blocks on
	first    chan struct{}
	firstOne sync.Once
	stopOnce sync.Once
	// synced closes when the informer caches finish their initial sync; a pass over a cache
	// still filling would describe a cluster missing most of itself and delete unseen routes
	synced    chan struct{}
	syncedOne sync.Once
	// stopped closes as soon as Stop is called, before it waits for the
	// pass in flight, so that pass can tell it is being retired
	stopped chan struct{}

	mtx sync.Mutex
	// version is the version of the last overlay published, so a reconcile
	// that changes nothing costs a compile rather than a reload
	version string
	// problems digests the last reported translation problems, so a resync
	// that re-delivers unchanged objects does not re-log them
	problems string
	// emitted is the set of Events the previous pass published, so a resync re-delivering
	// the same objects publishes nothing new; a new leadership term starts it over
	emitted map[string]struct{}
}

// statusJob is one pass's verdict awaiting its status write
type statusJob struct {
	report *ir.Report
	// programmed is whether the data plane took the pass's configuration
	programmed bool
	// term is the leadership term the verdict was reached under; it is written under no other
	term context.Context
}

// certKey identifies one certificate on one listener
type certKey struct{ listener, source string }

// certMaterial is one certificate pair and the object that referenced it; unusable material
// is still wanted, so a bad rotation does not withdraw what a listener already serves
type certMaterial struct {
	crt, key []byte
	source   ir.Source
	identity ir.CertIdentity
	usable   bool
}

// CertState is the inventory of runtime certificates by listener and source key, each with what it
// answers for; it outlives any one controller so a successor withdraws and judges by what serves
type CertState struct {
	mtx     sync.Mutex
	entries map[certKey]certInstall
}

// certInstall is one installed certificate: the digest of its material and what it answers for
type certInstall struct {
	digest   string
	identity ir.CertIdentity
}

// NewCertState returns an empty certificate inventory
func NewCertState() *CertState {
	return &CertState{entries: make(map[certKey]certInstall)}
}

func (s *CertState) digest(k certKey) (string, bool) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	e, ok := s.entries[k]
	return e.digest, ok
}

func (s *CertState) record(k certKey, digest string, identity ir.CertIdentity) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.entries[k] = certInstall{digest: digest, identity: identity}
}

// serving returns what the certificate installed under a source on one listener answers for,
// and whether one is installed there; a certificate serves only the store it was put in
func (s *CertState) serving(listener, source string) (ir.CertIdentity, bool) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	e, ok := s.entries[certKey{listener: listener, source: source}]
	return e.identity, ok
}

func (s *CertState) forget(k certKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.entries, k)
}

func (s *CertState) keys() []certKey {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	out := make([]certKey, 0, len(s.entries))
	for k := range s.entries {
		out = append(out, k)
	}
	return out
}

// New builds a controller for the provided configuration, constructing the
// Kubernetes clients it was not given. It starts nothing; call Start.
func New(cfg Config) (*Controller, error) {
	if cfg.Options == nil {
		return nil, ErrNoOptions
	}
	if cfg.Publisher == nil {
		return nil, ErrNoPublisher
	}
	c := &Controller{
		cfg:          cfg,
		dirty:        make(chan struct{}, 1),
		done:         make(chan struct{}),
		first:        make(chan struct{}),
		synced:       make(chan struct{}),
		stopped:      make(chan struct{}),
		emitted:      make(map[string]struct{}),
		statusSignal: make(chan struct{}, 1),
		statusDone:   make(chan struct{}),
		verdicts:     newCertVerdicts(),
	}
	if c.cfg.CertState == nil {
		c.cfg.CertState = NewCertState()
	}
	if c.cfg.Client == nil {
		client, err := kube.New(cfg.Options.Connection)
		if err != nil {
			return nil, err
		}
		c.cfg.Client = client
	}
	if c.cfg.GatewayClient == nil {
		// informers for kinds the API server does not serve would hang startup on caches that
		// never sync, so the Gateway API is watched only where it exists; Ingress serves either way
		resources, available, err := gatewayapi.Resources(c.cfg.Client)
		if err != nil {
			return nil, err
		}
		c.gatewayResources = resources
		if available {
			gc, err := gatewayapi.NewClientset(c.cfg.Client)
			if err != nil {
				return nil, err
			}
			c.cfg.GatewayClient = gc
			// the stream route kinds are served only where the experimental channel is installed
			alpha, _, err := gatewayapi.AlphaResources(c.cfg.Client)
			if err != nil {
				return nil, err
			}
			c.alphaResources = alpha
			if c.alphaResources == nil {
				c.alphaResources = []string{}
			}
		} else {
			logger.Info("kubernetes cluster does not serve the gateway api; "+
				"only ingress objects will be served",
				logging.Pairs{keys.Scope: kube.LogScope, keys.Detail: gatewayapi.GroupVersion})
		}
	}
	if c.cfg.DynamicClient == nil {
		// the cache policy resource is a CRD of this project's own; it is watched only where
		// the cluster serves it, for the same reason the Gateway API is
		served, err := cachepolicy.Served(c.cfg.Client)
		if err != nil {
			return nil, err
		}
		if served {
			dyn, err := cachepolicy.NewDynamicClient(c.cfg.Client)
			if err != nil {
				return nil, err
			}
			c.cfg.DynamicClient = dyn
		} else {
			logger.Info("kubernetes cluster does not serve the cache policy resource; "+
				"routes are served without one",
				logging.Pairs{keys.Scope: kube.LogScope, keys.Detail: cachepolicy.GVR.String()})
		}
	}
	c.claimer = class.New(cfg.Options.GatewayClassControllerName,
		cfg.Options.IngressClass)
	w, err := watch.New(watch.Config{
		Client:           c.cfg.Client,
		GatewayClient:    c.cfg.GatewayClient,
		GatewayResources: c.gatewayResources,
		AlphaResources:   c.alphaResources,
		DynamicClient:    c.cfg.DynamicClient,
		Options:          cfg.Options,
		OnChange:         c.Resync,
	})
	if err != nil {
		return nil, err
	}
	c.watcher = w
	if err := c.buildWriters(); err != nil {
		w.Stop()
		return nil, err
	}
	// nothing after this point can fail, so a controller that failed to build leaves no goroutine
	// #nosec G118 -- Stop calls the retained cancel function
	c.runCtx, c.cancel = context.WithCancel(context.Background())
	c.electCtx, c.electCancel = context.WithCancel(context.Background())
	go c.run(c.runCtx)
	go c.statusWorker(c.runCtx)
	return c, nil
}

func (c *Controller) buildWriters() error {
	o := c.cfg.Options
	if !o.WritesCluster() {
		return nil
	}
	cs := c.cfg.Client.Clientset()
	c.status = status.New(status.Config{
		Client: cs, GatewayClient: c.cfg.GatewayClient, DynamicClient: c.cfg.DynamicClient,
		Cache: c.watcher, ControllerName: o.GatewayClassControllerName,
		Timeout: c.cfg.Client.Timeout(),
	})
	c.recorder = c.cfg.Recorder
	if c.recorder == nil {
		c.recorder = events.New(cs)
	}
	if !o.ElectsLeader() {
		return nil
	}
	le := o.LeaderElection
	e, err := leader.New(leader.Config{
		Client: cs, Namespace: le.Namespace, Name: le.Name, Identity: c.cfg.Identity,
		LeaseDuration: time.Duration(le.LeaseDuration),
		RenewDeadline: time.Duration(le.RenewDeadline),
		RetryPeriod:   time.Duration(le.RetryPeriod),
		OnChange:      c.onLeaderChange,
	})
	if err != nil {
		return err
	}
	c.elector = e
	return nil
}

func (c *Controller) onLeaderChange(leader bool) {
	// a term begins or ends for the Event recorder; a new term republishes everything
	// current, since what came before is another replica's
	if !leader {
		c.recorder.End()
		return
	}
	if term, ok := c.leaderTerm(); ok {
		c.recorder.Begin(term)
	}
	c.mtx.Lock()
	c.emitted = make(map[string]struct{})
	c.mtx.Unlock()
	c.Resync()
}

// IsLeader reports whether this replica is the one writing status and Events: never when
// read-only, always when the election is off, and otherwise as the election decides
func (c *Controller) IsLeader() bool {
	_, ok := c.leaderTerm()
	return ok
}

func (c *Controller) leaderTerm() (context.Context, bool) {
	if !c.cfg.Options.WritesCluster() {
		return nil, false
	}
	if c.elector == nil {
		return c.runCtx, true
	}
	return c.elector.Term()
}

// Resync asks for another reconcile, from the watch layer or from a caller whose configuration
// changed underneath a running controller; a pass already queued absorbs it
func (c *Controller) Resync() {
	select {
	case c.dirty <- struct{}{}:
	default:
	}
}

func (c *Controller) run(ctx context.Context) {
	// one pass at a time: two in flight could apply in the opposite order to the one they read
	// in, and the reload lock cannot tell which snapshot is newer, so an older pass would win
	defer close(c.done)
	defer c.firstOne.Do(func() { close(c.first) })
	// a resync can be asked for from outside the watch layer, so the sync wait is here
	// rather than at the point of delivery: it covers every way a pass can be requested
	select {
	case <-ctx.Done():
		return
	case <-c.synced:
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.dirty:
		}
		c.safeReconcile(ctx)
		c.firstOne.Do(func() { close(c.first) })
	}
}

func (c *Controller) safeReconcile(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("kubernetes controller reconcile panic",
				logging.Pairs{keys.Scope: kube.LogScope, keys.Panic: r})
		}
	}()
	c.reconcile(ctx)
}

// Start begins watching and blocks until the informer caches have synced
// and the first translation has been applied
func (c *Controller) Start(ctx context.Context) error {
	if c.elector != nil {
		// contending starts now and outlives the workers, so Stop can drain them before
		// the Lease is released; a replica that cannot reach the cluster cannot win
		c.elector.Start(c.electCtx)
	} else if term, ok := c.leaderTerm(); ok {
		c.recorder.Begin(term)
	}
	if err := c.watcher.Start(ctx); err != nil {
		// the caches never synced, so the parked worker has neither translated nor applied
		// anything; the supervisor builds another controller
		return err
	}
	c.syncedOne.Do(func() { close(c.synced) })
	select {
	case <-c.first:
	case <-c.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// Stop stops watching and waits for a reconcile in flight, so a replaced controller cannot
// publish after its replacement; certificates stay, since the replacement can withdraw them
func (c *Controller) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopped)
		c.cancel()
	})
	c.watcher.Stop()
	<-c.done
	<-c.statusDone
	// the term's Events are cancelled before the Lease is released, so a successor never finds
	// them arriving under its term; then the Lease goes, so it need not wait for expiry
	c.recorder.End()
	c.electCancel()
	if c.elector != nil {
		c.elector.Stop()
	}
	// what this controller generated is no longer what the process serves
	metrics.KubeRouteInfo.Reset()
	metrics.KubeGeneratedObjects.Reset()
}

func (c *Controller) reconcile(ctx context.Context) {
	// whole-world rather than incremental: a rebuild is cheap, the IR hash makes a no-op rebuild
	// free, and a missed or duplicated watch event costs nothing instead of lasting drift
	started := time.Now()
	ctx, span := c.span(ctx, spanReconcile)
	c.verdicts.begin()
	defer c.verdicts.sweep()
	result := metrics.KubeResultUnchanged
	var failure error
	defer func() {
		metrics.KubeReconciles.WithLabelValues(result).Inc()
		metrics.KubeReconcileDuration.Observe(time.Since(started).Seconds())
		if span != nil {
			span.SetAttributes(attribute.String(keys.Result, result))
		}
		endSpan(span, failure)
	}()

	model, report, problems := c.translate(ctx)
	c.logProblems(problems)
	overlay, manifest, err := c.compile(ctx, model)
	if err != nil {
		// the last-good overlay stays in force: publishing nothing would
		// take down every generated route over one untranslatable object
		logger.Error("kubernetes controller could not compile configuration",
			logging.Pairs{keys.Scope: kube.LogScope, keys.Error: err.Error()})
		metrics.KubeReconcileErrors.WithLabelValues(metrics.KubeStageCompile).Inc()
		result, failure = metrics.KubeResultError, err
		return
	}
	accepted, changed, err := c.publish(ctx, overlay)
	if errors.Is(err, ErrRetired) {
		// a newer configuration owns the data plane; this pass describes a scope that is no
		// longer this process's, and its certificates are no longer its to add or withdraw
		return
	}
	if err != nil {
		metrics.KubeReconcileErrors.WithLabelValues(metrics.KubeStageApply).Inc()
		result, failure = metrics.KubeResultError, err
	} else if changed {
		result = metrics.KubeResultApplied
	}
	// certificates are pushed on every pass, since a rotation changes no configuration; a
	// rejected configuration is not running, so its certificate set is not the live one
	certProblems, installed := c.applyCerts(ctx, model, accepted)
	if len(certProblems) > 0 {
		metrics.KubeReconcileErrors.WithLabelValues(metrics.KubeStageCertificates).Inc()
	}
	observeModel(model, manifest)
	if err == nil {
		metrics.KubeLastSuccessfulSync.Set(float64(time.Now().Unix()))
	}
	term, leader := c.leaderTerm()
	if !leader || c.stopping() {
		return
	}
	markProgrammed(model, report, installed)
	// status goes to its own worker: the API calls it makes must not hold up the next pass,
	// which may carry a route removal or a certificate rotation the data plane is waiting for
	c.queueStatus(term, report, accepted)
	c.publishEvents(term, append(problems, certProblems...), report)
}

func (c *Controller) queueStatus(term context.Context, report *ir.Report, programmed bool) {
	c.statusJob.Store(&statusJob{report: report, programmed: programmed, term: term})
	select {
	case c.statusSignal <- struct{}{}:
	default:
	}
}

func (c *Controller) statusWorker(ctx context.Context) {
	defer close(c.statusDone)
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.statusSignal:
		}
		job := c.statusJob.Swap(nil)
		if job == nil || job.term.Err() != nil {
			// a verdict reached under a term that has ended is not a later term's to write
			continue
		}
		// the write ends with the term or with the controller, whichever is first
		writeCtx, cancel := context.WithCancel(job.term)
		stop := context.AfterFunc(ctx, cancel)
		c.writeStatus(writeCtx, job)
		stop()
		cancel()
	}
}

func (c *Controller) translate(ctx context.Context) (*ir.IR, *ir.Report, []ir.Problem) {
	// the Gateway API is translated only where the cluster serves it; elsewhere
	// the watcher holds none of its kinds and Ingress is the whole story.
	started := time.Now()
	_, span := c.span(ctx, spanTranslate)
	defer func() {
		metrics.KubeTranslateDuration.Observe(time.Since(started).Seconds())
		endSpan(span, nil)
	}()
	// the cache policies are indexed once and handed to both translators, since one policy may
	// govern a Service that an Ingress and an HTTPRoute both reach
	policies := c.policyIndex()
	model, report, problems := ingress.Translate(ingress.Config{
		Cache: c.watcher, Claimer: c.claimer, Options: c.cfg.Options,
		KnownNames: c.cfg.KnownNames, CertJudge: c.verdicts.judge, Policies: policies,
	})
	if c.watcher.ServesGatewayAPI() {
		gm, gr, gp := gateway.Translate(gateway.Config{
			Cache: c.watcher, Claimer: c.claimer, Options: c.cfg.Options,
			KnownNames: c.cfg.KnownNames, CertJudge: c.verdicts.judge,
			CertServing: c.certServing, Policies: policies,
		})
		model, report, problems = ir.Merge(model, gm), report.Merge(gr), append(problems, gp...)
	}
	report.Policies = policies.Report()
	return model, report, append(problems, policies.Problems()...)
}

func (c *Controller) policyIndex() *cachepolicy.Index {
	if !c.watcher.ServesCachePolicies() {
		return nil
	}
	var known ir.ConfiguredNames
	if c.cfg.KnownNames != nil {
		known = c.cfg.KnownNames()
	}
	return cachepolicy.New(c.watcher.CachePolicies(), cachepolicy.Config{
		Known: known, Exists: c.targetExists, ProviderPaths: c.providerPathNames,
	})
}

func (c *Controller) providerPathNames(provider string) []string {
	if c.cfg.ProviderPaths == nil {
		return nil
	}
	paths := c.cfg.ProviderPaths(provider)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != nil {
			out = append(out, p.Path)
		}
	}
	return out
}

func (c *Controller) targetExists(kind, namespace, name string) bool {
	switch kind {
	case cachepolicy.KindGateway:
		return c.watcher.Gateway(namespace, name) != nil
	case cachepolicy.KindHTTPRoute:
		return c.watcher.HTTPRoute(namespace, name) != nil
	case cachepolicy.KindService:
		return c.watcher.Service(namespace, name) != nil
	case cachepolicy.KindIngress:
		return c.watcher.Ingress(namespace, name) != nil
	}
	return false
}

func (c *Controller) compile(ctx context.Context, model *ir.IR,
) (*config.Overlay, compile.Manifest, error) {
	_, span := c.span(ctx, spanCompile)
	overlay, manifest, err := compile.CompileWith(model, c.cfg.Options, c.cfg.ProviderPaths)
	endSpan(span, err)
	return overlay, manifest, err
}

func (c *Controller) publish(ctx context.Context, overlay *config.Overlay) (bool, bool, error) {
	c.mtx.Lock()
	unchanged := overlay.VersionString() == c.version
	if !unchanged {
		c.version = overlay.VersionString()
	}
	c.mtx.Unlock()
	if unchanged {
		// the last publication of this same configuration succeeded, or the
		// version would have been cleared, so it is what is running
		return true, false, nil
	}
	started := time.Now()
	_, span := c.span(ctx, spanApply)
	changed, err := c.cfg.Publisher.Publish(overlay)
	metrics.KubeApplyDuration.Observe(time.Since(started).Seconds())
	endSpan(span, err)
	if err != nil {
		// the version is cleared so the next reconcile, which sees the same
		// objects, retries rather than deciding nothing changed
		c.mtx.Lock()
		c.version = ""
		c.mtx.Unlock()
		if errors.Is(err, ErrRetired) {
			logger.Debug("kubernetes controller output discarded; "+
				"a newer configuration owns the data plane",
				logging.Pairs{keys.Scope: kube.LogScope})
			return false, false, err
		}
		logger.Error("kubernetes controller configuration apply failed",
			logging.Pairs{keys.Scope: kube.LogScope, keys.Error: err.Error()})
		return false, false, err
	}
	logger.Info("kubernetes controller applied configuration", logging.Pairs{
		keys.Scope: kube.LogScope, keys.Detail: overlay.VersionString(),
		"reloaded": changed,
	})
	return true, changed, nil
}

func (c *Controller) logProblems(problems []ir.Problem) {
	// an informer resync re-delivers every object unchanged, so logging each pass
	// would fill the log with the same complaints on a timer.
	h := sha256.New()
	for _, p := range problems {
		h.Write([]byte(p.String()))
		h.Write([]byte{0})
	}
	digest := hex.EncodeToString(h.Sum(nil))
	c.mtx.Lock()
	unchanged := digest == c.problems
	c.problems = digest
	c.mtx.Unlock()
	if unchanged {
		return
	}
	for _, p := range problems {
		logger.Warn("kubernetes object partially translated", logging.Pairs{
			keys.Scope: kube.LogScope, keys.Detail: p.Detail,
			keys.Key: p.Source.Key(), keys.Reason: p.EventReason(),
		})
	}
}

func (c *Controller) writeStatus(term context.Context, job *statusJob) {
	if c.status == nil {
		return
	}
	ctx, span := c.span(term, spanStatus)
	failed := c.status.Write(ctx, status.Input{
		Report: job.report, Programmed: job.programmed,
		Addresses:     status.Addresses(c.watcher.PublishedService()),
		WantAddresses: c.cfg.Options.PublishedService != nil,
	})
	if failed > 0 {
		metrics.KubeReconcileErrors.WithLabelValues(metrics.KubeStageStatus).Inc()
	}
	if span != nil {
		span.SetAttributes(attribute.Int("kgw.status.failures", failed))
	}
	endSpan(span, nil)
}

func (c *Controller) publishEvents(term context.Context, problems []ir.Problem, report *ir.Report) {
	if c.recorder == nil || term.Err() != nil {
		return
	}
	current := make(map[string]struct{}, len(problems))
	c.mtx.Lock()
	previous := c.emitted
	c.emitted = current
	c.mtx.Unlock()
	emit := func(key string, publish func()) {
		current[key] = struct{}{}
		if _, done := previous[key]; done || term.Err() != nil {
			return
		}
		publish()
	}
	for _, p := range problems {
		key := p.Source.Key() + "\x00" + p.EventReason() + "\x00" + p.Detail
		emit(key, func() { c.recorder.Warning(p.Source, p.EventReason(), p.Detail) })
	}
	if report == nil {
		return
	}
	for _, cl := range report.Classes {
		if !cl.Accepted.Status {
			continue
		}
		key := cl.Source.Key() + "\x00" + cl.Accepted.Reason + "\x00" + cl.Accepted.Message
		emit(key, func() { c.recorder.Normal(cl.Source, cl.Accepted.Reason, cl.Accepted.Message) })
	}
}

func observeModel(model *ir.IR, manifest compile.Manifest) {
	metrics.KubeGeneratedObjects.WithLabelValues(gaugeListeners).Set(float64(len(model.Listeners)))
	metrics.KubeGeneratedObjects.WithLabelValues(gaugeRoutes).Set(float64(len(model.Routes)))
	metrics.KubeGeneratedObjects.WithLabelValues(gaugeBackends).Set(float64(len(model.Backends)))
	metrics.KubeGeneratedObjects.WithLabelValues(gaugeCertificates).Set(float64(len(model.Certs)))
	metrics.KubeGeneratedObjects.WithLabelValues(gaugePolicies).Set(float64(len(model.Policies)))
	metrics.KubeRouteInfo.Reset()
	for _, rb := range manifest {
		for _, name := range rb.Backends {
			metrics.KubeRouteInfo.WithLabelValues(rb.Source.Kind, rb.Source.Name,
				rb.Source.Namespace, name).Set(1)
		}
	}
}

func (c *Controller) span(ctx context.Context, name string) (context.Context, trace.Span) {
	if c.cfg.Tracer == nil {
		return ctx, nil
	}
	tr := c.cfg.Tracer()
	if tr == nil || tr.Tracer == nil {
		return ctx, nil
	}
	return tr.Start(ctx, name)
}

func endSpan(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func (c *Controller) applyCerts(ctx context.Context, model *ir.IR, accepted bool,
) ([]ir.Problem, map[certKey]struct{}) {
	if c.cfg.Certs == nil {
		return nil, nil
	}
	if c.stopping() {
		// this controller is being retired; its replacement reconciles the stores, and two
		// generations writing the same stores would undo each other
		return nil, nil
	}
	_, span := c.span(ctx, spanCertificates)
	defer endSpan(span, nil)
	available := make(map[string]struct{})
	for _, name := range c.cfg.Certs.TLSListeners() {
		available[name] = struct{}{}
	}
	byName := make(map[string]ir.CertRef, len(model.Certs))
	for _, ref := range model.Certs {
		byName[ref.Name] = ref
	}
	var problems []ir.Problem
	want := make(map[certKey]certMaterial)
	for _, l := range model.Listeners {
		if !l.External && l.Protocol != ir.ProtocolHTTPS {
			continue
		}
		listener := compile.CompiledListenerName(l)
		if _, ok := available[listener]; !ok {
			// the listener is not created yet (the reload creating it ends with another pass here)
			// or does not serve TLS: the sink enumerates only listeners that can hold a certificate
			continue
		}
		for _, name := range l.CertRefs {
			ref, ok := byName[name]
			if !ok {
				continue
			}
			crt, key, identity, problem := c.secretPEM(ref)
			if crt == nil && problem == nil {
				continue
			}
			if problem != nil {
				problems = append(problems, *problem)
			}
			want[certKey{listener: listener, source: ref.Key()}] = certMaterial{
				crt: crt, key: key, source: ref.Source, identity: identity, usable: problem == nil,
			}
		}
	}
	problems = append(problems, c.syncCerts(want, available, accepted)...)
	return problems, c.installedCerts(available)
}

func markProgrammed(model *ir.IR, report *ir.Report, installed map[certKey]struct{}) {
	if report == nil {
		return
	}
	byName := make(map[string]ir.CertRef, len(model.Certs))
	for _, ref := range model.Certs {
		byName[ref.Name] = ref
	}
	var index listenerIndex
	for _, l := range model.Listeners {
		if l.External || l.Protocol != ir.ProtocolHTTPS {
			continue
		}
		compiled := compile.CompiledListenerName(l)
		var served bool
		for _, name := range l.CertRefs {
			ref, ok := byName[name]
			if !ok {
				continue
			}
			if _, ok := installed[certKey{listener: compiled, source: ref.Key()}]; ok {
				served = true
				break
			}
		}
		if served {
			continue
		}
		if index == nil {
			index = indexListeners(report)
		}
		if ls := index.lookup(report, l); ls != nil {
			ls.Conditions = ir.Set(ls.Conditions, ir.Condition{
				Type: string(gwapiv1.ListenerConditionProgrammed), Status: false,
				Reason:  string(gwapiv1.ListenerReasonInvalid),
				Message: "no usable certificate is installed for the listener",
			})
		}
	}
}

// listenerPos locates one listener status entry within a report
type listenerPos struct{ gateway, listener int }

// listenerIndex finds a Gateway listener's status entry by Gateway key and section name, built
// once per report so lowering many listeners does not rescan every Gateway
type listenerIndex map[string]listenerPos

func indexListeners(report *ir.Report) listenerIndex {
	index := make(listenerIndex)
	for gi := range report.Gateways {
		key := report.Gateways[gi].Source.Key()
		for li, l := range report.Gateways[gi].Listeners {
			index[key+"\x00"+l.Name] = listenerPos{gateway: gi, listener: li}
		}
	}
	return index
}

func (x listenerIndex) lookup(report *ir.Report, l ir.Listener) *ir.ListenerStatus {
	pos, ok := x[l.Source.Key()+"\x00"+l.Section]
	if !ok {
		return nil
	}
	return &report.Gateways[pos.gateway].Listeners[pos.listener]
}

func (c *Controller) stopping() bool {
	select {
	case <-c.stopped:
		return true
	default:
		return false
	}
}

func (c *Controller) installedCerts(available map[string]struct{}) map[certKey]struct{} {
	// a reload that destroys a listener takes its entries with it; a digest remembered against
	// that store would make its replacement look populated and fail every handshake
	out := make(map[certKey]struct{})
	for listener := range available {
		sources, ok := c.cfg.Certs.MemoryCerts(listener)
		if !ok {
			continue
		}
		for _, source := range sources {
			out[certKey{listener: listener, source: source}] = struct{}{}
		}
	}
	return out
}

// certServing answers what serves under a Secret on a listener the translator is building, so
// unusable material is judged by what that listener's own store still holds
func (c *Controller) certServing(l ir.Listener, secret string) (ir.CertIdentity, bool) {
	return c.cfg.CertState.serving(compile.CompiledListenerName(l), secret)
}

func (c *Controller) secretPEM(ref ir.CertRef) ([]byte, []byte, ir.CertIdentity, *ir.Problem) {
	// the material installed is the material judged this pass, whose identity the listeners were
	// compared by; a Secret that changed or appeared since waits for the reconcile its event brings
	sec, identity, err := c.verdicts.judged(ref.Key())
	if sec == nil {
		return nil, nil, ir.CertIdentity{}, nil
	}
	crt := sec.Data[corev1.TLSCertKey]
	key := sec.Data[corev1.TLSPrivateKeyKey]
	if err != nil {
		logger.Warn("kubernetes tls secret holds unusable certificate material",
			logging.Pairs{keys.Scope: kube.LogScope, "secret": ref.Key(), keys.Error: err.Error()})
		return crt, key, identity, &ir.Problem{
			Source: ref.Source, Reason: ir.ReasonInvalidCertificate,
			Detail: "tls secret " + ref.Key() + ": " + err.Error(),
		}
	}
	return crt, key, identity, nil
}

func (c *Controller) syncCerts(want map[certKey]certMaterial,
	available map[string]struct{}, accepted bool,
) []ir.Problem {
	// the digests are what keep a rotation cheap: an unchanged pair is not re-
	// parsed and does not rebuild the store behind live handshakes.
	state := c.cfg.CertState
	installed := c.installedCerts(available)
	// a listener that is gone took its certificates with it, so what was
	// recorded against it is no longer ours and no longer there
	for _, k := range state.keys() {
		if _, ok := installed[k]; !ok {
			state.forget(k)
		}
	}
	var problems []ir.Problem
	for k, m := range want {
		if !m.usable {
			// nothing is pushed for it, and nothing already serving under it is withdrawn
			continue
		}
		current, owned := state.digest(k)
		if !accepted && !owned {
			// the daemon rejected this configuration; installing its certificate would put
			// material on a listener for a route that is not serving
			continue
		}
		sum := sha256.Sum256(append(append([]byte{}, m.crt...), m.key...))
		digest := hex.EncodeToString(sum[:])
		if current == digest {
			continue
		}
		if err := c.cfg.Certs.SetMemoryCert(k.listener, k.source, m.crt, m.key); err != nil {
			// the store validates before replacing, so its current certificate stays installed
			// and ours to withdraw; the digest is left alone so the rejected material is retried
			logger.Error("kubernetes controller could not apply a certificate",
				logging.Pairs{
					keys.Scope: kube.LogScope, keys.Error: err.Error(),
					"secret": k.source, keys.ListenerName: k.listener,
				})
			problems = append(problems, ir.Problem{
				Source: m.source, Reason: eventCertificateRejected,
				Detail: "tls secret " + k.source + " was refused by listener " +
					k.listener + ": " + err.Error(),
			})
			continue
		}
		state.record(k, digest, m.identity)
	}
	if !accepted {
		// the routes needing these certificates are still the previous ones; withdrawing
		// against a rejected configuration would take a live route's certificate away
		return problems
	}
	for _, k := range state.keys() {
		if _, ok := want[k]; ok {
			continue
		}
		if err := c.cfg.Certs.RemoveMemoryCert(k.listener, k.source); err != nil {
			logger.Warn("kubernetes controller could not withdraw a certificate",
				logging.Pairs{
					keys.Scope: kube.LogScope, keys.Error: err.Error(),
					"secret": k.source, keys.ListenerName: k.listener,
				})
		}
		state.forget(k)
	}
	return problems
}
