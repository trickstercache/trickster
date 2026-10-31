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

// Package redisstorage keeps ACME accounts, certificates and locks in Redis or Valkey, so
// instances sharing the keyspace share certificates and coordinate issuance.
package redisstorage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/valkey-io/valkey-go"
)

const (
	fieldValue    = "v"
	fieldModified = "m"
	indexKey      = "index"
	lockPrefix    = "lock:"
	valuePrefix   = "kv:"
	tokenBytes    = 16
	pathSeparator = "/"
	minimumPoll   = 50 * time.Millisecond
	maximumPoll   = time.Second

	// a Valkey server without its Lua engine answers EVAL with this, and an older one without
	// scripting answers that the command is unknown
	errNoScriptingEngine = "could not find scripting engine"
	errUnknownCommand    = "unknown command"
)

// ErrLockNotHeld is returned when unlocking or renewing a lock this Storage does not hold
var ErrLockNotHeld = errors.New("acme storage lock is not held by this instance")

var releaseScript = valkey.NewLuaScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)

var refreshScript = valkey.NewLuaScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0`)

var (
	_ certmagic.Storage          = (*Storage)(nil)
	_ certmagic.LockLeaseRenewer = (*Storage)(nil)
)

// ClientSource returns the client that a Storage sends its commands on, or an error while the
// server is not yet connected
type ClientSource interface {
	Client() (valkey.Client, error)
}

// Storage implements certmagic.Storage on Redis or Valkey. Every key shares one hash tag, so the
// keyspace lives in a single cluster slot and multi-key operations stay valid on a cluster.
type Storage struct {
	src     ClientSource
	prefix  string
	lockTTL time.Duration
	mtx     sync.Mutex
	held    map[string]*heldLock
	// nativeLocks allows releasing and refreshing locks with Valkey's DELIFEQ and SET IFEQ
	// when the server has no Lua; noLua records that a script found it missing
	nativeLocks bool
	noLua       atomic.Bool
}

type heldLock struct {
	token string
	stop  chan struct{}
	done  chan struct{}
}

// New returns a Storage that namespaces its keys under keyPrefix and leases locks for lockTTL.
// nativeLocks lets a Valkey server without Lua release and refresh locks with native commands.
func New(src ClientSource, keyPrefix string, lockTTL time.Duration, nativeLocks bool) *Storage {
	return &Storage{
		src:         src,
		prefix:      "{" + keyPrefix + "}",
		lockTTL:     lockTTL,
		held:        make(map[string]*heldLock),
		nativeLocks: nativeLocks,
	}
}

func (s *Storage) valueKey(key string) string {
	return s.prefix + valuePrefix + key
}

func (s *Storage) indexKey() string {
	return s.prefix + indexKey
}

func (s *Storage) lockKey(name string) string {
	return s.prefix + lockPrefix + name
}

// tx runs cmds in one MULTI/EXEC transaction and returns the first error among them
func tx(ctx context.Context, c valkey.Client, cmds ...valkey.Completed) error {
	multi := make(valkey.Commands, 0, len(cmds)+2)
	multi = append(multi, c.B().Multi().Build())
	multi = append(multi, cmds...)
	multi = append(multi, c.B().Exec().Build())
	res := c.DoMulti(ctx, multi...)
	for _, r := range res[:len(res)-1] {
		if err := r.Error(); err != nil {
			return err
		}
	}
	replies, err := res[len(res)-1].ToArray()
	if err != nil {
		return err
	}
	for _, r := range replies {
		if err := r.Error(); err != nil {
			return err
		}
	}
	return nil
}

// Store puts value at key, recording its modification time
func (s *Storage) Store(ctx context.Context, key string, value []byte) error {
	c, err := s.src.Client()
	if err != nil {
		return err
	}
	return tx(ctx, c,
		c.B().Hset().Key(s.valueKey(key)).FieldValue().FieldValue(fieldValue, valkey.BinaryString(value)).
			FieldValue(fieldModified, strconv.FormatInt(time.Now().UnixNano(), 10)).Build(),
		c.B().Sadd().Key(s.indexKey()).Member(key).Build())
}

// Load returns the value at key, or fs.ErrNotExist
func (s *Storage) Load(ctx context.Context, key string) ([]byte, error) {
	c, err := s.src.Client()
	if err != nil {
		return nil, err
	}
	b, err := c.Do(ctx, c.B().Hget().Key(s.valueKey(key)).Field(fieldValue).Build()).AsBytes()
	if valkey.IsValkeyNil(err) {
		return nil, fs.ErrNotExist
	}
	return b, err
}

// Delete removes key and, when key is a directory, every key beneath it
func (s *Storage) Delete(ctx context.Context, key string) error {
	c, err := s.src.Client()
	if err != nil {
		return err
	}
	keys, err := s.keysUnder(ctx, c, key, true)
	if err != nil {
		return err
	}
	keys = append(keys, key)
	cmds := make([]valkey.Completed, 0, len(keys)*2)
	for _, k := range keys {
		cmds = append(cmds, c.B().Del().Key(s.valueKey(k)).Build(), c.B().Srem().Key(s.indexKey()).Member(k).Build())
	}
	return tx(ctx, c, cmds...)
}

// Exists reports whether key is a file or a directory holding files
func (s *Storage) Exists(ctx context.Context, key string) bool {
	c, err := s.src.Client()
	if err != nil {
		return false
	}
	return s.exists(ctx, c, key)
}

func (s *Storage) exists(ctx context.Context, c valkey.Client, key string) bool {
	n, err := c.Do(ctx, c.B().Exists().Key(s.valueKey(key)).Build()).AsInt64()
	if err == nil && n > 0 {
		return true
	}
	keys, err := s.keysUnder(ctx, c, key, true)
	return err == nil && len(keys) > 0
}

// List returns the keys under path: every file beneath it when recursive, or else its children
func (s *Storage) List(ctx context.Context, path string, recursive bool) ([]string, error) {
	c, err := s.src.Client()
	if err != nil {
		return nil, err
	}
	keys, err := s.keysUnder(ctx, c, path, recursive)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fs.ErrNotExist
	}
	return keys, nil
}

// Stat describes key, which may be a file or a directory
func (s *Storage) Stat(ctx context.Context, key string) (certmagic.KeyInfo, error) {
	c, err := s.src.Client()
	if err != nil {
		return certmagic.KeyInfo{}, err
	}
	vals, err := c.Do(ctx, c.B().Hmget().Key(s.valueKey(key)).Field(fieldValue, fieldModified).Build()).ToArray()
	if err != nil {
		return certmagic.KeyInfo{}, err
	}
	if len(vals) == 2 && !vals[0].IsNil() {
		info := certmagic.KeyInfo{Key: key, IsTerminal: true}
		if v, err := vals[0].ToString(); err == nil {
			info.Size = int64(len(v))
		}
		if m, err := vals[1].ToString(); err == nil {
			if ns, err := strconv.ParseInt(m, 10, 64); err == nil {
				info.Modified = time.Unix(0, ns)
			}
		}
		return info, nil
	}
	if s.exists(ctx, c, key) {
		return certmagic.KeyInfo{Key: key}, nil
	}
	return certmagic.KeyInfo{}, fs.ErrNotExist
}

func (s *Storage) keysUnder(ctx context.Context, c valkey.Client, dir string, recursive bool) ([]string, error) {
	members, err := c.Do(ctx, c.B().Smembers().Key(s.indexKey()).Build()).AsStrSlice()
	if err != nil {
		return nil, err
	}
	prefix := ""
	if dir != "" {
		prefix = strings.TrimSuffix(dir, pathSeparator) + pathSeparator
	}
	out := make([]string, 0, len(members))
	for _, m := range members {
		rest, ok := strings.CutPrefix(m, prefix)
		if !ok || rest == "" {
			continue
		}
		if !recursive {
			if child, _, found := strings.Cut(rest, pathSeparator); found {
				m = prefix + child
			}
		}
		out = append(out, m)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Lock blocks until the named lock is acquired or ctx ends; a held lock's lease is refreshed in
// the background until Unlock, so a crashed holder blocks others for at most one lease
func (s *Storage) Lock(ctx context.Context, name string) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	poll := min(max(s.lockTTL/10, minimumPoll), maximumPoll)
	for {
		c, err := s.src.Client()
		if err != nil {
			return err
		}
		err = c.Do(ctx, c.B().Set().Key(s.lockKey(name)).Value(token).Nx().
			PxMilliseconds(s.lockTTL.Milliseconds()).Build()).Error()
		if err == nil {
			s.hold(name, token)
			return nil
		}
		if !valkey.IsValkeyNil(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (s *Storage) hold(name, token string) {
	h := &heldLock{token: token, stop: make(chan struct{}), done: make(chan struct{})}
	s.mtx.Lock()
	s.held[name] = h
	s.mtx.Unlock()
	go s.keepAlive(name, h)
}

func (s *Storage) keepAlive(name string, h *heldLock) {
	defer close(h.done)
	t := time.NewTicker(s.lockTTL / 3)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			// a lost lease is not fatal here: the holder learns of it on Unlock
			_, _ = s.refresh(context.Background(), name, h.token, s.lockTTL)
		}
	}
}

// Unlock releases a lock this Storage holds
func (s *Storage) Unlock(ctx context.Context, name string) error {
	s.mtx.Lock()
	h, ok := s.held[name]
	delete(s.held, name)
	s.mtx.Unlock()
	if !ok {
		return ErrLockNotHeld
	}
	close(h.stop)
	<-h.done
	released, err := s.release(ctx, name, h.token)
	if err != nil {
		return err
	}
	if !released {
		return ErrLockNotHeld
	}
	return nil
}

// RenewLockLease extends a held lock's lease to leaseDuration
func (s *Storage) RenewLockLease(ctx context.Context, name string, leaseDuration time.Duration) error {
	s.mtx.Lock()
	h, ok := s.held[name]
	s.mtx.Unlock()
	if !ok {
		return ErrLockNotHeld
	}
	renewed, err := s.refresh(ctx, name, h.token, leaseDuration)
	if err != nil {
		return err
	}
	if !renewed {
		return ErrLockNotHeld
	}
	return nil
}

// release deletes the named lock if token still holds it
func (s *Storage) release(ctx context.Context, name, token string) (bool, error) {
	c, err := s.src.Client()
	if err != nil {
		return false, err
	}
	key := s.lockKey(name)
	if !s.noLua.Load() {
		n, err := releaseScript.Exec(ctx, c, []string{key}, []string{token}).AsInt64()
		if !s.lacksLua(err) {
			return n == 1, err
		}
	}
	n, err := c.Do(ctx, c.B().Delifeq().Key(key).Value(token).Build()).AsInt64()
	return n == 1, err
}

// refresh extends the named lock's lease to lease if token still holds it
func (s *Storage) refresh(ctx context.Context, name, token string, lease time.Duration) (bool, error) {
	c, err := s.src.Client()
	if err != nil {
		return false, err
	}
	key := s.lockKey(name)
	ms := lease.Milliseconds()
	if !s.noLua.Load() {
		n, err := refreshScript.Exec(ctx, c, []string{key}, []string{token, strconv.FormatInt(ms, 10)}).AsInt64()
		if !s.lacksLua(err) {
			return n == 1, err
		}
	}
	// a SET IFEQ to the held token is a no-op on the value that renews only its lease
	err = c.Do(ctx, c.B().Set().Key(key).Value(token).Ifeq(token).PxMilliseconds(ms).Build()).Error()
	if valkey.IsValkeyNil(err) {
		return false, nil
	}
	return err == nil, err
}

// lacksLua reports whether err says the server can't run Lua, and if native lock commands may
// take over, switches to them for this Storage's lifetime
func (s *Storage) lacksLua(err error) bool {
	if err == nil || !s.nativeLocks {
		return false
	}
	ve, ok := valkey.IsValkeyErr(err)
	if !ok {
		return false
	}
	msg := strings.ToLower(ve.Error())
	if !strings.Contains(msg, errNoScriptingEngine) && !strings.Contains(msg, errUnknownCommand) {
		return false
	}
	s.noLua.Store(true)
	return true
}

// Close stops refreshing held locks and releases them
func (s *Storage) Close() error {
	s.mtx.Lock()
	names := make([]string, 0, len(s.held))
	for name := range s.held {
		names = append(names, name)
	}
	s.mtx.Unlock()
	var errs []error
	for _, name := range names {
		if err := s.Unlock(context.Background(), name); err != nil && !errors.Is(err, ErrLockNotHeld) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
