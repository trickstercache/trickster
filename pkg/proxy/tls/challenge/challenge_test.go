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

package challenge

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testToken    = HTTPPathPrefix + "token"
	testKeyAuth  = "token.thumbprint"
	testNextBody = "next"
)

var errNoChallenge = errors.New("no challenge")

type fakeSolver struct {
	cert *tls.Certificate
	err  error
}

func (f *fakeSolver) ServeHTTPChallenge(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != testToken {
		return false
	}
	_, _ = w.Write([]byte(testKeyAuth))
	return true
}

func (f *fakeSolver) TLSALPNCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return f.cert, f.err
}

func serve(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestHTTPHandler(t *testing.T) {
	t.Cleanup(func() { SetSolver(nil) })
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(testNextBody))
	})
	h := HTTPHandler(next)

	SetSolver(nil)
	require.Equal(t, testNextBody, serve(h, "/index.html").Body.String())
	w := serve(h, testToken)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Empty(t, w.Body.String())

	SetSolver(&fakeSolver{})
	require.Equal(t, testKeyAuth, serve(h, testToken).Body.String())
	require.Equal(t, http.StatusNotFound, serve(h, HTTPPathPrefix+"other").Code)
	require.Equal(t, testNextBody, serve(h, "/.well-known/security.txt").Body.String())
}

func TestConfigForClient(t *testing.T) {
	t.Cleanup(func() { SetSolver(nil) })
	ordinary := &tls.ClientHelloInfo{SupportedProtos: []string{"h2", "http/1.1"}}
	acmeHello := &tls.ClientHelloInfo{ServerName: "www.acme.test", SupportedProtos: []string{ProtocolACMETLS1}}

	SetSolver(nil)
	c, err := ConfigForClient(ordinary)
	require.NoError(t, err)
	require.Nil(t, c)
	c, err = ConfigForClient(acmeHello)
	require.NoError(t, err)
	require.Nil(t, c)

	SetSolver(&fakeSolver{err: errNoChallenge})
	_, err = ConfigForClient(acmeHello)
	require.ErrorIs(t, err, errNoChallenge)

	SetSolver(&fakeSolver{cert: &tls.Certificate{}})
	c, err = ConfigForClient(acmeHello)
	require.NoError(t, err)
	require.Equal(t, []string{ProtocolACMETLS1}, c.NextProtos)
	require.Len(t, c.Certificates, 1)
	c, err = ConfigForClient(ordinary)
	require.NoError(t, err)
	require.Nil(t, c)
}

func BenchmarkConfigForClientOrdinary(b *testing.B) {
	SetSolver(&fakeSolver{cert: &tls.Certificate{}})
	b.Cleanup(func() { SetSolver(nil) })
	hello := &tls.ClientHelloInfo{SupportedProtos: []string{"h2", "http/1.1"}}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = ConfigForClient(hello)
	}
}
