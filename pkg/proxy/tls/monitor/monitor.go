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

// Package monitor detects out-of-band TLS certificate rotation and hot-swaps
// renewed certificates into live listeners without restart or config reload.
package monitor

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	"github.com/trickstercache/trickster/v2/pkg/watchers"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	logKeyListenerName = "listenerName"
	logKeyEntry        = "entry"
	logKeyDetail       = "detail"
)

// Monitor owns certificate rotation watchers and swaps validated sources into
// listener certificate stores. Control-plane only; never on the handshake path.
type Monitor struct {
	mtx       sync.Mutex
	group     *listener.Group
	watchers  map[string]*watchedSet       // keyed by fileset key + interval
	cache     map[string]*tr.Entry         // last-good entry per fileset key
	listeners map[string]*listenerNotifier // keyed by listener group key
}

type watchedSet struct {
	fs       tr.FileSet
	interval time.Duration
	watcher  watchers.Watcher
}

type listenerNotifier struct {
	name     string
	groupKey string
	watch    bool
	fileSets []tr.FileSet
	memory   map[string]*tr.Entry
}

type watchSpec struct {
	key      string
	fs       tr.FileSet
	interval time.Duration
}

// New returns a new Monitor
func New() *Monitor {
	return &Monitor{
		watchers:  make(map[string]*watchedSet),
		cache:     make(map[string]*tr.Entry),
		listeners: make(map[string]*listenerNotifier),
	}
}

// Apply reconciles watchers and listener stores with conf. Call after the
// listener group has applied conf.
func (m *Monitor) Apply(conf *config.Config, lg *listener.Group) {
	if conf == nil || lg == nil {
		return
	}
	desired := make(map[string]watchSpec)
	newListeners := make(map[string]*listenerNotifier)
	backendNames := make([]string, 0, len(conf.Backends))
	for name := range conf.Backends {
		backendNames = append(backendNames, name)
	}
	slices.Sort(backendNames)
	for name, o := range conf.Listeners {
		if o == nil || !o.Active || !o.ServeTLS || o.TLSListenPort <= 0 ||
			(o.Protocol != "" && o.Protocol != listenerconfig.ProtocolHTTP) {
			continue
		}
		interval := time.Duration(o.TLSWatchInterval)
		ln := &listenerNotifier{
			name:     name,
			groupKey: listener.GroupKey(name, o.Protocol, true),
			watch:    interval > 0,
			memory:   make(map[string]*tr.Entry),
		}
		for _, backendName := range backendNames {
			b := conf.Backends[backendName]
			if !b.UsesListener(name) || b.TLS == nil || !b.TLS.ServeTLS ||
				b.TLS.FullChainCertPath == "" || b.TLS.PrivateKeyPath == "" {
				continue
			}
			fs := tr.FileSet{CertPath: b.TLS.FullChainCertPath, KeyPath: b.TLS.PrivateKeyPath}
			if slices.ContainsFunc(ln.fileSets,
				func(f tr.FileSet) bool { return f.Key() == fs.Key() }) {
				continue
			}
			ln.fileSets = append(ln.fileSets, fs)
			if ln.watch {
				desired[watchKey(fs, interval)] = watchSpec{
					key: watchKey(fs, interval), fs: fs, interval: interval,
				}
			}
		}
		if len(ln.fileSets) == 0 {
			continue
		}
		newListeners[ln.groupKey] = ln
	}

	var toClose []watchers.Watcher
	var toStart []watchSpec
	m.mtx.Lock()
	m.group = lg
	for groupKey, ln := range newListeners {
		if old, ok := m.listeners[groupKey]; ok {
			ln.memory = old.memory
		}
	}
	for groupKey, old := range m.listeners {
		if _, ok := newListeners[groupKey]; !ok {
			clearListenerMetrics(old.name)
		}
	}
	m.listeners = newListeners
	for key, ws := range m.watchers {
		if _, ok := desired[key]; !ok {
			toClose = append(toClose, ws.watcher)
			delete(m.watchers, key)
		}
	}
	for key, spec := range desired {
		if _, ok := m.watchers[key]; !ok {
			toStart = append(toStart, spec)
		}
	}
	groupKeys := make([]string, 0, len(newListeners))
	for groupKey := range newListeners {
		groupKeys = append(groupKeys, groupKey)
	}
	m.mtx.Unlock()

	for _, w := range toClose {
		w.Close()
	}
	// create outside the lock: initial load may call onLoad, which takes it
	for _, spec := range toStart {
		w := tr.NewFileSetWatcher(spec.fs, spec.interval, m.onLoad)
		if w == nil {
			continue
		}
		m.mtx.Lock()
		m.watchers[spec.key] = &watchedSet{fs: spec.fs, interval: spec.interval, watcher: w}
		m.mtx.Unlock()
	}
	slices.Sort(groupKeys)
	for _, groupKey := range groupKeys {
		m.rebuild(groupKey)
	}
}

