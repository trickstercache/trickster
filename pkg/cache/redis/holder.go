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

package redis

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Holder keeps a connection's client. A cluster or sentinel client can't be made while its
// servers are unreachable, so when Connect finds them down, a background loop retries until
// it succeeds or the Holder closes; until then, Client returns ErrNotConnected.
type Holder struct {
	ref        atomic.Pointer[clientRef]
	opt        valkey.ClientOption
	minBackoff time.Duration
	maxBackoff time.Duration
	onConnect  func(valkey.Client)
	newClient  func(valkey.ClientOption) (valkey.Client, error)
	stop       chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
}

type clientRef struct {
	c valkey.Client
}

func newHolder(opt valkey.ClientOption, minBackoff, maxBackoff time.Duration,
	onConnect func(valkey.Client),
) *Holder {
	return &Holder{
		opt:        opt,
		minBackoff: minBackoff,
		maxBackoff: maxBackoff,
		onConnect:  onConnect,
		newClient:  valkey.NewClient,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// Client returns the connected client, or ErrNotConnected while there is none
func (h *Holder) Client() (valkey.Client, error) {
	if h == nil {
		return nil, ErrNotConnected
	}
	if r := h.ref.Load(); r != nil {
		return r.c, nil
	}
	return nil, ErrNotConnected
}

// connect makes the client, or starts the retry loop when it can't be made. A single-server
// client is made even when its first dial fails, as it redials on its own; the error is returned.
func (h *Holder) connect() error {
	c, err := h.newClient(h.opt)
	if c == nil {
		go h.retry()
		return err
	}
	h.ref.Store(&clientRef{c: c})
	close(h.done)
	if err == nil && h.onConnect != nil {
		h.onConnect(c)
	}
	return err
}

func (h *Holder) retry() {
	defer close(h.done)
	backoff := h.minBackoff
	t := time.NewTimer(backoff)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
		}
		if c, err := h.newClient(h.opt); err == nil {
			h.ref.Store(&clientRef{c: c})
			if h.onConnect != nil {
				h.onConnect(c)
			}
			return
		} else if c != nil {
			c.Close()
		}
		backoff = min(backoff*2, h.maxBackoff)
		t.Reset(backoff)
	}
}

// close stops the retry loop and closes the client
func (h *Holder) close() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		close(h.stop)
		<-h.done
		if r := h.ref.Swap(nil); r != nil {
			r.c.Close()
		}
	})
}
