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

package backends

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"

	"github.com/stretchr/testify/require"
)

const (
	// retireTestPause is how long a test watches for something that must not happen yet
	retireTestPause = 50 * time.Millisecond
	// retireTestWait bounds what a test waits on that, broken, would never happen
	retireTestWait = 5 * time.Second
)

// retirements records the order backends retire in, and the deadline each was given
type retirements struct {
	mu        sync.Mutex
	order     []string
	deadlines []time.Time
}

// retiringBackend is a backend of provider recording its retirement
type retiringBackend struct {
	Backend
	log *retirements
}

func (b *retiringBackend) Retire(ctx context.Context) {
	deadline, _ := ctx.Deadline()
	b.log.mu.Lock()
	defer b.log.mu.Unlock()
	b.log.order = append(b.log.order, b.Name())
	b.log.deadlines = append(b.log.deadlines, deadline)
}

func newRetiringBackend(t *testing.T, name, provider string, log *retirements) *retiringBackend {
	t.Helper()
	o := bo.New()
	o.Provider = provider
	b, err := New(name, o, nil, lm.NewRouter(), nil)
	require.NoError(t, err)
	return &retiringBackend{Backend: b, log: log}
}

func TestBackendsRetireVirtualFirst(t *testing.T) {
	log := &retirements{}
	plain, err := New("plain", bo.New(), nil, lm.NewRouter(), nil)
	require.NoError(t, err)
	b := Backends{
		"member-a": newRetiringBackend(t, "member-a", providers.Prometheus, log),
		"alb":      newRetiringBackend(t, "alb", providers.ALB, log),
		"member-b": newRetiringBackend(t, "member-b", providers.Prometheus, log),
		"rule":     newRetiringBackend(t, "rule", providers.Rule, log),
		"plain":    plain,
		"nil":      nil,
	}
	start := time.Now()
	b.Retire(time.Minute)
	require.Len(t, log.order, 4, "each backend holding anything retires once")
	// what a virtual backend runs reads through the others, so every one retires before any of them
	require.ElementsMatch(t, []string{"alb", "rule"}, log.order[:2])
	require.ElementsMatch(t, []string{"member-a", "member-b"}, log.order[2:])
	// they share one deadline, so the wait is bounded in all
	for _, d := range log.deadlines {
		require.Equal(t, log.deadlines[0], d)
		require.WithinDuration(t, start.Add(time.Minute), d, 10*time.Second)
	}
	Backends(nil).Retire(time.Minute)
}

func TestRetire(t *testing.T) {
	log := &retirements{}
	Retire(context.Background(), newRetiringBackend(t, "member", providers.Prometheus, log))
	Retire(context.Background(), "holds nothing")
	require.Equal(t, []string{"member"}, log.order)
}

// funcBackend is a backend of provider whose retirement runs retire
type funcBackend struct {
	Backend
	retire func(ctx context.Context)
}

func (b *funcBackend) Retire(ctx context.Context) { b.retire(ctx) }

func newFuncBackend(t *testing.T, name, provider string, retire func(ctx context.Context)) *funcBackend {
	t.Helper()
	return &funcBackend{Backend: newRetiringBackend(t, name, provider, nil).Backend, retire: retire}
}

func TestBackendsRetireWaitsForVirtualBackendsFirst(t *testing.T) {
	release := make(chan struct{})
	var memberRetired atomic.Bool
	b := Backends{
		"alb":    newFuncBackend(t, "alb", providers.ALB, func(context.Context) { <-release }),
		"member": newFuncBackend(t, "member", providers.Prometheus, func(context.Context) { memberRetired.Store(true) }),
	}
	done := make(chan struct{})
	go func() {
		b.Retire(time.Minute)
		close(done)
	}()
	require.Never(t, memberRetired.Load, retireTestPause, time.Millisecond,
		"a member retired while a virtual backend reading through it was still retiring")
	close(release)
	select {
	case <-done:
	case <-time.After(retireTestWait):
		t.Fatal("the retirement never ended")
	}
	require.True(t, memberRetired.Load())
}

func TestBackendsRetireGivesEachBackendTheWholeWait(t *testing.T) {
	const wait = 100 * time.Millisecond
	ended := make(chan struct{})
	var entered sync.WaitGroup
	entered.Add(1)
	quickErr := make(chan error, 1)
	b := Backends{
		// one ignores the deadline until after it passes, as work that ignores cancellation can
		"stuck": newFuncBackend(t, "stuck", providers.ALB, func(ctx context.Context) {
			entered.Done()
			<-ctx.Done()
			<-ended
		}),
		"quick": newFuncBackend(t, "quick", providers.ALB, func(ctx context.Context) {
			entered.Wait()
			quickErr <- ctx.Err()
		}),
		"panics": newFuncBackend(t, "panics", providers.Rule, func(context.Context) { panic("retire") }),
	}
	done := make(chan struct{})
	go func() {
		b.Retire(wait)
		close(done)
	}()
	select {
	case err := <-quickErr:
		require.NoError(t, err, "a backend found the shared deadline spent by another's work")
	case <-time.After(retireTestWait):
		t.Fatal("a backend never retired while another was stuck")
	}
	time.Sleep(wait)
	close(ended)
	select {
	case <-done:
	case <-time.After(retireTestWait):
		t.Fatal("the retirement never ended")
	}
}
