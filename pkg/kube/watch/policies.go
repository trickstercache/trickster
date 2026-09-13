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

package watch

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
)

// policyInformer is the informer over the cache policy resource in one namespace scope, read
// through the dynamic client since the resource has no generated clientset; it drives its own
// lifecycle in the shape of a shared factory handle so the watcher treats it as one
type policyInformer struct {
	informer cache.SharedIndexInformer
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}

	mtx     sync.Mutex
	started bool
}

var _ kube.FactoryHandle = (*policyInformer)(nil)

// unstructuredType is what the informer's sync result is keyed by, as a
// typed factory keys its result by the object type
var unstructuredType = reflect.TypeFor[*unstructured.Unstructured]()

func newPolicyInformer(dyn dynamic.Interface, namespace string, resync time.Duration,
) *policyInformer {
	ctx, cancel := context.WithCancel(context.Background())
	res := dyn.Resource(cachepolicy.GVR).Namespace(namespace)
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return res.List(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (apiwatch.Interface, error) {
			return res.Watch(ctx, o)
		},
	}
	// the reflector streams the initial list where the client supports it; a client that says
	// it does not, as the fakes do, is listed and watched the older way
	return &policyInformer{
		informer: cache.NewSharedIndexInformer(cache.ToListWatcherWithWatchListSemantics(lw, dyn),
			&unstructured.Unstructured{}, resync,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}),
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
}

// Start runs the informer until Release
func (p *policyInformer) Start() {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if p.started {
		return
	}
	p.started = true
	go func() {
		defer close(p.done)
		p.informer.Run(p.ctx.Done())
	}()
}

// WaitForCacheSync blocks until the informer has synced or stop is closed
func (p *policyInformer) WaitForCacheSync(stop <-chan struct{}) map[reflect.Type]bool {
	return map[reflect.Type]bool{
		unstructuredType: cache.WaitForCacheSync(stop, p.informer.HasSynced),
	}
}

// Release stops the informer and waits for it to return; it is idempotent
func (p *policyInformer) Release() {
	p.cancel()
	p.mtx.Lock()
	started := p.started
	p.mtx.Unlock()
	if started {
		<-p.done
	}
}

func (p *policyInformer) list() []*cachepolicy.CachePolicy {
	items := p.informer.GetStore().List()
	out := make([]*cachepolicy.CachePolicy, 0, len(items))
	for _, item := range items {
		if cp := convertPolicy(item); cp != nil {
			out = append(out, cp)
		}
	}
	return out
}

func (p *policyInformer) get(namespace, name string) *cachepolicy.CachePolicy {
	item, ok, err := p.informer.GetStore().GetByKey(namespace + "/" + name)
	if err != nil || !ok {
		return nil
	}
	return convertPolicy(item)
}

func convertPolicy(item any) *cachepolicy.CachePolicy {
	// the API server validated the object against the resource's schema, so one that does not
	// convert is malformed beyond what the schema expresses and is passed over
	u, ok := item.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	cp, err := cachepolicy.FromUnstructured(u)
	if err != nil {
		logger.Warn("kubernetes cache policy could not be read", logging.Pairs{
			keys.Scope: kube.LogScope, keys.Key: ObjectKey(u), keys.Error: err.Error(),
		})
		return nil
	}
	return cp
}
