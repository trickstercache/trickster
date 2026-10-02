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

package ondemand

import (
	"crypto/tls"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testKey     = "listener.default.https"
	testStored  = "stored.acme.test"
	testIssued  = "issued.acme.test"
	testRefused = "refused.acme.test"
)

var (
	storedCert   = &tls.Certificate{}
	issuedCert   = &tls.Certificate{}
	fallbackCert = &tls.Certificate{}
	errRefused   = errors.New("refused")
)

type fakeStore struct{}

func (fakeStore) Match(name string) *tls.Certificate {
	if name == testStored {
		return storedCert
	}
	return nil
}

type fakeProvider struct {
	calls int
}

func (p *fakeProvider) Enabled(key string) bool {
	return key == testKey
}

func (p *fakeProvider) Certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.calls++
	if hello.ServerName == testIssued {
		return issuedCert, nil
	}
	return nil, errRefused
}

func fallback(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return fallbackCert, nil
}

func TestGetCertificate(t *testing.T) {
	t.Cleanup(func() { SetProvider(nil) })
	get := GetCertificate(testKey, fakeStore{}, fallback)
	other := GetCertificate("listener.other.https", fakeStore{}, fallback)

	SetProvider(nil)
	c, err := get(&tls.ClientHelloInfo{ServerName: testIssued})
	require.NoError(t, err)
	require.Same(t, fallbackCert, c)

	p := &fakeProvider{}
	SetProvider(p)
	for name, want := range map[string]*tls.Certificate{
		testStored: storedCert, testIssued: issuedCert, testRefused: fallbackCert, "": fallbackCert,
	} {
		c, err := get(&tls.ClientHelloInfo{ServerName: name})
		require.NoError(t, err)
		require.Same(t, want, c, name)
	}
	require.Equal(t, 2, p.calls, "only names the store does not cover reach the provider")
	c, err = other(&tls.ClientHelloInfo{ServerName: testIssued})
	require.NoError(t, err)
	require.Same(t, fallbackCert, c)
	require.Equal(t, 2, p.calls)
}

func BenchmarkGetCertificateNoProvider(b *testing.B) {
	SetProvider(nil)
	get := GetCertificate(testKey, fakeStore{}, fallback)
	hello := &tls.ClientHelloInfo{ServerName: testStored}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = get(hello)
	}
}
