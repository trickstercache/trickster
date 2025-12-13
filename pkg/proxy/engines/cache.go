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
 * limitations
 */

package engines

import (
	"fmt"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	providerMemory = "memory"
)

func stripConditionalHeaders(h http.Header) {
	h.Del(headers.NameIfMatch)
	h.Del(headers.NameIfUnmodifiedSince)
	h.Del(headers.NameIfNoneMatch)
	h.Del(headers.NameIfModifiedSince)
}

func getSubKey(key string, chunkExtent timeseries.Extent) string {
	return fmt.Sprintf("%s.%s", key, chunkExtent)
}
