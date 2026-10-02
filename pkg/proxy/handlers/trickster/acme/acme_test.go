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

package acme

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testPath   = "/trickster/acme"
	testDomain = "www.acme.test"
	testIssuer = "le"
)

var (
	errUnmanaged = errors.New("unmanaged")
	errInvalid   = errors.New("invalid")
)

type fakeManager struct {
	renewed []string
}

func (f *fakeManager) Domains() map[string]string {
	return map[string]string{testDomain: testIssuer}
}

func (f *fakeManager) Renew(domain string) error {
	switch domain {
	case testDomain:
		f.renewed = append(f.renewed, domain)
		return nil
	case "bad":
		return errInvalid
	}
	return errUnmanaged
}

type nilDomains struct{ fakeManager }

func (nilDomains) Domains() map[string]string { return nil }

func call(h http.HandlerFunc, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestHandlerFunc(t *testing.T) {
	m := &fakeManager{}
	h := HandlerFunc(m, errUnmanaged)
	w := call(h, http.MethodGet, testPath)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"domains":{"www.acme.test":"le"}}`, w.Body.String())

	w = call(h, http.MethodPost, testPath+"?domain="+testDomain)
	require.Equal(t, http.StatusAccepted, w.Code)
	require.Equal(t, []string{testDomain}, m.renewed)
	require.Equal(t, http.StatusNotFound, call(h, http.MethodPost, testPath+"?domain=other.acme.test").Code)
	require.Equal(t, http.StatusBadRequest, call(h, http.MethodPost, testPath+"?domain=bad").Code)
	require.Equal(t, http.StatusBadRequest, call(h, http.MethodPost, testPath).Code)
	w = call(h, http.MethodDelete, testPath)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Equal(t, allowed, w.Header().Get("Allow"))
}

func TestHandlerFuncWithoutManager(t *testing.T) {
	h := HandlerFunc(nil, errUnmanaged)
	w := call(h, http.MethodGet, testPath)
	require.JSONEq(t, `{"domains":{}}`, w.Body.String())
	require.Equal(t, http.StatusNotFound, call(h, http.MethodPost, testPath+"?domain="+testDomain).Code)
	w = call(HandlerFunc(&nilDomains{}, errUnmanaged), http.MethodGet, testPath)
	require.JSONEq(t, `{"domains":{}}`, w.Body.String())
}