// Close stops all watchers and waits for their goroutines to exit
func (m *Monitor) Close() {
	m.mtx.Lock()
	toClose := make([]watchers.Watcher, 0, len(m.watchers))
	for _, ws := range m.watchers {
		toClose = append(toClose, ws.watcher)
	}
	m.watchers = make(map[string]*watchedSet)
	m.mtx.Unlock()
	for _, w := range toClose {
		w.Close()
	}
}

// SetMemoryCert adds or updates an in-memory PEM pair for listenerName,
// keyed by sourceKey. Validated and hot-swapped like file sources.
func (m *Monitor) SetMemoryCert(listenerName, sourceKey string, certPEM, keyPEM []byte) error {
	cert, err := tr.ValidatePair(certPEM, keyPEM)
	if err != nil {
		metrics.TLSCertificateValidationFailures.WithLabelValues(
			memoryEntryKey(sourceKey)).Inc()
		return err
	}
	e := tr.NewEntry(memoryEntryKey(sourceKey), tr.SourceKindMemory, cert)
	m.mtx.Lock()
	ln := m.listenerByName(listenerName)
	if ln == nil {
		m.mtx.Unlock()
		return fmt.Errorf("no TLS listener named %s", listenerName)
	}
	prev := ln.memory[e.Key]
	ln.memory[e.Key] = e
	groupKey := ln.groupKey
	m.mtx.Unlock()
	m.rebuild(groupKey)
	if prev == nil || prev.ContentHash != e.ContentHash {
		logSwap(listenerName, e)
		metrics.TLSCertificateSwapsTotal.WithLabelValues(listenerName, e.Key).Inc()
	}
	return nil
}

// RemoveMemoryCert removes an in-memory certificate from the named listener.
func (m *Monitor) RemoveMemoryCert(listenerName, sourceKey string) error {
	m.mtx.Lock()
	ln := m.listenerByName(listenerName)
	if ln == nil {
		m.mtx.Unlock()
		return fmt.Errorf("no TLS listener named %s", listenerName)
	}
	delete(ln.memory, memoryEntryKey(sourceKey))
	groupKey := ln.groupKey
	m.mtx.Unlock()
	m.rebuild(groupKey)
	return nil
}

func memoryEntryKey(sourceKey string) string {
	return tr.SourceKindMemory + ":" + sourceKey
}

// requires m.mtx held
func (m *Monitor) listenerByName(name string) *listenerNotifier {
	for _, ln := range m.listeners {
		if ln.name == name {
			return ln
		}
	}
	return nil
}

func (m *Monitor) onLoad(e *tr.Entry) {
	m.mtx.Lock()
	prev := m.cache[e.Key]
	m.cache[e.Key] = e
	changed := prev != nil && prev.ContentHash != e.ContentHash
	affected := make([]*listenerNotifier, 0, len(m.listeners))
	for _, ln := range m.listeners {
		if ln.watch && slices.ContainsFunc(ln.fileSets,
			func(f tr.FileSet) bool { return f.Key() == e.Key }) {
			affected = append(affected, ln)
		}
	}
	m.mtx.Unlock()
	slices.SortFunc(affected, func(a, b *listenerNotifier) int {
		return cmp.Compare(a.groupKey, b.groupKey)
	})
	for _, ln := range affected {
		m.rebuild(ln.groupKey)
		if changed {
			logSwap(ln.name, e)
			metrics.TLSCertificateSwapsTotal.WithLabelValues(ln.name, e.Key).Inc()
		}
	}
}

