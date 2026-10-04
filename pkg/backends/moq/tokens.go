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
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

const (
	scopePublish   = "publish"
	scopeSubscribe = "subscribe"
	scopeAny       = "any"
	commentPrefix  = "#"

	// a changed token file is noticed within this long, without a watcher per backend
	tokenRecheck = time.Second
)

type grant struct {
	publish   bool
	subscribe bool
	namespace wire.Namespace
}

type tokenTable map[string][]grant

type tokenFile struct {
	path    string
	backend string
	table   atomic.Pointer[tokenTable]

	mu      sync.Mutex
	checked time.Time
	modTime time.Time
	size    int64
}

var _ relay.Authorizer = (*tokenFile)(nil)

func newTokenFile(backend, path string) (*tokenFile, error) {
	tf := &tokenFile{path: path, backend: backend}
	if err := tf.load(time.Now()); err != nil {
		return nil, err
	}
	return tf, nil
}

func (tf *tokenFile) load(now time.Time) error {
	fi, err := os.Stat(tf.path)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(tf.path)
	if err != nil {
		return err
	}
	table, err := parseTokens(b)
	if err != nil {
		return fmt.Errorf("moq token file %s: %w", tf.path, err)
	}
	tf.table.Store(&table)
	tf.checked, tf.modTime, tf.size = now, fi.ModTime(), fi.Size()
	return nil
}

func parseTokens(b []byte) (tokenTable, error) {
	// each line is: token scope [namespace], with '#' starting a comment line
	table := make(tokenTable)
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, commentPrefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields) > 3 {
			return nil, fmt.Errorf("line %d: want a token, a scope and an optional namespace", n)
		}
		var g grant
		switch strings.ToLower(fields[1]) {
		case scopePublish:
			g.publish = true
		case scopeSubscribe:
			g.subscribe = true
		case scopeAny:
			g.publish, g.subscribe = true, true
		default:
			return nil, fmt.Errorf("line %d: scope %q is not publish, subscribe or any", n, fields[1])
		}
		if len(fields) == 3 {
			ns, err := mo.ParseNamespace(fields[2])
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			g.namespace = ns
		}
		table[fields[0]] = append(table[fields[0]], g)
	}
	return table, sc.Err()
}

func (tf *tokenFile) current() tokenTable {
	// a file that changed is read again; one that no longer parses keeps the grants it had
	now := time.Now()
	tf.mu.Lock()
	if now.Sub(tf.checked) >= tokenRecheck {
		tf.checked = now
		if fi, err := os.Stat(tf.path); err == nil && (!fi.ModTime().Equal(tf.modTime) || fi.Size() != tf.size) {
			if err = tf.load(now); err != nil {
				logger.Warn("moq token file not reloaded", logging.Pairs{
					keys.BackendName: tf.backend, keys.Error: err.Error(),
				})
			}
		}
	}
	tf.mu.Unlock()
	return *tf.table.Load()
}

// Authorize reports whether one of tokens grants op on the tracks under namespace
func (tf *tokenFile) Authorize(op relay.Operation, namespace wire.Namespace, tokens []wire.Token) bool {
	if len(tokens) == 0 {
		return false
	}
	table := tf.current()
	for _, tok := range tokens {
		for _, g := range table[string(tok.Value)] {
			allowed := g.subscribe
			if op == relay.OpPublish {
				allowed = g.publish
			}
			if allowed && namespace.HasPrefix(g.namespace) {
				return true
			}
		}
	}
	return false
}
