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

// Package certificates provides a read-only mgmt handler that reports the
// TLS certificate inventory of each running listener. The output includes
// only certificate metadata (ids, subjects, expiry, source kind, last load
// time) and never any key material.
package certificates

import (
	"encoding/json"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
)

type inventory struct {
	Listeners map[string][]tr.EntryInfo `json:"listeners"`
}

// HandlerFunc responds to the HTTP request with the per-listener TLS
// certificate inventory
func HandlerFunc(lg *listener.Group) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		out := inventory{Listeners: make(map[string][]tr.EntryInfo)}
		if lg != nil {
			for _, key := range lg.Keys() {
				l := lg.Get(key)
				if l == nil || l.CertSwapper() == nil {
					continue
				}
				store, ok := l.CertSwapper().(tr.CertStore)
				if !ok {
					continue
				}
				out.Listeners[key] = store.Entries()
			}
		}
		w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(out)
	}
}