func logSwap(listenerName string, e *tr.Entry) {
	pairs := logging.Pairs{logKeyListenerName: listenerName, logKeyEntry: e.Key}
	if leaf := e.Certificate.Leaf; leaf != nil {
		pairs["subjectAltNames"] = leaf.DNSNames
		pairs["notAfter"] = leaf.NotAfter
	}
	logger.Info("tls certificate hot-swapped into listener", pairs)
}

// rebuild replaces groupKey's cert store with last-good entries. Leaves the
// store untouched if any file set has never loaded successfully.
func (m *Monitor) rebuild(groupKey string) {
	m.mtx.Lock()
	ln, ok := m.listeners[groupKey]
	if !ok || m.group == nil {
		m.mtx.Unlock()
		return
	}
	entries := make([]*tr.Entry, 0, len(ln.fileSets)+len(ln.memory))
	for _, fs := range ln.fileSets {
		e := m.cache[fs.Key()]
		if e == nil {
			e = m.loadFileSet(fs)
		}
		if e == nil {
			// incomplete load: leave store as config path populated it
			m.mtx.Unlock()
			return
		}
		entries = append(entries, e)
	}
	memoryKeys := make([]string, 0, len(ln.memory))
	for key := range ln.memory {
		memoryKeys = append(memoryKeys, key)
	}
	slices.Sort(memoryKeys)
	for _, key := range memoryKeys {
		entries = append(entries, ln.memory[key])
	}
	group := m.group
	name := ln.name
	m.mtx.Unlock()

	l := group.Get(groupKey)
	if l == nil || l.CertSwapper() == nil {
		return
	}
	store, ok := l.CertSwapper().(tr.CertStore)
	if !ok {
		return
	}
	store.SetEntries(entries)
	clearListenerMetrics(name)
	infos := store.Entries()
	metrics.TLSCertificateStoreSize.WithLabelValues(name).Set(float64(len(infos)))
	for _, info := range infos {
		metrics.TLSCertificateNotAfter.WithLabelValues(name, info.Key).
			Set(float64(info.NotAfter.Unix()))
		metrics.TLSCertificateLastLoad.WithLabelValues(name, info.Key).
			Set(float64(info.LastLoad.Unix()))
	}
}

// loadFileSet loads and caches a file set when no watcher entry exists yet.
// Requires m.mtx held.
func (m *Monitor) loadFileSet(fs tr.FileSet) *tr.Entry {
	certPEM, err := os.ReadFile(fs.CertPath) // #nosec G703 -- path comes from operator-provided config
	if err != nil {
		logger.Warn("unable to read tls certificate source", logging.Pairs{
			logKeyEntry: fs.Key(), logKeyDetail: err.Error(),
		})
		return nil
	}
	keyPEM, err := os.ReadFile(fs.KeyPath) // #nosec G703 -- path comes from operator-provided config
	if err != nil {
		logger.Warn("unable to read tls certificate source", logging.Pairs{
			logKeyEntry: fs.Key(), logKeyDetail: err.Error(),
		})
		return nil
	}
	cert, err := tr.ValidatePair(certPEM, keyPEM)
	if err != nil {
		logger.Warn("tls certificate source failed validation", logging.Pairs{
			logKeyEntry: fs.Key(), logKeyDetail: err.Error(),
		})
		return nil
	}
	e := tr.NewEntry(fs.Key(), tr.SourceKindFile, cert)
	m.cache[fs.Key()] = e
	return e
}

func watchKey(fs tr.FileSet, interval time.Duration) string {
	return fs.Key() + "@" + interval.String()
}

func clearListenerMetrics(listenerName string) {
	labels := prometheus.Labels{"listener": listenerName}
	metrics.TLSCertificateNotAfter.DeletePartialMatch(labels)
	metrics.TLSCertificateLastLoad.DeletePartialMatch(labels)
	metrics.TLSCertificateStoreSize.Delete(labels)
}
