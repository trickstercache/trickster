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

package middleware

import (
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// sigV4AuthPrefix begins the Authorization value of a SigV4 or SigV4a signature
const sigV4AuthPrefix = "AWS4-"

// sigV4Fields are the fields a client's own SigV4 signature adds besides Authorization
var sigV4Fields = []string{
	headers.NameXAmzDate,
	headers.NameXAmzSecurityToken,
	headers.NameXAmzContentSHA256,
}

// StripSigV4 drops a client's SigV4 signature for a backend that signs requests itself, so the
// signature neither splits the cache key nor reaches the origin beside Trickster's own.
func StripSigV4(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get(headers.NameAuthorization), sigV4AuthPrefix) {
			r.Header.Del(headers.NameAuthorization)
		}
		for _, name := range sigV4Fields {
			r.Header.Del(name)
		}
		next.ServeHTTP(w, r)
	})
}
