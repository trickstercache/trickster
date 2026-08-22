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
)

// maxProbeBody bounds what a probe or find response may return; a probe
// asks for one or two points and a find for one level of the tree
const maxProbeBody = 4 << 20

// Origin issues synthetic requests to the Graphite origin on behalf of the
// resolver. It is deliberately minimal: GET, a path under the backend's
// base URL, query parameters, and the backend's HTTP client and timeout.
type Origin struct {
	// Base is the backend's upstream base URL
	Base *url.URL
	// Client is the backend's HTTP client
	Client *http.Client
	// Timeout bounds each request (0 = rely on the client)
	Timeout time.Duration
	// Headers are added to every request (e.g., authorization)
	Headers http.Header
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
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if len(body) > 200 {
			body = body[:200]
		}
		return nil, &errStatus{code: resp.StatusCode, body: string(body)}
	}
	return body, nil
}
