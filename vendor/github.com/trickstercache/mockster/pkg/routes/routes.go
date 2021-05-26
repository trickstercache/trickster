/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

// Package routes handles the mockster routes
package routes

import (
	"net/http"

	"github.com/trickstercache/mockster/pkg/mocks/byterange"
	"github.com/trickstercache/mockster/pkg/mocks/prometheus"
)

// GetRouter returns a Router wtih the Mockster routes
func GetRouter() *http.ServeMux {
	mux := http.NewServeMux()
	prometheus.InsertRoutes(mux)
	byterange.InsertRoutes(mux)
	return mux
}
