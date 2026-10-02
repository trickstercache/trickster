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

// Package acme provides the mgmt handler that lists ACME-managed domains (GET) and forces the
// renewal of one of them (POST with a domain query parameter)
package acme

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const (
	paramDomain = "domain"
	allowed     = http.MethodGet + ", " + http.MethodPost
)

// Manager is the part of the ACME manager the handler drives
type Manager interface {
	// Domains returns every managed domain with the issuer that manages it
	Domains() map[string]string
	// Renew starts a forced renewal of domain in the background
	Renew(domain string) error
}

type listing struct {
	Domains map[string]string `json:"domains"`
}

type renewal struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
}

// HandlerFunc returns the handler for m; unmanaged is the error Renew returns for an unknown domain
func HandlerFunc(m Manager, unmanaged error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		switch r.Method {
		case http.MethodGet:
			out := listing{Domains: map[string]string{}}
			if m != nil {
				if d := m.Domains(); d != nil {
					out.Domains = d
				}
			}
			writeJSON(w, http.StatusOK, out)
		case http.MethodPost:
			domain := strings.TrimSpace(r.URL.Query().Get(paramDomain))
			if domain == "" {
				http.Error(w, "the domain query parameter is required", http.StatusBadRequest)
				return
			}
			if m == nil {
				http.Error(w, "acme is not configured", http.StatusNotFound)
				return
			}
			if err := m.Renew(domain); err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, unmanaged) {
					status = http.StatusNotFound
				}
				http.Error(w, err.Error(), status)
				return
			}
			writeJSON(w, http.StatusAccepted, renewal{Domain: domain, Status: "renewal started"})
		default:
			w.Header().Set(headers.NameAllow, allowed)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
