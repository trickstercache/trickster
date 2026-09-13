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

// Package leader elects, over a Kubernetes Lease, the one replica that writes status and
// Events; every replica programs its own data plane, the election only decides who speaks
package leader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

var (
	// ErrNoClient indicates New was called without a clientset
	ErrNoClient = errors.New("no kubernetes client provided")
	// ErrLeaseTooShort indicates a lease duration under a second, which the
	// Lease API cannot represent
	ErrLeaseTooShort = errors.New("lease duration must be at least one second")
)

// MinLeaseDuration is the shortest lease the Lease API can hold: its
// duration is stored in whole seconds, so anything less reads as expired
const MinLeaseDuration = time.Second

// Config carries the election's inputs
type Config struct {
	// Client is the clientset the Lease is read and written through
	Client kubernetes.Interface
	// Namespace and Name locate the Lease
	Namespace string
	Name      string
	// Identity is this replica's name in the Lease; empty derives one from the hostname
	Identity string
	// LeaseDuration, RenewDeadline and RetryPeriod are the election's timings
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
	// OnChange is called with true when leadership is won and false when it is lost; it must not block
	OnChange func(leader bool)
}

// term is one period of leadership; its context ends when the Lease is lost
type term struct {
	ctx context.Context
}

// Elector contends for the Lease until stopped
type Elector struct {
	cfg    Config
	lock   resourcelock.Interface
	leader atomic.Bool
	// current is the leadership term in progress, nil between terms
	current atomic.Pointer[term]

	mtx     sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
	// stopped is permanent: an elector stopped before it started never contends
	stopped bool
}

// New validates the configuration and prepares the Lease lock; nothing
// contends until Start
func New(cfg Config) (*Elector, error) {
	if cfg.Client == nil {
		return nil, ErrNoClient
	}
	if cfg.LeaseDuration < MinLeaseDuration {
		return nil, ErrLeaseTooShort
	}
	if cfg.Identity == "" {
		cfg.Identity = Identity()
	}
	if cfg.Namespace == "" {
		cfg.Namespace = kube.DefaultNamespace()
	}
	lock, err := resourcelock.New(resourcelock.LeasesResourceLock, cfg.Namespace, cfg.Name,
		cfg.Client.CoreV1(), cfg.Client.CoordinationV1(),
		resourcelock.ResourceLockConfig{Identity: cfg.Identity})
	if err != nil {
		return nil, err
	}
	e := &Elector{cfg: cfg, lock: lock, done: make(chan struct{})}
	// the timings are checked once here, so a bad combination is a construction error
	// rather than a goroutine that exits at once
	if _, err := leaderelection.NewLeaderElector(e.electionConfig()); err != nil {
		return nil, err
	}
	return e, nil
}

// Identity returns a name for this replica: its hostname (in a pod, the pod name) with a random
// suffix, so a restarted replica does not inherit a lease its predecessor still holds
func Identity() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "trickster"
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return host
	}
	return host + "_" + hex.EncodeToString(b)
}

// Identity returns the name this replica contends under
func (e *Elector) Identity() string {
	return e.cfg.Identity
}

// IsLeader reports whether this replica currently holds the Lease
func (e *Elector) IsLeader() bool {
	_, ok := e.Term()
	return ok
}

// Term returns the context of the leadership term in progress, cancelled the moment the Lease
// is lost or released, and false between terms; every write made as leader binds to it
func (e *Elector) Term() (context.Context, bool) {
	t := e.current.Load()
	if t == nil || t.ctx.Err() != nil {
		return nil, false
	}
	return t.ctx, true
}

func (e *Elector) electionConfig() leaderelection.LeaderElectionConfig {
	return leaderelection.LeaderElectionConfig{
		Lock:            e.lock,
		LeaseDuration:   e.cfg.LeaseDuration,
		RenewDeadline:   e.cfg.RenewDeadline,
		RetryPeriod:     e.cfg.RetryPeriod,
		ReleaseOnCancel: true,
		Name:            e.cfg.Name,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				// the term is published before the change is announced, so
				// a listener acting on the announcement finds it
				e.current.Store(&term{ctx: ctx})
				e.set(true)
			},
			OnStoppedLeading: func() {
				e.set(false)
				e.current.Store(nil)
			},
			OnNewLeader: func(identity string) {
				if identity != e.cfg.Identity {
					logger.Info("kubernetes controller leader observed", logging.Pairs{
						keys.Scope: kube.LogScope, keys.Name: identity,
					})
				}
			},
		},
	}
}

func (e *Elector) set(leader bool) {
	if e.leader.Swap(leader) == leader {
		return
	}
	if leader {
		metrics.KubeLeader.Set(1)
		logger.Info("kubernetes controller leadership acquired", logging.Pairs{
			keys.Scope: kube.LogScope, keys.Name: e.cfg.Identity,
		})
	} else {
		metrics.KubeLeader.Set(0)
		logger.Info("kubernetes controller leadership lost", logging.Pairs{
			keys.Scope: kube.LogScope, keys.Name: e.cfg.Identity,
		})
	}
	if e.cfg.OnChange != nil {
		e.cfg.OnChange(leader)
	}
}

// Start begins contending on its own goroutine and returns at once; a
// second Start is a no-op
func (e *Elector) Start(ctx context.Context) {
	e.mtx.Lock()
	defer e.mtx.Unlock()
	if e.started || e.stopped {
		return
	}
	e.started = true
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	go e.run(runCtx)
}

func (e *Elector) run(ctx context.Context) {
	defer close(e.done)
	for {
		// a fresh elector per term: the previous one remembers the record it lost with
		le, err := leaderelection.NewLeaderElector(e.electionConfig())
		if err != nil {
			// the configuration was validated at construction, so this cannot happen
			return
		}
		le.Run(ctx)
		e.set(false)
		e.current.Store(nil)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.cfg.RetryPeriod):
		}
	}
}

// Stop releases the Lease if held and waits for the election goroutine to exit
func (e *Elector) Stop() {
	e.mtx.Lock()
	cancel, started := e.cancel, e.started
	e.stopped = true
	e.mtx.Unlock()
	if !started {
		return
	}
	cancel()
	<-e.done
	e.set(false)
	e.current.Store(nil)
}
