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

package file

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type snapCollector struct {
	ch chan discovery.Snapshot
}

func newSnapCollector() *snapCollector {
	return &snapCollector{ch: make(chan discovery.Snapshot, 16)}
}

func (c *snapCollector) handle(s discovery.Snapshot) { c.ch <- s }

func (c *snapCollector) next(t *testing.T) discovery.Snapshot {
	t.Helper()
	select {
	case s := <-c.ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for snapshot")
		return nil
	}
}

func (c *snapCollector) expectNone(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case s := <-c.ch:
		t.Fatalf("expected no snapshot, got %d members", len(s))
	case <-time.After(within):
	}
}

// writeAtomic replaces path via the write-temp-then-rename idiom the
// provider documents for writers
func writeAtomic(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))
	require.NoError(t, os.Rename(tmp, path))
}

const membersV1 = `
- name: prom-1
  address: 10.0.0.1:9090
- name: prom-2
  scheme: https
  address: 10.0.0.2:9090
  path_prefix: /base
  weight: 3
`

func newWatchedFile(t *testing.T) (string, *snapCollector, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "members.yaml")
	writeAtomic(t, path, membersV1)
	d, err := New("test-file", nil)
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	t.Cleanup(func() { d.Stop() })
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{Path: path}, col.handle)
	require.NoError(t, err)
	return path, col, unsub
}

func TestFileDiscovery(t *testing.T) {
	path, col, unsub := newWatchedFile(t)
	defer unsub()

	snap := col.next(t)
	require.Len(t, snap, 2)
	require.Equal(t, "prom-1", snap[0].Name)
	require.Equal(t, "http", snap[0].Scheme, "scheme defaults to http")
	require.Equal(t, "10.0.0.1:9090", snap[0].Address)
	require.Equal(t, "prom-2", snap[1].Name)
	require.Equal(t, "https", snap[1].Scheme)
	require.Equal(t, "/base", snap[1].PathPrefix)
	require.Equal(t, 3, snap[1].Weight)

	// live-edit via atomic rename is applied
	writeAtomic(t, path, "- address: 10.0.0.3:9090\n")
	snap = col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.3:9090", snap[0].Name,
		"unnamed entries take their address as name")

	// an empty file is a valid, empty membership
	writeAtomic(t, path, "")
	require.Empty(t, col.next(t))
}

func TestFileDiscoveryPartialWriteKeepsLastGood(t *testing.T) {
	path, col, unsub := newWatchedFile(t)
	defer unsub()
	require.Len(t, col.next(t), 2)

	// a non-atomic writer's partial state: invalid YAML must not emit and
	// must not clear the last-good membership; the failure counts in the
	// refresh-errors metric
	errs0 := testutil.ToFloat64(metrics.DiscoveryRefreshErrors.
		WithLabelValues("test-file", "file"))
	require.NoError(t, os.WriteFile(path, []byte("- name: [broken"), 0o644))
	col.expectNone(t, 600*time.Millisecond)
	require.Greater(t, testutil.ToFloat64(metrics.DiscoveryRefreshErrors.
		WithLabelValues("test-file", "file")), errs0)

	// the completed write is then applied
	writeAtomic(t, path, "- address: 10.0.0.9:9090\n")
	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.9:9090", snap[0].Address)
}

func TestFileDiscoveryEntryValidation(t *testing.T) {
	for _, bad := range []string{
		"- name: no-address\n",
		"- address: not-host-port\n",
		"- address: 10.0.0.1:9090\n  scheme: gopher\n",
		"- address: 10.0.0.1:9090\n  weight: -1\n",
	} {
		_, err := parseMembers([]byte(bad))
		require.Error(t, err, "expected error for %q", bad)
	}
	snap, err := parseMembers([]byte("- address: 10.0.0.1:9090\n"))
	require.NoError(t, err)
	require.Len(t, snap, 1)
}

func TestFileDiscoveryJSON(t *testing.T) {
	snap, err := parseMembers(
		[]byte(`[{"name": "j1", "address": "10.0.0.1:9090", "weight": 2}]`))
	require.NoError(t, err)
	require.Len(t, snap, 1)
	require.Equal(t, "j1", snap[0].Name)
	require.Equal(t, 2, snap[0].Weight)
}

func TestFileDiscoveryMissingFile(t *testing.T) {
	d, err := New("test-file", nil)
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()
	col := newSnapCollector()
	path := filepath.Join(t.TempDir(), "members.yaml")
	unsub, err := d.Subscribe(&do.Query{Path: path}, col.handle)
	require.NoError(t, err)
	defer unsub()

	// nothing emitted while the file is absent; membership arrives once a
	// writer creates it
	col.expectNone(t, 400*time.Millisecond)
	writeAtomic(t, path, "- address: 10.0.0.1:9090\n")
	require.Len(t, col.next(t), 1)
}

func TestSubscribeErrors(t *testing.T) {
	d, err := New("test-file", nil)
	require.NoError(t, err)
	_, err = d.Subscribe(nil, nil)
	require.Error(t, err)
	_, err = d.Subscribe(&do.Query{}, func(discovery.Snapshot) {})
	require.Error(t, err, "path is required")
	require.NoError(t, d.Stop())
	_, err = d.Subscribe(&do.Query{Path: "/x"}, func(discovery.Snapshot) {})
	require.ErrorIs(t, err, ErrStopped)
}

func TestFileDiscoveryReplicaGroup(t *testing.T) {
	snap, err := parseMembers([]byte(`
- name: prom-a
  address: 10.0.0.1:9090
  replica_group: shard-0
- name: prom-b
  address: 10.0.0.2:9090
`))
	require.NoError(t, err)
	require.Len(t, snap, 2)
	require.Equal(t, "shard-0", snap[0].ReplicaGroup)
	require.Empty(t, snap[1].ReplicaGroup)
}

func TestPollIntervalOption(t *testing.T) {
	require.Equal(t, do.DefaultFilePollInterval, pollIntervalFor(nil))
	require.Equal(t, do.DefaultFilePollInterval,
		pollIntervalFor(&do.Options{Provider: "file"}))
	require.Equal(t, 2*time.Second, pollIntervalFor(&do.Options{
		Provider: "file",
		File:     &do.FileOptions{PollInterval: timeconv.Duration(2 * time.Second)},
	}))
}

// TestPollDetectsLateCreatedDirectory subscribes to a member file whose
// parent directory does not exist yet, so event watching cannot arm; the
// operator-configurable content-comparing poll (and the watcher's
// directory re-arming) must pick the file up once a writer creates it --
// the same fallback that carries filesystems without reliable change
// notification (NFS-backed kubernetes volumes, some FUSE mounts)
func TestPollDetectsLateCreatedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-yet")
	path := filepath.Join(dir, "members.yaml")

	d, err := New("poll-only", &do.Options{Provider: "file",
		File: &do.FileOptions{PollInterval: timeconv.Duration(50 * time.Millisecond)}})
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{Path: path}, col.handle)
	require.NoError(t, err)
	defer unsub()

	// nothing to deliver while the directory is absent
	col.expectNone(t, 300*time.Millisecond)

	// a writer creates the directory and file; the poll picks it up
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t,
		os.WriteFile(path, []byte("- address: 10.0.0.1:9090\n"), 0o644))
	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.1:9090", snap[0].Address)

	// subsequent changes are also observed
	require.NoError(t,
		os.WriteFile(path, []byte("- address: 10.0.0.2:9090\n"), 0o644))
	snap = col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.2:9090", snap[0].Address)
}
