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

package drain

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testWait = 5 * time.Second

func TestGate(t *testing.T) {
	var g Gate
	require.True(t, g.Enter())
	require.True(t, g.Enter())
	g.Exit()
	// a bounded wait ends with work still admitted
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.False(t, g.Wait(ctx))
	require.False(t, g.Enter(), "a closed gate admits nothing")
	g.Close()
	ended := make(chan bool)
	go func() { ended <- g.Wait(context.Background()) }()
	select {
	case <-ended:
		t.Fatal("the wait ended before the admitted work did")
	case <-time.After(20 * time.Millisecond):
	}
	g.Exit()
	select {
	case ok := <-ended:
		require.True(t, ok)
	case <-time.After(testWait):
		t.Fatal("the wait never ended")
	}
}

func TestGateAdmitsWhileOpen(t *testing.T) {
	var g Gate
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				if g.Enter() {
					g.Exit()
				}
			}
		})
	}
	wg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		defer cancel()
		require.True(t, g.Wait(ctx))
	})
	wg.Wait()
	require.False(t, g.Enter())
}

func TestWait(t *testing.T) {
	var g Group
	require.True(t, Wait(context.Background(), &g), "nothing to wait for")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.Add(1)
	require.False(t, Wait(ctx, &g))
	g.Done()
	g.Wait()
}

func TestWaitAfterTheContextEnded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// however the waiting goroutine is scheduled, nothing under way is nothing left
	for range 1000 {
		var g Group
		require.True(t, Wait(ctx, &g))
		g.Add(2)
		g.Done()
		g.Done()
		require.True(t, Wait(ctx, &g), "work that ended before the wait is not left")
		var gate Gate
		require.True(t, gate.Wait(ctx))
	}
	var gate Gate
	require.True(t, gate.Enter())
	gate.Exit()
	require.True(t, gate.Wait(ctx), "work the gate admitted and saw end is not left")
}

func TestWaitSeesWorkEndAsTheContextEnds(t *testing.T) {
	// a wait already under way that sees ctx end before its waiting goroutine sees the work end still finds it ended
	for range 100 {
		var g Group
		g.Add(1)
		ctx, cancel := context.WithCancel(context.Background())
		ended := make(chan bool)
		go func() { ended <- Wait(ctx, &g) }()
		time.Sleep(100 * time.Microsecond)
		g.Done()
		cancel()
		select {
		case ok := <-ended:
			require.True(t, ok, "the work ended by the time the wait saw ctx end")
		case <-time.After(testWait):
			t.Fatal("the wait never ended")
		}
	}
}

func TestWaitWithNothingUnderWayAllocatesNothing(t *testing.T) {
	var g Group
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Zero(t, testing.AllocsPerRun(100, func() { Wait(ctx, &g) }))
}
