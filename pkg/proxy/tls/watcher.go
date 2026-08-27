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

package tls

import (
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/watchers"
	"github.com/trickstercache/trickster/v2/pkg/watchers/filesystem"
)

// FileSet identifies the set of related TLS source files that are watched
// and validated as one unit, so a mid-rotation partial state (e.g. cert file
// updated before key file) is never swapped into a live listener.
type FileSet struct {
	CertPath string
	KeyPath  string
	// CAPaths optionally names CA bundle files associated with the same
	// identity; a change to any of them re-fires the load callback.
	CAPaths []string
}

// Key returns the stable source identity for the FileSet
func (fs FileSet) Key() string {
	parts := append([]string{SourceKindFile + ":" + fs.CertPath, fs.KeyPath}, fs.CAPaths...)
	return strings.Join(parts, "|")
}

// paths returns all files in the set, cert and key first
func (fs FileSet) paths() []string {
	return append([]string{fs.CertPath, fs.KeyPath}, fs.CAPaths...)
}

// NewFileSetWatcher starts a filesystem watcher (see watchers/filesystem for
// the hybrid event+poll detection strategy) over the provided FileSet,
// layering TLS pair-coherence validation on top: onLoad is never invoked
// with an invalid or mismatched pair — a mid-rotation partial state keeps
// the last-good certificate serving and is retried once the pair settles.
// An initial load is performed synchronously: if the set is currently valid,
// onLoad is invoked with the loaded Entry before this function returns, and
// each subsequent detected-and-validated change invokes it again from the
// watch goroutine. If interval is <= 0, no watcher is started and nil is
// returned. Watchers are control-plane only: nothing here runs on the
// handshake path.
func NewFileSetWatcher(fs FileSet, interval time.Duration,
	onLoad func(*Entry),
) watchers.Watcher {
	if interval <= 0 {
		return nil
	}
	entryID := fs.CertPath
	w, err := filesystem.StartNew(&filesystem.Options{
		Name:     entryID,
		Paths:    fs.paths(),
		Interval: interval,
		OnChange: func(contents [][]byte) error {
			cert, err := ValidatePair(contents[0], contents[1])
			if err != nil {
				// likely a mid-rotation partial state; keep serving the
				// last-good pair and retry once the content settles
				metrics.TLSCertificateValidationFailures.WithLabelValues(entryID).Inc()
				logger.Debug("tls certificate change failed validation", logging.Pairs{
					"entry": entryID, "detail": err.Error(),
				})
				return err
			}
			if onLoad != nil {
				onLoad(NewEntry(fs.Key(), SourceKindFile, cert))
			}
			return nil
		},
		OnReadError: func(_ error) {
			metrics.TLSWatcherErrors.WithLabelValues(entryID).Inc()
		},
	})
	if err != nil {
		// unreachable with a validated interval and a non-empty FileSet
		logger.Error("unable to start tls certificate watcher", logging.Pairs{
			"entry": entryID, "detail": err.Error(),
		})
		return nil
	}
	return w
}
