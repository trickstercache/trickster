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

// Package watch is the controller's watch layer: informers for every kind the translators read
// and one debounced work item saying only that something changed
package watch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/kube/gatewayapi"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	corelisters "k8s.io/client-go/listers/core/v1"
	netlisters "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	gwclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gwlisters "sigs.k8s.io/gateway-api/pkg/client/listers/apis/v1"
	gwlistersv1a2 "sigs.k8s.io/gateway-api/pkg/client/listers/apis/v1alpha2"
)

// ErrNoClient is returned when the watcher has no Kubernetes client
var ErrNoClient = errors.New("no kubernetes client provided")

// ErrCacheSync is returned when an informer cache does not sync
var ErrCacheSync = errors.New("kubernetes informer cache did not sync")

// tlsSecretSelector restricts the Secret watch to TLS secrets; watching every Secret would hold
// them all in memory and need blanket read access
var tlsSecretSelector = fields.OneTermEqualSelector(
	"type", string(corev1.SecretTypeTLS)).String()

// Kinds of the core objects the watcher holds, as its events and debug lines name them
const (
	KindService         = "Service"
	KindSecret          = "Secret"
	KindConfigMap       = "ConfigMap"
	KindNamespace       = "Namespace"
	KindReferenceGrant  = "ReferenceGrant"
	KindPublishedSvc    = "PublishedService"
	kindBackendTLS      = ir.KindBackendTLSPolicy
	eventAdd            = "add"
	eventUpdate         = "update"
	eventDelete         = "delete"
	metadataNameField   = "metadata.name"
	watchEventLogString = "kubernetes watch event"
)

// Config carries the watcher's dependencies
type Config struct {
	// Client is the shared Kubernetes client whose informer registry the
	// watcher joins
	Client *kube.Client
	// GatewayClient is the typed gateway-api clientset; nil means the cluster does not serve the
	// Gateway API and no informer is built for its kinds, since one would never sync
	GatewayClient gwclient.Interface
	// GatewayResources are the resource names the cluster serves in the Gateway API group
	// version; a late-joining kind gets an informer only when served, and nil means all are
	GatewayResources []string
	// AlphaResources are the resource names the cluster serves in the experimental Gateway API
	// group version, where TCPRoute, TLSRoute and UDPRoute live; nil means all are
	AlphaResources []string
	// DynamicClient reads the cache policy resource; nil means the cluster does not serve it
	// and no informer is built for it, since one would never sync
	DynamicClient dynamic.Interface
	// Options is the validated kubernetes configuration section
	Options *kubecfg.Options
	// OnChange is invoked, debounced, whenever a watched object changes and
	// once when the caches first sync. It must not block.
	OnChange func()
}

// Watcher owns the controller's informers and its debounced work item
type Watcher struct {
	cfg      Config
	debounce time.Duration
	// scopes are the namespace-scoped informer sets: one per watched
	// namespace, or a single all-namespaces scope
	scopes []*scope
	// cluster holds the cluster-scoped informers (GatewayClass,
	// IngressClass, and Namespace when a namespace selector is configured)
	cluster *clusterScope
	// published watches the one Service whose addresses are published into status, in its own
	// namespace, which need not be a watched one
	published *publishedScope
	// selector filters namespaces by label when configured; nil admits all
	selector labels.Selector

	mtx     sync.Mutex
	cancel  context.CancelFunc
	timer   *time.Timer
	armed   bool
	synced  bool
	started bool
	stopped bool
}

// scope is one namespace's informer set; a factory is namespace-scoped, so three namespaces
// hold three of these and the whole cluster holds one with an empty namespace
type scope struct {
	namespace string
	handles   []kube.FactoryHandle

	services   corelisters.ServiceLister
	secretLst  corelisters.SecretLister
	configMaps corelisters.ConfigMapLister
	ingresses  netlisters.IngressLister
	gateways   gwlisters.GatewayLister
	routes     gwlisters.HTTPRouteLister
	grpcRoutes gwlisters.GRPCRouteLister
	tcpRoutes  gwlistersv1a2.TCPRouteLister
	tlsRoutes  gwlistersv1a2.TLSRouteLister
	udpRoutes  gwlistersv1a2.UDPRouteLister
	grants     gwlisters.ReferenceGrantLister
	backendTLS gwlisters.BackendTLSPolicyLister
	policies   *policyInformer

	regs []registration
}

