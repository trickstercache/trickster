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

// Package redisstorage keeps ACME accounts, certificates and locks in Redis, so instances
// sharing the keyspace share certificates and coordinate issuance.
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
	"time"

	"github.com/caddyserver/certmagic"
	redis "github.com/redis/go-redis/v9"
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
)

// ErrLockNotHeld is returned when unlocking or renewing a lock this Storage does not hold
var ErrLockNotHeld = errors.New("acme storage lock is not held by this instance")

var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)

var refreshScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0`)

var (
	_ certmagic.Storage          = (*Storage)(nil)
	_ certmagic.LockLeaseRenewer = (*Storage)(nil)
)

// Storage implements certmagic.Storage on Redis. Every key shares one hash tag, so the keyspace
// lives in a single cluster slot and multi-key operations stay valid on Redis Cluster.
type Storage struct {
	client  redis.Cmdable
	prefix  string
	lockTTL time.Duration
	mtx     sync.Mutex
	held    map[string]*heldLock
}

type heldLock struct {
	token string
	stop  chan struct{}
	done  chan struct{}
}

// New returns a Storage that namespaces its keys under keyPrefix and leases locks for lockTTL
func New(client redis.Cmdable, keyPrefix string, lockTTL time.Duration) *Storage {
	return &Storage{
		client:  client,
		prefix:  "{" + keyPrefix + "}",
		lockTTL: lockTTL,
		held:    make(map[string]*heldLock),
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

// Store puts value at key, recording its modification time
func (s *Storage) Store(ctx context.Context, key string, value []byte) error {
	_, err := s.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, s.valueKey(key), fieldValue, value,
			fieldModified, strconv.FormatInt(time.Now().UnixNano(), 10))
		p.SAdd(ctx, s.indexKey(), key)
		return nil
	})
	return err
}

// Load returns the value at key, or fs.ErrNotExist
func (s *Storage) Load(ctx context.Context, key string) ([]byte, error) {
	b, err := s.client.HGet(ctx, s.valueKey(key), fieldValue).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, fs.ErrNotExist
	}
	return b, err
}

// Delete removes key and, when key is a directory, every key beneath it
func (s *Storage) Delete(ctx context.Context, key string) error {
	keys, err := s.keysUnder(ctx, key, true)
	if err != nil {
		return err
	}
	keys = append(keys, key)
	_, err = s.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		for _, k := range keys {
			p.Del(ctx, s.valueKey(k))
			p.SRem(ctx, s.indexKey(), k)
		}
		return nil
	})
	return err
}

// Exists reports whether key is a file or a directory holding files
func (s *Storage) Exists(ctx context.Context, key string) bool {
	n, err := s.client.Exists(ctx, s.valueKey(key)).Result()
	if err == nil && n > 0 {
		return true
	}
	keys, err := s.keysUnder(ctx, key, true)
	return err == nil && len(keys) > 0
}

// List returns the keys under path: every file beneath it when recursive, or else its children
func (s *Storage) List(ctx context.Context, path string, recursive bool) ([]string, error) {
	keys, err := s.keysUnder(ctx, path, recursive)
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
	vals, err := s.client.HMGet(ctx, s.valueKey(key), fieldValue, fieldModified).Result()
	if err != nil {
		return certmagic.KeyInfo{}, err
	}
	if len(vals) == 2 && vals[0] != nil {
		info := certmagic.KeyInfo{Key: key, IsTerminal: true}
		if v, ok := vals[0].(string); ok {
			info.Size = int64(len(v))
		}
		if m, ok := vals[1].(string); ok {
			if ns, err := strconv.ParseInt(m, 10, 64); err == nil {
				info.Modified = time.Unix(0, ns)
			}
		}
		return info, nil
	}
	if s.Exists(ctx, key) {
		return certmagic.KeyInfo{Key: key}, nil
	}
	return certmagic.KeyInfo{}, fs.ErrNotExist
}

func (s *Storage) keysUnder(ctx context.Context, dir string, recursive bool) ([]string, error) {
	members, err := s.client.SMembers(ctx, s.indexKey()).Result()
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
		ok, err := s.client.SetNX(ctx, s.lockKey(name), token, s.lockTTL).Result()
		if err != nil {
			return err
		}
		if ok {
			s.hold(name, token)
			return nil
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
			_, _ = refreshScript.Run(context.Background(), s.client, []string{s.lockKey(name)},
				h.token, s.lockTTL.Milliseconds()).Result()
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
	n, err := releaseScript.Run(ctx, s.client, []string{s.lockKey(name)}, h.token).Int()
	if err != nil {
		return err
	}
	if n == 0 {
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
	n, err := refreshScript.Run(ctx, s.client, []string{s.lockKey(name)}, h.token,
		leaseDuration.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLockNotHeld
	}
	return nil
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
