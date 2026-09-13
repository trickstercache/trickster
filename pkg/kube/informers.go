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

package kube

import (
	"context"
	"reflect"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
)

// SharedFactory is the part of a generated client-go informer factory the registry drives; the
// core and gateway-api factories both satisfy it, so one reference-counting implementation serves both
type SharedFactory interface {
	Start(stopCh <-chan struct{})
	WaitForCacheSync(stopCh <-chan struct{}) map[reflect.Type]bool
	Shutdown()
}

// FactorySpec is the server-side filtering that defines a shared informer factory; subscribers
// agreeing on it and on the connection share one watch per resource. Selectors are rendered strings.
type FactorySpec struct {
	// Namespace scopes the factory; empty means all namespaces
	Namespace string
	// LabelSelector filters watched objects server-side
	LabelSelector string
	// FieldSelector filters watched objects server-side
	FieldSelector string
	// Resync is the informer resync period; 0 disables periodic resync
	Resync time.Duration
}

// factoryKey identifies one shared factory: a spec reached over one connection identity, for one
// API group set; the kind keeps the core and gateway-api factories for one spec distinct
type factoryKey struct {
	conn string
	kind string
	spec FactorySpec
}

// factory kinds, the API group set a shared factory serves
const (
	factoryKindCore    = "core"
	factoryKindGateway = "gateway"
)

// factoryEntry is one live shared factory and its reference count; its context is its own, so
// one subscriber's cancellation cannot stop watches another still needs
type factoryEntry struct {
	factory SharedFactory
	ctx     context.Context
	cancel  context.CancelFunc
	refs    int
}

// factoryRegistry holds the live factories keyed by connection and spec
type factoryRegistry struct {
	mtx     sync.Mutex
	entries map[factoryKey]*factoryEntry
}

// informerRegistry is the process-wide shared informer registry; handles are reference-counted,
// so a factory lives exactly as long as some subscriber holds it
var informerRegistry = newFactoryRegistry()

func newFactoryRegistry() *factoryRegistry {
	return &factoryRegistry{entries: make(map[factoryKey]*factoryEntry)}
}

// InformerFactory is a reference-counted handle to a shared informer factory; the holder registers
// through Factory(), calls Start once, and calls Release exactly once when done
type InformerFactory[F SharedFactory] struct {
	reg   *factoryRegistry
	key   factoryKey
	entry *factoryEntry

	mtx      sync.Mutex
	released bool
}

// CoreInformerFactory is a handle to a shared core (k8s.io/api) factory
type CoreInformerFactory = InformerFactory[informers.SharedInformerFactory]

// FactoryHandle is the lifecycle a reference-counted handle presents, whatever API group set its
// factory serves, so handles to the core and gateway-api factories drive as one list
type FactoryHandle interface {
	// Start starts every informer registered on the shared factory that is
	// not already running
	Start()
	// WaitForCacheSync blocks until the started informers have synced or
	// stop is closed
	WaitForCacheSync(stop <-chan struct{}) map[reflect.Type]bool
	// Release drops this holder's reference; the last one stops the factory
	Release()
}

var _ FactoryHandle = (*CoreInformerFactory)(nil)

// InformerFactory returns a handle to the shared core informer factory for the spec on this
// client's connection, creating it if no other holder has one; the caller owns one Release per call
func (c *Client) InformerFactory(spec FactorySpec) *CoreInformerFactory {
	return getFactory(c.id, factoryKindCore, spec,
		func() informers.SharedInformerFactory {
			return informers.NewSharedInformerFactoryWithOptions(
				c.cs, spec.Resync, tweakCore(spec)...)
		})
}

func tweakCore(spec FactorySpec) []informers.SharedInformerOption {
	opts := []informers.SharedInformerOption{
		informers.WithNamespace(spec.Namespace),
	}
	if spec.LabelSelector != "" || spec.FieldSelector != "" {
		opts = append(opts, informers.WithTweakListOptions(spec.Tweak()))
	}
	return opts
}

// Tweak returns the ListOptions mutator for the spec's selectors; a factory holds exactly one
// tweak func, so label and field filtering are combined into it
func (s FactorySpec) Tweak() func(*metav1.ListOptions) {
	return func(lo *metav1.ListOptions) {
		if s.LabelSelector != "" {
			lo.LabelSelector = s.LabelSelector
		}
		if s.FieldSelector != "" {
			lo.FieldSelector = s.FieldSelector
		}
	}
}

// getFactory returns a reference-counted handle to the shared factory for
// the key, calling build only when no other holder has one.
func getFactory[F SharedFactory](conn, kind string, spec FactorySpec,
	build func() F,
) *InformerFactory[F] {
	r := informerRegistry
	key := factoryKey{conn: conn, kind: kind, spec: spec}
	r.mtx.Lock()
	defer r.mtx.Unlock()
	e, ok := r.entries[key]
	if !ok {
		ctx, cancel := context.WithCancel(context.Background())
		e = &factoryEntry{factory: build(), ctx: ctx, cancel: cancel}
		r.entries[key] = e
	}
	e.refs++
	return &InformerFactory[F]{reg: r, key: key, entry: e}
}

// Factory returns the shared factory to register informers and event
// handlers on
func (f *InformerFactory[F]) Factory() F {
	return f.entry.factory.(F)
}

// Start starts every informer registered on the shared factory that is not already running,
// including other holders'; it is safe to call from each holder
func (f *InformerFactory[F]) Start() {
	f.entry.factory.Start(f.entry.ctx.Done())
}

// WaitForCacheSync blocks until the started informers have synced or stop is closed; the result
// covers every started informer on the shared factory, which may include another holder's
func (f *InformerFactory[F]) WaitForCacheSync(stop <-chan struct{}) map[reflect.Type]bool {
	return f.entry.factory.WaitForCacheSync(stop)
}

// Release drops this holder's reference; the last release stops the shared factory and joins its
// goroutines, earlier ones leave it running for the remaining holders. Release is idempotent.
func (f *InformerFactory[F]) Release() {
	f.mtx.Lock()
	if f.released {
		f.mtx.Unlock()
		return
	}
	f.released = true
	f.mtx.Unlock()

	f.reg.mtx.Lock()
	f.entry.refs--
	last := f.entry.refs <= 0
	if last {
		// deleted under the registry lock so that a concurrent get for the
		// same key builds a fresh entry rather than one being shut down
		delete(f.reg.entries, f.key)
	}
	f.reg.mtx.Unlock()

	if last {
		f.entry.cancel()
		// Shutdown joins the informer goroutines, so it runs outside the
		// registry lock; no other holder can reach this entry any more
		f.entry.factory.Shutdown()
	}
}

// GatewayInformerFactory returns a reference-counted handle to the shared gateway-api factory for
// the spec; it lives here so the registry stays unexported, and the caller supplies the constructor
func GatewayInformerFactory[F SharedFactory](c *Client, spec FactorySpec,
	build func() F,
) *InformerFactory[F] {
	return getFactory(c.id, factoryKindGateway, spec, build)
}
