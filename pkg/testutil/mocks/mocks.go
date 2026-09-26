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

// Package mocks serves the mock origins together. It and its subpackages import
// only the standard library, so the dev environment can build them offline.
package mocks

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/testutil/mocks/promsim"
	"github.com/trickstercache/trickster/v2/pkg/testutil/mocks/rangesim"
)

// NewRouter returns a mux serving the Prometheus simulator under /prometheus/
// and the Range request simulator under /byterange/.
func NewRouter() *http.ServeMux {
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

// Register mounts every mock origin on the provided mux.
func Register(mux *http.ServeMux) {
	promsim.Register(mux)
	rangesim.Register(mux)
}
