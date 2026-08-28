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

package dynamic

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

// TestConcurrentApplySnapshot races overlapping full-membership updates
// through one Manager (as a flapping discoverer would deliver them) and
// asserts the final state is exactly the last-applied snapshot's, with no
// stranded members. Run under -race, this is the step-29 concurrency check
// for the manager's control plane.
func TestConcurrentApplySnapshot(t *testing.T) {
	m, c, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template"})

	snapshots := make([]discovery.Snapshot, 8)
	for i := range snapshots {
		s := make(discovery.Snapshot, 5)
		for j := range s {
			s[j] = member(
				fmt.Sprintf("m%d", (i+j)%12),
				fmt.Sprintf("10.0.%d.%d:8080", i%4, j))
		}
		snapshots[i] = s
	}

	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range 50 {
				m.ApplySnapshot(snapshots[(w+i)%len(snapshots)])
			}
		}(w)
	}
	wg.Wait()

	// settle on one final, known membership
	final := discovery.Snapshot{member("final", "10.9.9.9:8080")}
	m.ApplySnapshot(final)
	require.Equal(t, []string{"myalb-final"}, m.MemberNames())
	require.Equal(t, []string{"myalb-final"}, c.DynamicPoolNames())
	statuses := hc.Statuses()
	require.Contains(t, statuses, "myalb-final")
	require.Len(t, statuses, 1,
		"no stranded health registrations after churn")
}

// TestTeardownReleasesResources adds and removes members repeatedly and
// asserts goroutines and health-check registrations return to baseline:
// the step-29 leak check for runtime instantiation/teardown.
func TestTeardownReleasesResources(t *testing.T) {
	m, c, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template"})
	// give members an actively-probing healthcheck so probe goroutine
	// teardown is exercised; 127.0.0.1:1 refuses connections immediately
	m.cfg.Template.HealthCheck.Interval = timeconv.Duration(10 * time.Millisecond)

	baseline := runtime.NumGoroutine()

	for round := range 3 {
		s := make(discovery.Snapshot, 20)
		for j := range s {
			s[j] = member(fmt.Sprintf("r%d-m%d", round, j),
				fmt.Sprintf("127.0.0.1:%d", j+1))
		}
		m.ApplySnapshot(s)
		require.Len(t, m.MemberNames(), 20)
		m.ApplySnapshot(discovery.Snapshot{})
		require.Empty(t, m.MemberNames())
	}
	require.Empty(t, c.DynamicPoolNames())
	require.Empty(t, hc.Statuses(), "all health registrations released")

	// goroutines (probe loops, pool workers, drain closers) settle back to
	// the pre-churn baseline, within scheduler noise
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline+5
	}, 5*time.Second, 25*time.Millisecond,
		"goroutines leaked: baseline %d, now %d", baseline, runtime.NumGoroutine())
}

// makeChurnSnapshot builds an n-member snapshot; gen varies the addresses
// so successive generations replace every member
func makeChurnSnapshot(n, gen int) discovery.Snapshot {
	s := make(discovery.Snapshot, n)
	for j := range s {
		s[j] = member(fmt.Sprintf("m%d", j),
			fmt.Sprintf("10.%d.%d.%d:8080", gen%2, j/250, j%250))
	}
	return s
}

// BenchmarkApplySnapshotChurn measures the full reconcile cost -- diff,
// backend instantiation, health registration, pool swap, and teardown --
// when every member of a high-cardinality pool is replaced (step 33).
func BenchmarkApplySnapshotChurn(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("members-%d", n), func(b *testing.B) {
			m, _, _ := newTestManager(b, &ao.DiscoveryOptions{
				DiscovererName: "d", TemplateBackend: "rp-template"})
			snaps := []discovery.Snapshot{
				makeChurnSnapshot(n, 0), makeChurnSnapshot(n, 1)}
			m.ApplySnapshot(snaps[0])
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				m.ApplySnapshot(snaps[(i+1)%2])
			}
		})
	}
}

// BenchmarkApplySnapshotUnchanged measures the steady-state no-op cost: a
// healthy discoverer re-emitting identical membership must be cheap
func BenchmarkApplySnapshotUnchanged(b *testing.B) {
	m, _, _ := newTestManager(b, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template"})
	s := makeChurnSnapshot(100, 0)
	m.ApplySnapshot(s)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.ApplySnapshot(s)
	}
}
