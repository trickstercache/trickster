/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

// Package routing is the Trickster Request Router
package routing

import (
	"github.com/gorilla/mux"
)

// Router is the HTTP Routing Object
var Router = mux.NewRouter()

// TLSRouter is the HTTPS Routing Object
var TLSRouter = mux.NewRouter()
