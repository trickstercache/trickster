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

package ratelimit

import (
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
)

// LookupOptions returns the shared limiter a validated definition describes, as Lookup does
func LookupOptions(o *options.Options) *Limiter {
	return Lookup(o.Name, Shape{
		Keys: EncodeKeys(o.Keys), IPv6Prefix: o.IPv6Prefix, Window: time.Duration(o.Window),
		Limit: uint32(o.Limit), MaxKeys: o.MaxKeys, // #nosec G115 -- Validate bounds limit to uint32
	}, Policy{Missing: missingOf(o.MissingKey), OnFull: onFullOf(o.MaxKeysAction)})
}

func missingOf(v string) MissingKey {
	if v == options.MissingShared {
		return MissingShared
	}
	return MissingExempt
}

func onFullOf(v string) MaxKeysAction {
	if v == options.OnFullReject {
		return MaxKeysReject
	}
	return MaxKeysAllow
}
