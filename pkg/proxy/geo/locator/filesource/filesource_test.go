/*
 * Copyright 2026 The Trickster Authors
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

package filesource

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/watchers/filesystem"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const (
	testInterval = 10 * time.Millisecond
	waitTimeout  = 5 * time.Second
	waitTick     = 5 * time.Millisecond
	goodContent  = "good"
	badContent   = "bad"
)

var errBad = errors.New("bad content")

type loader struct {
	mtx    sync.Mutex
	loaded string
}

func (l *loader) load(contents [][]byte) error {
	if string(contents[0]) == badContent {
		return errBad
	}
	l.mtx.Lock()
	l.loaded = string(contents[0])
	l.mtx.Unlock()
	return nil
}

func (l *loader) current() string {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	return l.loaded
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o600))
	require.NoError(t, os.Rename(tmp, path))
}

func TestWatch(t *testing.T) {
	const name = "filesource-watch"
	path := filepath.Join(t.TempDir(), "data")
	writeFile(t, path, goodContent)
	l := &loader{}
	success := metrics.GeoLocatorReloads.WithLabelValues(name, ResultSuccess)
	failure := metrics.GeoLocatorReloads.WithLabelValues(name, ResultError)
	successes, failures := testutil.ToFloat64(success), testutil.ToFloat64(failure)

	w, err := Watch(name, []string{path}, testInterval, l.load)
	require.NoError(t, err)
	defer w.Close()
	require.Equal(t, goodContent, l.current())
	require.Equal(t, successes, testutil.ToFloat64(success), "the first load is not a reload")

	writeFile(t, path, "better")
	require.Eventually(t, func() bool { return l.current() == "better" }, waitTimeout, waitTick)
	require.Eventually(t, func() bool { return testutil.ToFloat64(success) == successes+1 }, waitTimeout, waitTick)

	writeFile(t, path, badContent)
	require.Eventually(t, func() bool { return testutil.ToFloat64(failure) == failures+1 }, waitTimeout, waitTick)
	require.Equal(t, "better", l.current())

	require.NoError(t, os.Remove(path))
	require.Eventually(t, func() bool { return testutil.ToFloat64(failure) >= failures+2 }, waitTimeout, waitTick)
	writeFile(t, path, "best")
	require.Eventually(t, func() bool { return l.current() == "best" }, waitTimeout, waitTick)
}

func TestWatchFirstLoadFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	_, err := Watch("filesource-first", []string{path}, testInterval, (&loader{}).load)
	require.ErrorIs(t, err, os.ErrNotExist)
	writeFile(t, path, badContent)
	_, err = Watch("filesource-first", []string{path}, testInterval, (&loader{}).load)
	require.ErrorIs(t, err, errBad)
	_, err = Watch("filesource-first", nil, testInterval, (&loader{}).load)
	require.ErrorIs(t, err, filesystem.ErrNoPaths)
}

func TestReadErrors(t *testing.T) {
	s := &source{name: "filesource-read", success: &countingInc{}, failure: &countingInc{}}
	s.onReadError(os.ErrNotExist)
	require.ErrorIs(t, s.firstErr, os.ErrNotExist)
	s.started = true
	s.onReadError(filesystem.ErrChangedDuringRead)
	require.Zero(t, s.failure.(*countingInc).n, "a file read mid-write is not a failure")
	s.onReadError(os.ErrPermission)
	s.onReadError(os.ErrPermission)
	require.Equal(t, 2, s.failure.(*countingInc).n)
	require.True(t, s.failing)
}

type countingInc struct{ n int }

func (c *countingInc) Inc() { c.n++ }
