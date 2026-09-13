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

package discovery

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

// fakeRunner records lifecycle transitions
type fakeRunner struct {
	launched atomic.Int32
	stopped  atomic.Int32
	ctx      context.Context
}

func (f *fakeRunner) Launch(ctx context.Context) {
	f.ctx = ctx
	f.launched.Add(1)
}

func (f *fakeRunner) Stop() { f.stopped.Add(1) }

func TestLifecycle(t *testing.T) {
	var runners []*fakeRunner
	l := NewLifecycle("d1", func(q *options.Query, _ SnapshotHandler) (SubscriptionRunner, error) {
		if q.Path == "reject" {
			return nil, errors.New("rejected")
		}
		r := &fakeRunner{}
		runners = append(runners, r)
		return r, nil
	})
	require.Equal(t, "d1", l.Name())
	handler := func(Snapshot) {}

	// nil inputs are rejected
	_, err := l.Subscribe(nil, handler)
	require.Error(t, err)
	_, err = l.Subscribe(&options.Query{}, nil)
	require.Error(t, err)

	// constructor errors propagate and register nothing
	_, err = l.Subscribe(&options.Query{Path: "reject"}, handler)
	require.Error(t, err)
	require.Empty(t, runners)

	// a subscription before Start is held, then launched by Start
	unsubA, err := l.Subscribe(&options.Query{Path: "a"}, handler)
	require.NoError(t, err)
	require.Equal(t, int32(0), runners[0].launched.Load())
	require.NoError(t, l.Start(t.Context()))
	require.Equal(t, int32(1), runners[0].launched.Load())
	require.NoError(t, l.Start(t.Context()), "Start is idempotent")
	require.Equal(t, int32(1), runners[0].launched.Load(),
		"idempotent Start must not relaunch")

	// a subscription after Start launches immediately
	_, err = l.Subscribe(&options.Query{Path: "b"}, handler)
	require.NoError(t, err)
	require.Equal(t, int32(1), runners[1].launched.Load())

	// queries are cloned: caller mutation cannot reach the runner
	q := &options.Query{Path: "c", Selector: map[string]string{"k": "v"}}
	_, err = l.Subscribe(q, handler)
	require.NoError(t, err)
	q.Selector["k"] = "mutated"

	// unsubscribe stops exactly that runner
	unsubA()
	require.Equal(t, int32(1), runners[0].stopped.Load())
	require.Equal(t, int32(0), runners[1].stopped.Load())
	unsubA()
	require.Equal(t, int32(2), runners[0].stopped.Load(),
		"redundant unsubscribe reaches the runner, which must be idempotent")

	// Stop stops the remaining runners and cancels the shared context
	require.NoError(t, l.Stop())
	require.Equal(t, int32(1), runners[1].stopped.Load())
	require.Equal(t, int32(1), runners[2].stopped.Load())
	require.Error(t, runners[1].ctx.Err(), "Start context canceled on Stop")
	require.NoError(t, l.Stop(), "Stop is idempotent")

	// stopped lifecycles reject further use with ErrStopped
	_, err = l.Subscribe(&options.Query{Path: "d"}, handler)
	require.ErrorIs(t, err, ErrStopped)
	require.ErrorIs(t, l.Start(t.Context()), ErrStopped)
}

func TestEmitter(t *testing.T) {
	var got []Snapshot
	e := NewEmitter(func(s Snapshot) { got = append(got, s) })

	e.Emit(Snapshot{{Name: "b", Address: "h:2"}, {Name: "a", Address: "h:1"}})
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0][0].Name, "snapshots are canonicalized")

	// same membership in a different order is suppressed
	e.Emit(Snapshot{{Name: "a", Address: "h:1"}, {Name: "b", Address: "h:2"}})
	require.Len(t, got, 1)

	// changed membership is delivered
	e.Emit(Snapshot{{Name: "a", Address: "h:1"}})
	require.Len(t, got, 2)

	// nothing is delivered after Stop
	e.Stop()
	e.Emit(Snapshot{{Name: "c", Address: "h:3"}})
	require.Len(t, got, 2)
}
