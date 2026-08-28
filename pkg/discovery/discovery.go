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

// Package discovery provides runtime autodiscovery of ALB pool members via
// pluggable providers (kubernetes, dns_srv, dns_a, file). A Discoverer is a
// long-lived, connection-level object constructed from a named entry in the
// top-level 'discovery' config section; consumers (ALBs, and the kubernetes
// gateway controller) subscribe to it with a provider-specific Query and
// receive full-membership Snapshots as membership changes.
//
// Discovery is control-plane code: implementations and subscribers may use
// goroutines, channels and callbacks freely, but nothing in this package may
// be called on the request hot path. Consumers apply snapshots to
// atomically-swapped pool state that request handling reads without locks.
package discovery

import (
	"context"

	"github.com/trickstercache/trickster/v2/pkg/discovery/options"
)

// SnapshotHandler is called with the full current membership each time a
// subscribed query's membership changes. Handlers are invoked from the
// discoverer's control loop and must not block; long work should be handed
// off. The Snapshot is owned by the handler and will not be mutated by the
// discoverer after delivery.
type SnapshotHandler func(Snapshot)

// Discoverer is a running discovery provider instance. One Discoverer is
// constructed per named entry in the 'discovery' config section, and is
// shared by every subscriber referencing that name, so provider
// implementations must multiplex subscriptions over one client/informer/
// resolver set.
//
// Lifecycle: Subscribe may be called before or after Start. After Start,
// each subscription receives an initial snapshot as soon as the provider
// has one, then a new snapshot on each membership change (implementations
// should suppress no-op deliveries via Snapshot.Equal). Stop terminates all
// watching/polling; handlers are not called after Stop returns.
type Discoverer interface {
	// Start begins watching or polling. The context bounds the discoverer's
	// lifetime; cancellation is equivalent to Stop.
	Start(ctx context.Context) error
	// Stop terminates discovery and releases provider resources
	Stop() error
	// Subscribe registers a handler for the membership of the provided
	// query, returning an unsubscribe function. The query must already be
	// validated for this discoverer's provider.
	Subscribe(q *options.Query, handler SnapshotHandler) (func(), error)
}

// NewDiscovererFunc is the constructor signature each provider registers;
// name is the discoverer's config name, for logs and metrics
type NewDiscovererFunc func(name string, o *options.Options) (Discoverer, error)

// Lookup is a map of provider name to provider constructor, mirroring
// pkg/backends/providers/registry/types
type Lookup map[string]NewDiscovererFunc
