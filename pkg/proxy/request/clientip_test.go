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

package request

import (
	"net/http/httptest"
	"testing"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"

	"github.com/stretchr/testify/require"
)

func TestClientIP(t *testing.T) {
	require.Empty(t, ClientIP(nil))
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	require.Equal(t, "10.0.0.1", ClientIP(r))
	r.RemoteAddr = "10.0.0.2"
	require.Equal(t, "10.0.0.2", ClientIP(r))
	r = r.WithContext(tctx.WithClientIP(r.Context(), "203.0.113.7"))
	require.Equal(t, "203.0.113.7", ClientIP(r))
	require.Empty(t, tctx.ClientIP(nil))
}
