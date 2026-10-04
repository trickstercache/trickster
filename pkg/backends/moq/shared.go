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

package moq

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
)

type shared struct {
	key    string
	relay  *relay.Relay
	dialer *dialer
	cache  atomic.Pointer[Client]

	mu       sync.Mutex
	sessions int
	retired  bool
	closed   bool
}

var (
	sharedMu sync.Mutex
	byName   = map[string]*shared{}
	live     = map[*shared]struct{}{}
)

func relayKey(o *bo.Options) string {
	// a reload keeps a backend's relay, its live edge and upstream subscriptions, unless one of these changed
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|", o.OriginURL, o.MaxTTL)
	if t := o.TLS; t != nil {
		fmt.Fprintf(&b, "%t|%s|%s|%s|%s|%t|%s|", t.InsecureSkipVerify, strings.Join(t.CertificateAuthorityPaths, ","),
			t.ClientCertPath, t.ClientKeyPath, t.ServerName, t.ExcludeSystemRoots, checksum.Checksum(t.CertificateAuthorityPEM))
	}
	m := o.MoQ
	if m == nil {
		m = mo.New()
	}
	fmt.Fprintf(&b, "%t|%d|%d|%v|%t|%d|%d|%d|%d|%d", m.Spill, m.FillTimeout, m.MaxObjectSizeBytes, m.Upstream.Drafts,
		m.Upstream.Prewarm, m.Upstream.ReconnectBackoff, m.Upstream.Linger, m.Upstream.MaxGapFetches,
		m.LiveEdge.Groups, m.LiveEdge.MaxSizeBytes)
	return b.String()
}

func sharedFor(name, key string, build func() (*shared, error)) (*shared, error) {
	// a client of an earlier configuration whose settings match hands its relay on
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sh := byName[name]; sh != nil && sh.key == key && !sh.isClosed() {
		sh.mu.Lock()
		sh.retired = false
		sh.mu.Unlock()
		return sh, nil
	}
	sh, err := build()
	if err != nil {
		return nil, err
	}
	sh.key = key
	byName[name] = sh
	live[sh] = struct{}{}
	return sh, nil
}

func (sh *shared) isClosed() bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.closed
}

// Cache returns the cache of the newest client using the relay, so a reload that renames the cache is honored
func (sh *shared) Cache() cache.Cache {
	if c := sh.cache.Load(); c != nil {
		return c.Backend.Cache()
	}
	return nil
}

func (sh *shared) hold() {
	sh.mu.Lock()
	sh.sessions++
	sh.mu.Unlock()
}

func (sh *shared) drop() {
	sh.mu.Lock()
	sh.sessions--
	now := sh.retired && sh.sessions <= 0 && !sh.closed
	if now {
		sh.closed = true
	}
	sh.mu.Unlock()
	if now {
		sh.shutdown()
	}
}

func (sh *shared) retire() {
	// a retired relay serves its sessions to their end, then closes
	sh.mu.Lock()
	sh.retired = true
	now := sh.sessions <= 0 && !sh.closed
	if now {
		sh.closed = true
	}
	sh.mu.Unlock()
	if now {
		sh.shutdown()
	}
}

func (sh *shared) close() {
	sh.mu.Lock()
	already := sh.closed
	sh.closed = true
	sh.mu.Unlock()
	if !already {
		sh.shutdown()
	}
}

func (sh *shared) shutdown() {
	sharedMu.Lock()
	delete(live, sh)
	for name, cur := range byName {
		if cur == sh {
			delete(byName, name)
		}
	}
	sharedMu.Unlock()
	if sh.relay != nil {
		sh.relay.Close()
	}
	if sh.dialer != nil {
		sh.dialer.close()
	}
}

// RetireClients retires every relay the committed clients no longer use; each closes once its sessions end
func RetireClients(current backends.Backends) {
	keep := make(map[*shared]struct{})
	for _, c := range current {
		if mc, ok := c.(*Client); ok && mc.sh != nil {
			keep[mc.sh] = struct{}{}
		}
	}
	sharedMu.Lock()
	var retire []*shared
	for sh := range live {
		if _, ok := keep[sh]; !ok {
			retire = append(retire, sh)
		}
	}
	sharedMu.Unlock()
	for _, sh := range retire {
		sh.retire()
	}
}

// StopClients closes every relay, ending their sessions; it is called when the process stops
func StopClients() {
	sharedMu.Lock()
	all := make([]*shared, 0, len(live))
	for sh := range live {
		all = append(all, sh)
	}
	sharedMu.Unlock()
	for _, sh := range all {
		sh.close()
	}
}
