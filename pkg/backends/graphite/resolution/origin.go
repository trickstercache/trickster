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

package resolution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
)

// maxProbeBody bounds what a probe or find response may return; a probe
// asks for one or two points and a find for one level of the tree
const maxProbeBody = 4 << 20

// Origin issues synthetic GET requests to the Graphite origin on behalf of
// the resolver, using the backend's base URL, HTTP client and timeout.
type Origin struct {
	// Base is the backend's upstream base URL
	Base *url.URL
	// Client is the backend's HTTP client
	Client *http.Client
	// Timeout bounds each request (0 = rely on the client)
	Timeout time.Duration
	// Headers are added to every request (e.g., authorization)
	Headers http.Header
	// PathOptions, when set, returns the configured request_headers and
	// request_params for the given upstream path
	PathOptions func(path string) (hdrs map[string]string, qparams map[string]string)
}

// errStatus carries a non-2xx origin status
type errStatus struct {
	code int
	body string
}

func (e *errStatus) Error() string {
	return fmt.Sprintf("origin returned HTTP %d: %s", e.code, strings.TrimSpace(e.body))
}

// Get performs a GET and returns the body. A non-2xx status is an error.
func (o *Origin) Get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	if o == nil || o.Base == nil || o.Client == nil {
		return nil, errors.New("origin not configured")
	}
	u := *o.Base
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	var pathHeaders map[string]string
	if o.PathOptions != nil {
		var qparams map[string]string
		pathHeaders, qparams = o.PathOptions(path)
		if len(qparams) > 0 {
			q = maps.Clone(q)
			params.UpdateParams(q, qparams)
		}
	}
	u.RawQuery = q.Encode()
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	maps.Copy(req.Header, o.Headers)
	if len(pathHeaders) > 0 {
		headers.UpdateHeaders(req.Header, pathHeaders)
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// read one byte past the cap: an exactly-at-cap body could otherwise be
	// a silently truncated stream whose prefix still parses as complete
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxProbeBody {
		return nil, fmt.Errorf("origin response exceeds %d bytes", maxProbeBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if len(body) > 200 {
			body = body[:200]
		}
		return nil, &errStatus{code: resp.StatusCode, body: string(body)}
	}
	return body, nil
}