// resourceBackendTLSPolicies is the Gateway API resource that governs upstream TLS; it graduated
// later than the core kinds, so a cluster may serve the group without it
const resourceBackendTLSPolicies = "backendtlspolicies"

// resourceGRPCRoutes is the Gateway API resource for gRPC routes, gated like BackendTLSPolicy
// since a cluster may serve an older channel of the group without it
const resourceGRPCRoutes = "grpcroutes"

// The experimental Gateway API resources for stream routes, each served only by a cluster that
// installed the experimental channel
const (
	resourceTCPRoutes = "tcproutes"
	resourceTLSRoutes = "tlsroutes"
	resourceUDPRoutes = "udproutes"
)

func (w *Watcher) serves(resource string) bool {
	return w.cfg.GatewayResources == nil ||
		slices.Contains(w.cfg.GatewayResources, resource)
}

func (w *Watcher) servesAlpha(resource string) bool {
	return w.cfg.AlphaResources == nil ||
		slices.Contains(w.cfg.AlphaResources, resource)
}

// clusterScope holds the informers for cluster-scoped kinds
type clusterScope struct {
	handles []kube.FactoryHandle

	gatewayClasses gwlisters.GatewayClassLister
	ingressClasses netlisters.IngressClassLister
	namespaces     corelisters.NamespaceLister

	regs []registration
}

// publishedScope holds the informer over the published Service
type publishedScope struct {
	handles  []kube.FactoryHandle
	services corelisters.ServiceLister
	regs     []registration
}

// registration pairs an informer with this watcher's handler on it, so the handler can be
// removed at Stop; the informers are shared and outlive the watcher
type registration struct {
	informer cache.SharedIndexInformer
	handle   cache.ResourceEventHandlerRegistration
}

// kindInformer is an informer with the kind it delivers, which is what its
// events are counted and logged by
type kindInformer struct {
	kind     string
	informer cache.SharedIndexInformer
}

// New constructs a Watcher for the provided configuration. It registers
// informers but starts nothing; call Start.
func New(cfg Config) (*Watcher, error) {
	if cfg.Client == nil {
		return nil, ErrNoClient
	}
	if cfg.Options == nil {
		return nil, kubecfg.ErrRoutingModeRequired
	}
	w := &Watcher{
		cfg:      cfg,
		debounce: time.Duration(cfg.Options.DebounceWindow),
	}
	if len(cfg.Options.NamespaceSelector) > 0 {
		w.selector = labels.SelectorFromSet(cfg.Options.NamespaceSelector)
	}
	if err := w.build(); err != nil {
		w.release()
		return nil, err
	}
	return w, nil
}

func (w *Watcher) build() error {
	resync := time.Duration(w.cfg.Options.ResyncInterval)
	if err := w.buildCluster(resync); err != nil {
		return err
	}
	namespaces := w.cfg.Options.WatchNamespaces
	if len(namespaces) == 0 {
		// all namespaces; a namespace selector filters the results rather than the watch,
		// because an informer cannot select by the labels of another object
		namespaces = []string{""}
	}
	for _, ns := range namespaces {
		s, err := w.buildScope(ns, resync)
		if err != nil {
			return err
		}
		w.scopes = append(w.scopes, s)
	}
	return w.buildPublished(resync)
}

