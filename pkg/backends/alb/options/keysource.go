/*
 * Copyright 2026 The Trickster Authors
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
package options

import (
	"errors"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
)

// KeySource is a parsed key source.
//
// Deprecated: use flowkey.KeySource.
type KeySource = flowkey.KeySource

// KeyKind is where a flow key is read from.
//
// Deprecated: use flowkey.KeyKind.
type KeyKind = flowkey.KeyKind

// StreamListener is what decides which keys a stream listener can read.
//
// Deprecated: use flowkey.StreamListener.
type StreamListener = flowkey.StreamListener

// The kinds of key an hrw.key can name.
//
// Deprecated: use the flowkey package's kinds.
const (
	KeyClientIP = flowkey.KeyClientIP
	KeyHost     = flowkey.KeyHost
	KeyHeader   = flowkey.KeyHeader
	KeyCookie   = flowkey.KeyCookie
	KeyQuery    = flowkey.KeyQuery
	KeySNI      = flowkey.KeySNI
	KeyProxyTLV = flowkey.KeyProxyTLV
	KeyUser     = flowkey.KeyUser
)

// Key source spellings.
//
// Deprecated: use the flowkey package's spellings.
const (
	KeySourceClientIP = flowkey.KeySourceClientIP
	KeySourceHost     = flowkey.KeySourceHost
	KeySourceSNI      = flowkey.KeySourceSNI
	KeySourceUser     = flowkey.KeySourceUser
)

// ErrInvalidKeySource is returned for a key source that cannot be parsed.
//
// Deprecated: use flowkey.ErrInvalidKeySource.
var ErrInvalidKeySource = flowkey.ErrInvalidKeySource

// ParseKeySource parses a key source; the empty string is client_ip.
//
// Deprecated: use flowkey.ParseKeySource.
var ParseKeySource = flowkey.ParseKeySource

// ErrInvalidHRWKey is returned for an hrw.key that names the shape of a request, which no
// client's affinity can follow.
var ErrInvalidHRWKey = errors.New("'hrw.key' cannot be method, path or query: use client_ip, host, " +
	"header:<name>, cookie:<name>, query:<name>, sni, proxy_tlv:<type> or user")

// ParseHRWKey parses an hrw.key, which follows a client rather than the shape of a request;
// the empty string is client_ip.
func ParseHRWKey(s string) (flowkey.KeySource, error) {
	ks, err := flowkey.ParseKeySource(s)
	if err != nil {
		return ks, err
	}
	switch ks.Kind {
	case flowkey.KeyMethod, flowkey.KeyPath, flowkey.KeyRawQuery:
		return flowkey.KeySource{}, fmt.Errorf("%w: %q", ErrInvalidHRWKey, s)
	}
	return ks, nil
}
