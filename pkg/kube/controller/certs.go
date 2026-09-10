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

package controller

import (
	"crypto/sha256"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	corev1 "k8s.io/api/core/v1"
)

// certVerdicts caches whether each TLS Secret's material can be served, so unchanged material is
// parsed once however many listeners reference it and however many passes read it
type certVerdicts struct {
	mtx     sync.Mutex
	entries map[string]*certVerdict
	// pass numbers reconcile passes, so entries no pass references are swept
	pass uint64
	// parses counts key-pair parses, for tests
	parses uint64
}

// certVerdict is one Secret's verdict and the material it was reached about; identity is what
// the material answers for, nothing when it is unusable
type certVerdict struct {
	resourceVersion string
	digest          [sha256.Size]byte
	identity        ir.CertIdentity
	err             error
	seen            uint64
	// secret is the object judged in the pass numbered seen, whose material is what is installed
	secret *corev1.Secret
}

func newCertVerdicts() *certVerdicts {
	return &certVerdicts{entries: make(map[string]*certVerdict)}
}

func (v *certVerdicts) begin() {
	// begin starts a pass; what the pass does not reference is swept at its end
	v.mtx.Lock()
	v.pass++
	v.mtx.Unlock()
}

func (v *certVerdicts) judge(sec *corev1.Secret) (ir.CertIdentity, error) {
	// judge returns whether the Secret's pair can be served and what it answers for, parsing it
	// only when its resource version and material differ from what was last seen
	crt, key := sec.Data[corev1.TLSCertKey], sec.Data[corev1.TLSPrivateKeyKey]
	name := sec.Namespace + "/" + sec.Name
	v.mtx.Lock()
	defer v.mtx.Unlock()
	e := v.entries[name]
	if e != nil {
		e.seen, e.secret = v.pass, sec
		if sec.ResourceVersion != "" && e.resourceVersion == sec.ResourceVersion {
			return e.identity, e.err
		}
	}
	h := sha256.New()
	h.Write(crt)
	h.Write([]byte{0})
	h.Write(key)
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	if e != nil && e.digest == digest {
		e.resourceVersion = sec.ResourceVersion
		return e.identity, e.err
	}
	v.parses++
	identity, err := ir.ParseCertPair(crt, key)
	v.entries[name] = &certVerdict{
		resourceVersion: sec.ResourceVersion, digest: digest, identity: identity, err: err,
		seen: v.pass, secret: sec,
	}
	return identity, err
}

func (v *certVerdicts) judged(name string) (*corev1.Secret, ir.CertIdentity, error) {
	// judged returns the Secret judged in the current pass under the name, with its verdict and
	// what its material answers for; nil for one no translator judged this pass
	v.mtx.Lock()
	defer v.mtx.Unlock()
	e := v.entries[name]
	if e == nil || e.seen != v.pass {
		return nil, ir.CertIdentity{}, nil
	}
	return e.secret, e.identity, e.err
}

func (v *certVerdicts) sweep() {
	// sweep drops the verdicts on Secrets this pass did not reference
	v.mtx.Lock()
	defer v.mtx.Unlock()
	for name, e := range v.entries {
		if e.seen != v.pass {
			delete(v.entries, name)
		}
	}
}
