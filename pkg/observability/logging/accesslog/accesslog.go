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

// Package accesslog provides per-backend, format-configurable HTTP access
// and error logging to rotation-managed files.
package accesslog

import (
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/format"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
)

// Logger writes formatted access log lines for one backend, and error log
// lines for responses at or above the error threshold
type Logger struct {
	access    *manager.Handle
	errlog    *manager.Handle
	accessFmt *format.Formatter
	errFmt    *format.Formatter
	threshold int
	backend   string
	provider  string
}

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 512)
		return &b
	},
}

const maxPooledBufferSize = 64 * 1024

// NewLogger returns a Logger for the provided options, or nil when neither
// an access log nor an error log filename is configured
func NewLogger(o *alo.Options, instanceID int,
	backend, provider string,
) (*Logger, error) {
	if !o.IsEnabled() {
		return nil, nil
	}
	l := &Logger{
		backend:   backend,
		provider:  provider,
		threshold: o.ResolvedErrorThreshold(),
	}
	var err error
	if l.accessFmt, err = format.ParseFormat(o.ResolvedFormat()); err != nil {
		return nil, err
	}
	if l.errFmt, err = format.ParseFormat(o.ResolvedErrorFormat()); err != nil {
		return nil, err
	}
	if o.Filename != "" {
		mo := o.AccessManagerOptions()
		mo.Filename = manager.InstanceFilename(mo.Filename, instanceID)
		if l.access, err = manager.GetWriter(mo); err != nil {
			return nil, err
		}
	}
	if o.ErrorFilename != "" {
		mo := o.ErrorManagerOptions()
		mo.Filename = manager.InstanceFilename(mo.Filename, instanceID)
		if l.errlog, err = manager.GetWriter(mo); err != nil {
			l.Close()
			return nil, err
		}
	}
	track(l)
	return l, nil
}

// Log writes the request described by the Fields to the configured logs
func (l *Logger) Log(f *format.Fields) {
	f.Backend = l.backend
	f.Provider = l.provider
	if l.access != nil {
		l.render(l.access, l.accessFmt, f)
	}
	if l.errlog != nil && f.Status >= l.threshold {
		l.render(l.errlog, l.errFmt, f)
	}
}

// NeedsResultHeader reports whether either configured log emits result fields.
func (l *Logger) NeedsResultHeader() bool {
	return l != nil && (l.access != nil && l.accessFmt.NeedsResultHeader() ||
		l.errlog != nil && l.errFmt.NeedsResultHeader())
}

func (l *Logger) render(w *manager.Handle, fm *format.Formatter, f *format.Fields) {
	bp := bufPool.Get().(*[]byte)
	b := fm.Render((*bp)[:0], f)
	w.Write(b)
	if cap(b) <= maxPooledBufferSize {
		*bp = b
		bufPool.Put(bp)
	}
}

// Close releases the Logger's log file handles
func (l *Logger) Close() {
	if l == nil {
		return
	}
	if l.access != nil {
		l.access.Close()
	}
	if l.errlog != nil {
		l.errlog.Close()
	}
}
