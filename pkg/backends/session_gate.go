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

package backends

import (
	"net/netip"
	"sync/atomic"
)

// DenialReason says why a SessionGate refused a session
type DenialReason uint8

const (
	// DenialLocation refuses a session for where its client is
	DenialLocation DenialReason = iota + 1
)

// Denial is a refusal for a protocol server to put in its own error form
type Denial struct {
	Reason  DenialReason
	Message string
}

// SessionGate judges a native protocol session before it is served, and before any credential is checked
type SessionGate interface {
	// Admit returns nil to admit a session from client, or why it is refused. The Denial is shared, so a
	// caller must not change it.
	Admit(client netip.Addr) *Denial
}

// SessionGateSlot holds a protocol server's SessionGate, which a reload may swap while sessions are judged;
// its zero value admits every session
type SessionGateSlot struct {
	gate atomic.Pointer[sessionGateBox]
}

type sessionGateBox struct {
	gate SessionGate
}

// Store replaces the held gate; a nil gate admits every session
func (s *SessionGateSlot) Store(g SessionGate) {
	if g == nil {
		s.gate.Store(nil)
		return
	}
	s.gate.Store(&sessionGateBox{gate: g})
}

// Holds reports whether the slot holds a gate, so a caller can skip finding a client address when it does not
func (s *SessionGateSlot) Holds() bool {
	return s != nil && s.gate.Load() != nil
}

// Admit judges a session from client by the held gate, at the cost of one atomic load when none is held
func (s *SessionGateSlot) Admit(client netip.Addr) *Denial {
	if s == nil {
		return nil
	}
	b := s.gate.Load()
	if b == nil {
		return nil
	}
	return b.gate.Admit(client)
}