func (w *Watcher) buildPublished(resync time.Duration) error {
	// its addresses change without any routing object changing, so the Service itself is
	// watched rather than read once; the field selector keeps the watch to that one object
	ps := w.cfg.Options.PublishedService
	if ps == nil {
		return nil
	}
	spec := kube.FactorySpec{
		Namespace: ps.Namespace, Resync: resync,
		FieldSelector: fields.OneTermEqualSelector(metadataNameField, ps.Name).String(),
	}
	f := w.cfg.Client.InformerFactory(spec)
	inf := f.Factory().Core().V1().Services()
	w.published = &publishedScope{handles: []kube.FactoryHandle{f}, services: inf.Lister()}
	return w.register(&w.published.regs, kindInformer{KindPublishedSvc, inf.Informer()})
}

func (w *Watcher) buildCluster(resync time.Duration) error {
	spec := kube.FactorySpec{Resync: resync}
	core := w.cfg.Client.InformerFactory(spec)
	c := &clusterScope{handles: []kube.FactoryHandle{core}}
	w.cluster = c
	gw := w.gatewayFactory(spec)
	if gw != nil {
		c.handles = append(c.handles, gw)
	}

	icInf := core.Factory().Networking().V1().IngressClasses()
	c.ingressClasses = icInf.Lister()
	informers := []kindInformer{{ir.KindIngressClass, icInf.Informer()}}
	if gw != nil {
		gcInf := gw.Factory().Gateway().V1().GatewayClasses()
		c.gatewayClasses = gcInf.Lister()
		informers = append(informers, kindInformer{ir.KindGatewayClass, gcInf.Informer()})
	}

	if w.selector != nil || gw != nil {
		// needed by the namespace selector and by Gateway listeners admitting routes by namespace
		// label; an Ingress-only controller with no selector never needs namespace read access
		nsInf := core.Factory().Core().V1().Namespaces()
		c.namespaces = nsInf.Lister()
		informers = append(informers, kindInformer{KindNamespace, nsInf.Informer()})
	}
	return w.register(&c.regs, informers...)
}

func (w *Watcher) buildScope(ns string, resync time.Duration) (*scope, error) {
	spec := kube.FactorySpec{Namespace: ns, Resync: resync}
	secretSpec := kube.FactorySpec{
		Namespace: ns, Resync: resync, FieldSelector: tlsSecretSelector,
	}
	core := w.cfg.Client.InformerFactory(spec)
	secrets := w.cfg.Client.InformerFactory(secretSpec)
	s := &scope{
		namespace: ns,
		handles:   []kube.FactoryHandle{core, secrets},
	}

	svcInf := core.Factory().Core().V1().Services()
	s.services = svcInf.Lister()
	ingInf := core.Factory().Networking().V1().Ingresses()
	s.ingresses = ingInf.Lister()
	secInf := secrets.Factory().Core().V1().Secrets()
	s.secretLst = secInf.Lister()
	informers := []kindInformer{
		{KindService, svcInf.Informer()},
		{ir.KindIngress, ingInf.Informer()},
		{KindSecret, secInf.Informer()},
	}

	if gw := w.gatewayFactory(spec); gw != nil {
		s.handles = append(s.handles, gw)
		gwInf := gw.Factory().Gateway().V1().Gateways()
		s.gateways = gwInf.Lister()
		rtInf := gw.Factory().Gateway().V1().HTTPRoutes()
		s.routes = rtInf.Lister()
		rgInf := gw.Factory().Gateway().V1().ReferenceGrants()
		s.grants = rgInf.Lister()
		// a GatewayClass may name a ConfigMap for its parameters and a BackendTLSPolicy one for
		// its CA bundle; nothing else reads ConfigMaps, so an Ingress-only controller holds none
		cmInf := core.Factory().Core().V1().ConfigMaps()
		s.configMaps = cmInf.Lister()
		informers = append(informers,
			kindInformer{ir.KindGateway, gwInf.Informer()},
			kindInformer{ir.KindHTTPRoute, rtInf.Informer()},
			kindInformer{KindReferenceGrant, rgInf.Informer()},
			kindInformer{KindConfigMap, cmInf.Informer()})
		if w.serves(resourceBackendTLSPolicies) {
			btInf := gw.Factory().Gateway().V1().BackendTLSPolicies()
			s.backendTLS = btInf.Lister()
			informers = append(informers, kindInformer{kindBackendTLS, btInf.Informer()})
		}
		if w.serves(resourceGRPCRoutes) {
			grInf := gw.Factory().Gateway().V1().GRPCRoutes()
			s.grpcRoutes = grInf.Lister()
			informers = append(informers, kindInformer{ir.KindGRPCRoute, grInf.Informer()})
		}
		if w.servesAlpha(resourceTCPRoutes) {
			inf := gw.Factory().Gateway().V1alpha2().TCPRoutes()
			s.tcpRoutes = inf.Lister()
			informers = append(informers, kindInformer{ir.KindTCPRoute, inf.Informer()})
		}
		if w.servesAlpha(resourceTLSRoutes) {
			inf := gw.Factory().Gateway().V1alpha2().TLSRoutes()
			s.tlsRoutes = inf.Lister()
			informers = append(informers, kindInformer{ir.KindTLSRoute, inf.Informer()})
		}
		if w.servesAlpha(resourceUDPRoutes) {
			inf := gw.Factory().Gateway().V1alpha2().UDPRoutes()
			s.udpRoutes = inf.Lister()
			informers = append(informers, kindInformer{ir.KindUDPRoute, inf.Informer()})
		}
	}
	if w.cfg.DynamicClient != nil {
		s.policies = newPolicyInformer(w.cfg.DynamicClient, ns, resync)
		s.handles = append(s.handles, s.policies)
		informers = append(informers, kindInformer{ir.KindCachePolicy, s.policies.informer})
	}
	return s, w.register(&s.regs, informers...)
}

