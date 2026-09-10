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
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

// newRunner builds a subscription for a path without starting it
func newRunner(t *testing.T, p *provider, path string) *subscription {
	t.Helper()
	r, err := p.newSubscription(&do.Query{Path: path},
		func(discovery.Snapshot) {})
	require.NoError(t, err)
	return r.(*subscription)
}

// Unsubscribing before the discoverer starts must leave Launch a no-op
// rather than starting a watcher nobody will ever close
func TestLaunchAfterStopDoesNothing(t *testing.T) {
	p := &provider{name: "test", pollInterval: pollIntervalFor(nil)}
	s := newRunner(t, p, filepath.Join(t.TempDir(), "members.yaml"))

	s.Stop()
	s.Stop() // idempotent
	s.Launch(t.Context())
	require.Nil(t, s.watcher)
}

// Watcher construction is unreachable with a validated subscription, but a
// failure must be logged and left inert rather than panicking on Stop
func TestLaunchWatcherConstructionFailure(t *testing.T) {
	p := &provider{name: "test"} // no poll interval: rejected by the watcher
	s := newRunner(t, p, filepath.Join(t.TempDir(), "members.yaml"))

	s.Launch(t.Context())
	require.Nil(t, s.watcher)
	require.NotPanics(t, s.Stop)
}
