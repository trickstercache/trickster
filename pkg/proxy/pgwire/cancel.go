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
package pgwire

import (
	"crypto/rand"
	"crypto/subtle"
	"sync"
)

// legacySecretLen is the fixed cancel-key length of protocol 3.0.
const legacySecretLen = 4

type cancelTarget struct {
	secret     []byte
	realPID    uint32
	realSecret []byte
}

type cancelRegistry struct {
	mtx     sync.Mutex
	nextPID uint32
	targets map[uint32]*cancelTarget
}

func newCancelRegistry() *cancelRegistry {
	return &cancelRegistry{targets: make(map[uint32]*cancelTarget)}
}

func (r *cancelRegistry) register(realPID uint32, realSecret []byte) (uint32, []byte, error) {
	// issues a key whose secret is as long as the origin's, so it is
	// valid for whichever protocol version the client negotiated.
	secret := make([]byte, max(len(realSecret), legacySecretLen))
	if _, err := rand.Read(secret); err != nil {
		return 0, nil, err
	}
	r.mtx.Lock()
	defer r.mtx.Unlock()
	for {
		r.nextPID++
		if _, taken := r.targets[r.nextPID]; !taken && r.nextPID != 0 {
			break
		}
	}
	r.targets[r.nextPID] = &cancelTarget{secret: secret, realPID: realPID, realSecret: realSecret}
	return r.nextPID, secret, nil
}

func (r *cancelRegistry) release(pid uint32) {
	r.mtx.Lock()
	delete(r.targets, pid)
	r.mtx.Unlock()
}

func (r *cancelRegistry) lookup(pid uint32, secret []byte) *cancelTarget {
	r.mtx.Lock()
	target := r.targets[pid]
	r.mtx.Unlock()
	if target == nil || subtle.ConstantTimeCompare(target.secret, secret) != 1 {
		return nil
	}
	return target
}