func (w *Watcher) gatewayFactory(spec kube.FactorySpec) *gatewayapi.InformerFactory {
	if w.cfg.GatewayClient == nil {
		return nil
	}
	return gatewayapi.Informers(w.cfg.Client, w.cfg.GatewayClient, spec)
}

func (w *Watcher) register(regs *[]registration, informers ...kindInformer) error {
	for _, ki := range informers {
		h, err := ki.informer.AddEventHandler(w.handler(ki.kind))
		if err != nil {
			return err
		}
		*regs = append(*regs, registration{informer: ki.informer, handle: h})
	}
	return nil
}

func (w *Watcher) handler(kind string) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { w.observe(kind, eventAdd, obj) },
		UpdateFunc: func(_, obj any) { w.observe(kind, eventUpdate, obj) },
		DeleteFunc: func(obj any) { w.observe(kind, eventDelete, obj) },
	}
}

func (w *Watcher) observe(kind, event string, obj any) {
	metrics.KubeWatchEvents.WithLabelValues(kind, event).Inc()
	if logger.Level() == level.Debug {
		logger.Debug(watchEventLogString, logging.Pairs{
			keys.Scope: kube.LogScope, keys.Kind: kind, keys.Event: event,
			keys.Key: ObjectKey(obj),
		})
	}
	w.markDirty()
}

// ObjectKey returns the namespace/name identity of a watched object, seeing through the
// tombstone an informer delivers for an object whose deletion it missed
func ObjectKey(obj any) string {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return d.Key
	}
	m, ok := obj.(metav1.Object)
	if !ok {
		return ""
	}
	if ns := m.GetNamespace(); ns != "" {
		return ns + "/" + m.GetName()
	}
	return m.GetName()
}

// Start begins watching, blocks until every cache has synced and its handler has seen the initial
// list, then delivers the first OnChange; on an error the Watcher never started, and Stop is still safe
func (w *Watcher) Start(ctx context.Context) error {
	w.mtx.Lock()
	if w.stopped || w.started {
		w.mtx.Unlock()
		return nil
	}
	w.started = true
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	handlers := w.handlerSyncs()
	w.mtx.Unlock()

	for _, h := range w.handles() {
		h.Start()
	}
	if err := w.waitForSync(runCtx, handlers); err != nil {
		return err
	}
	w.mtx.Lock()
	w.synced = true
	w.mtx.Unlock()
	// the first delivery is immediate: the controller is waiting to program
	// a data plane and every subsequent change is debounced anyway
	w.deliver()
	return nil
}

