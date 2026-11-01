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

package redis

import "errors"

// ErrInvalidEndpointConfig indicates an invalid endpoint config
var ErrInvalidEndpointConfig = errors.New("invalid 'endpoint' config")

// ErrInvalidEndpointsConfig indicates an invalid endpoints config
var ErrInvalidEndpointsConfig = errors.New("invalid 'endpoints' config")

// ErrInvalidSentinalMasterConfig indicates an invalid sentinel_master config
var ErrInvalidSentinalMasterConfig = errors.New("invalid 'sentinel_master' config")

// ErrInvalidProtocolConfig indicates a protocol other than tcp or unix
var ErrInvalidProtocolConfig = errors.New("invalid 'protocol' config")

// ErrNotConnected indicates a command was made before a client could connect
var ErrNotConnected = errors.New("not connected to the redis or valkey server")
