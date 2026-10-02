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

package prometheus

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/mocks/promsim"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"

	"github.com/stretchr/testify/require"
)

// the simulated origin parses a form body only on POST, so a 200 shows the QUERY reached it as POST
func TestQueryRangeHandlerQueryMethod(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200, "", nil,
		tu.PromSimBackendProvider, promsim.PathQueryRange, "error")
	require.NoError(t, err)
	defer ts.Close()
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, rsc.CacheClient, nil, nil)
	require.NoError(t, err)
	client := backendClient.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = client.HTTPClient()
	h := middleware.QueryAsPost(queryMediaTypes(), http.HandlerFunc(client.QueryRangeHandler))

	end := time.Now().Truncate(time.Minute)
	form := url.Values{
		"query": {`query_method_test{series_id="1"}`},
		"step":  {"60"},
		"start": {strconv.FormatInt(end.Add(-time.Hour).Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
	}.Encode()
	run := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(methods.MethodQuery, ts.URL+promsim.PathQueryRange,
			strings.NewReader(form))
		req.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
		// each request carries its own body cache, as it would when served
		rq := rsc.Clone()
		rq.RequestBody = nil
		req = request.SetResources(req, rq)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	w := run()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"resultType":"matrix"`)
	require.Equal(t, `"application/x-www-form-urlencoded"`, w.Header().Get(headers.NameAcceptQuery))

	w = run()
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Header().Get(headers.NameTricksterResult), "status=hit")
}
