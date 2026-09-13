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

package leader

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// changes records every leadership transition an elector reports
type changes struct {
	mtx  sync.Mutex
	seen []bool
}

func (c *changes) record(leader bool) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.seen = append(c.seen, leader)
}

func (c *changes) list() []bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	return append([]bool(nil), c.seen...)
}

func elector(t *testing.T, cs kubernetes.Interface, identity string, ch *changes) *Elector {
	t.Helper()
	e, err := New(Config{
		Client: cs, Namespace: "trickster", Name: "test-lease", Identity: identity,
		LeaseDuration: 2 * time.Second, RenewDeadline: time.Second,
		RetryPeriod: 100 * time.Millisecond, OnChange: ch.record,
	})
	require.NoError(t, err)
	t.Cleanup(e.Stop)
	return e
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 10*time.Millisecond, msg)
}

func transitions(t *testing.T, ch *changes, want ...bool) {
	t.Helper()
	eventually(t, func() bool { return len(ch.list()) >= len(want) },
		"expected the leadership transitions to be reported")
	require.Equal(t, want, ch.list())
}

func TestLeaderHandoff(t *testing.T) {
	// Two replicas contend for one Lease: the first wins, the second waits, and
	// stopping the leader hands the Lease over
	cs := fake.NewClientset()
	var ca, cb changes
	a := elector(t, cs, "a", &ca)
	a.Start(t.Context())
	eventually(t, a.IsLeader, "the first elector should win an uncontended lease")

	b := elector(t, cs, "b", &cb)
	b.Start(t.Context())
	time.Sleep(500 * time.Millisecond)
	require.False(t, b.IsLeader(), "a held lease must not be taken")
	require.True(t, a.IsLeader())

	a.Stop()
	require.False(t, a.IsLeader())
	eventually(t, b.IsLeader, "the released lease should pass to the waiting elector")
	transitions(t, &ca, true, false)
	transitions(t, &cb, true)
	// starting again is a no-op and stopping twice is safe
	b.Start(t.Context())
	b.Stop()
	b.Stop()
	require.False(t, b.IsLeader())
}

func TestTermEndsWithLeadership(t *testing.T) {
	// The term context ends with the Lease, which is how a write bound to it
	// learns the replica no longer speaks for the cluster
	e := elector(t, fake.NewClientset(), "a", &changes{})
	_, ok := e.Term()
	require.False(t, ok)
	e.Start(t.Context())
	eventually(t, e.IsLeader, "an uncontended lease should be won")
	ctx, ok := e.Term()
	require.True(t, ok)
	require.NoError(t, ctx.Err())
	e.Stop()
	require.Error(t, ctx.Err())
	_, ok = e.Term()
	require.False(t, ok)
}

func TestStopBeforeStart(t *testing.T) {
	e := elector(t, fake.NewClientset(), "x", &changes{})
	e.Stop()
	require.False(t, e.IsLeader())
	require.Equal(t, "x", e.Identity())
}

func TestNewErrors(t *testing.T) {
	_, err := New(Config{})
	require.ErrorIs(t, err, ErrNoClient)
	// a renew deadline the retry period cannot meet is refused at construction
	_, err = New(Config{Client: fake.NewClientset(), Name: "l",
		LeaseDuration: time.Second, RenewDeadline: 100 * time.Millisecond,
		RetryPeriod: 100 * time.Millisecond})
	require.Error(t, err)
	// the Lease API holds whole seconds, so a shorter lease is always expired
	_, err = New(Config{Client: fake.NewClientset(), Name: "l",
		LeaseDuration: 500 * time.Millisecond, RenewDeadline: 200 * time.Millisecond,
		RetryPeriod: 50 * time.Millisecond})
	require.ErrorIs(t, err, ErrLeaseTooShort)
}

func TestIdentityDefaults(t *testing.T) {
	e, err := New(Config{Client: fake.NewClientset(), Name: "l",
		LeaseDuration: time.Second, RenewDeadline: 500 * time.Millisecond,
		RetryPeriod: 100 * time.Millisecond})
	require.NoError(t, err)
	require.NotEmpty(t, e.Identity())
	require.Contains(t, e.Identity(), "_", "the identity carries a random suffix")
	require.NotEqual(t, Identity(), Identity())
	require.False(t, strings.HasPrefix(Identity(), "_"))
	require.Equal(t, "default", e.cfg.Namespace)
}

func TestLeaderReacquiresAfterLoss(t *testing.T) {
	// A leader that cannot renew gives the Lease up, and contends again once it
	// can; the fake API accepts any update, so renewal is blocked at the client
	cs := fake.NewClientset()
	var blocked atomic.Bool
	cs.PrependReactor("update", "leases", func(ktesting.Action) (bool, runtime.Object, error) {
		if blocked.Load() {
			return true, nil, errors.New("api server unreachable")
		}
		return false, nil, nil
	})
	var ch changes
	e := elector(t, cs, "a", &ch)
	e.Start(t.Context())
	eventually(t, e.IsLeader, "an uncontended lease should be won")

	blocked.Store(true)
	eventually(t, func() bool { return !e.IsLeader() }, "a lease that cannot be renewed is lost")
	blocked.Store(false)
	// the old record names this replica but a new term does not trust it; it
	// contends again once that record has expired
	eventually(t, e.IsLeader, "the elector should contend again after losing")
	transitions(t, &ch, true, false, true)
}

func TestStartAfterStopContendsForNothing(t *testing.T) {
	cs := fake.NewClientset()
	e := elector(t, cs, "late", &changes{})
	e.Stop()
	e.Start(t.Context())
	time.Sleep(300 * time.Millisecond)
	require.False(t, e.IsLeader())
	_, err := cs.CoordinationV1().Leases("trickster").Get(t.Context(), "test-lease",
		metav1.GetOptions{})
	require.Error(t, err, "a stopped elector must not create a Lease")
}
