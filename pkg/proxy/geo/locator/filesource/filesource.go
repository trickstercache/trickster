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

// Package filesource watches the data files of file-based geo locators, loading each changed set while
// serving and keeping the last good one when a replacement fails to load.
package filesource

import (
	"errors"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/watchers/filesystem"
)

// reload result label values
const (
	ResultSuccess = "success"
	ResultError   = "error"
)

// ErrNotLoaded is returned when the first read of a locator's files delivered nothing to load
var ErrNotLoaded = errors.New("geo locator files were not loaded")

// Load replaces a locator's data with the contents of its files, in the order they are watched. It returns an
// error, and changes nothing, when the contents are not fit to serve.
type Load func(contents [][]byte) error

type source struct {
	name             string
	load             Load
	success, failure interface{ Inc() }
	mtx              sync.Mutex
	started, loaded  bool
	failing          bool
	firstErr         error
}

// Watch loads the files at paths and starts watching them, loading them again whenever they change. It fails,
// leaving nothing running, when the first load fails.
func Watch(locatorName string, paths []string, interval time.Duration, load Load) (*filesystem.Watcher, error) {
	s := &source{
		name: locatorName, load: load,
		success: metrics.GeoLocatorReloads.WithLabelValues(locatorName, ResultSuccess),
		failure: metrics.GeoLocatorReloads.WithLabelValues(locatorName, ResultError),
	}
	w, err := filesystem.StartNew(&filesystem.Options{
		Name:          "geo locator " + locatorName,
		Paths:         paths,
		Interval:      interval,
		SkipUnchanged: true,
		OnChange:      s.onChange,
		OnReadError:   s.onReadError,
	})
	if err != nil {
		return nil, err
	}
	s.mtx.Lock()
	loaded, firstErr := s.loaded, s.firstErr
	s.started = true
	s.mtx.Unlock()
	if !loaded {
		w.Close()
		if firstErr == nil {
			firstErr = ErrNotLoaded
		}
		return nil, firstErr
	}
	return w, nil
}

func (s *source) onChange(contents [][]byte) error {
	err := s.load(contents)
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if !s.started {
		s.loaded, s.firstErr = err == nil, err
		return err
	}
	if err != nil {
		s.fail("geo locator refused a replaced file and serves the last good one", err)
		return err
	}
	s.success.Inc()
	s.failing = false
	return nil
}

func (s *source) onReadError(err error) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if !s.started {
		s.firstErr = err
		return
	}
	// a file read mid-write is read again once it settles, so it is no failure to report
	if errors.Is(err, filesystem.ErrChangedDuringRead) {
		logger.Debug("geo locator file changed while it was read", logging.Pairs{keys.GeoLocator: s.name})
		return
	}
	s.fail("geo locator could not read its file and serves the last good one", err)
}

func (s *source) fail(event string, err error) {
	// every failure is counted, and only the first of a run of them warns
	s.failure.Inc()
	pairs := logging.Pairs{keys.GeoLocator: s.name, keys.Error: err.Error()}
	if s.failing {
		logger.Debug(event, pairs)
		return
	}
	s.failing = true
	logger.Warn(event, pairs)
}