func (w *Watcher) handles() []kube.FactoryHandle {
	out := make([]kube.FactoryHandle, 0, len(w.scopes)*3+3)
	if w.cluster != nil {
		out = append(out, w.cluster.handles...)
	}
	for _, s := range w.scopes {
		out = append(out, s.handles...)
	}
	if w.published != nil {
		out = append(out, w.published.handles...)
	}
	return out
}

func (w *Watcher) handlerSyncs() []cache.InformerSynced {
	var regs []registration
	if w.cluster != nil {
		regs = append(regs, w.cluster.regs...)
	}
	for _, s := range w.scopes {
		regs = append(regs, s.regs...)
	}
	if w.published != nil {
		regs = append(regs, w.published.regs...)
	}
	out := make([]cache.InformerSynced, 0, len(regs))
	for _, r := range regs {
		if r.handle != nil {
			out = append(out, r.handle.HasSynced)
		}
	}
	return out
}

func (w *Watcher) waitForSync(ctx context.Context, handlers []cache.InformerSynced) error {
	for _, h := range w.handles() {
		for typ, ok := range h.WaitForCacheSync(ctx.Done()) {
			if !ok {
				return fmt.Errorf("%w: %s", ErrCacheSync, typ.String())
			}
		}
	}
	// a synced cache has been listed, but its handlers hear the initial adds on another
	// goroutine; the first delivery, and the counts it is measured by, must follow the last of them
	if len(handlers) > 0 && !cache.WaitForCacheSync(ctx.Done(), handlers...) {
		return fmt.Errorf("%w: event handlers", ErrCacheSync)
	}
	return ctx.Err()
}

// Stop removes this watcher's event handlers and releases its references to the shared
// informer factories, which keep running while another consumer holds them
func (w *Watcher) Stop() {
	w.mtx.Lock()
	if w.stopped {
		w.mtx.Unlock()
		return
	}
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
	cancel := w.cancel
	w.mtx.Unlock()
	if cancel != nil {
		cancel()
	}
	w.release()
}

func (w *Watcher) release() {
	if w.cluster != nil {
		removeAll(w.cluster.regs)
		w.cluster.regs = nil
	}
	for _, s := range w.scopes {
		removeAll(s.regs)
		s.regs = nil
	}
	if w.published != nil {
		removeAll(w.published.regs)
		w.published.regs = nil
	}
	// deregister before releasing: a shared factory another consumer still
	// holds keeps running, and must stop calling into this watcher
	for _, h := range w.handles() {
		h.Release()
	}
}

func removeAll(regs []registration) {
	for _, r := range regs {
		if r.informer != nil && r.handle != nil {
			r.informer.RemoveEventHandler(r.handle) //nolint:errcheck
		}
	}
}

func (w *Watcher) markDirty() {
	// A rollout touching many objects at once produces one rebuild rather than one
	// per object.
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.stopped || !w.synced || w.armed {
		return
	}
	w.armed = true
	if w.debounce <= 0 {
		go w.deliver()
		return
	}
	w.timer = time.AfterFunc(w.debounce, w.deliver)
}

func (w *Watcher) deliver() {
	w.mtx.Lock()
	if w.stopped {
		w.mtx.Unlock()
		return
	}
	w.armed = false
	h := w.cfg.OnChange
	w.mtx.Unlock()
	if h == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			// a panic in translation must not kill the watch layer and
			// freeze the data plane at its last-good configuration silently
			logger.Error("kubernetes controller reconcile panic",
				logging.Pairs{keys.Panic: r})
		}
	}()
	h()
}
