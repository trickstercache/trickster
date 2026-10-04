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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tt "github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	pathopts "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

const (
	// globalOriginHost names the in-process origin that evaluates a global-mode ALB's cache misses
	globalOriginHost   = "tsm-global.trickstercache.org"
	globalOriginScheme = "http"
)

// globalTenantHeaders are the tenant headers of the products that serve raw series, which partition
// cached global results so no tenant reads another's
var globalTenantHeaders = []string{"X-Scope-OrgID", "THANOS-TENANT"}

// cachedGlobal answers global queries from the ALB's cache, evaluating only what the cache lacks
type cachedGlobal struct {
	client *Client
	paths  map[string]*pathopts.Options
}

// newCachedGlobal builds a Prometheus client over the ALB's cache whose origin is e itself, reached in
// process, so cache misses and extents the cache lacks are what e evaluates
func (e *globalEvaluator) newCachedGlobal(results backends.TSMGlobalCache) (*cachedGlobal, error) {
	o := results.Backend.Clone()
	o.Provider = providers.Prometheus
	o.ALBOptions = nil
	o.HealthCheck = nil
	o.OriginURL = globalOriginScheme + "://" + globalOriginHost
	o.Scheme, o.Host, o.PathPrefix = globalOriginScheme, globalOriginHost, ""
	o.CacheKeyPrefix = e.cacheKeyPrefix(results.Backend)
	o.Prometheus = &po.Options{InstantRound: tt.Duration(po.DefaultInstantRound)}
	o.HTTPClient = &http.Client{Transport: inProcessTransport{handler: http.HandlerFunc(e.serveOrigin)}}
	client, err := NewClientWithHooks(o.Name, o, nil, results.Cache, Hooks{
		CacheKeyHeaders: globalTenantHeaders, ProxyOnlyWarning: global.ErrMemberUnavailable.Error(),
	})
	if err != nil {
		return nil, err
	}
	o.Paths = client.DefaultPathConfigs(o)
	cg := &cachedGlobal{client: client, paths: make(map[string]*pathopts.Options, 2)}
	for _, path := range []string{APIPath + mnQueryRange, APIPath + mnQuery} {
		if pc := o.Paths.Match(http.MethodGet, path); pc != nil {
			// the evaluator's Cache-Control stands, since only it knows when a result is partial
			pc = pc.Clone()
			delete(pc.ResponseHeaders, headers.NameCacheControl)
			cg.paths[path] = pc
		}
	}
	return cg, nil
}

// cacheKeyPrefix digests what changes a global result besides the query itself: the ALB's members,
// replica labels and deduplication
func (e *globalEvaluator) cacheKeyPrefix(alb *bo.Options) string {
	h := sha256.New()
	for _, part := range e.settings.ReplicaLabels {
		h.Write([]byte(part + "\x00"))
	}
	h.Write([]byte("\x01" + e.settings.Deduplication + "\x01"))
	if alb != nil && alb.ALBOptions != nil {
		for _, m := range alb.ALBOptions.Pool {
			h.Write([]byte(m.Name + "\x00"))
		}
	}
	return globalOriginHost + "." + hex.EncodeToString(h.Sum(nil)[:8])
}

// ServeHTTP answers r from the cache, giving the cached client fresh resources for the request
func (cg *cachedGlobal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if i := strings.LastIndex(path, APIPath); i > 0 {
		path = path[i:]
	}
	pc, ok := cg.paths[path]
	if !ok {
		failures.HandleNotFound(w, r)
		return
	}
	body, err := request.GetBody(r)
	if err != nil {
		failures.HandleBadRequestResponse(w, r)
		return
	}
	o := cg.client.Configuration()
	c := cg.client.Cache()
	rsc := request.NewResources(o, pc, c.Configuration(), c, cg.client, nil)
	rsc.RequestBody = body
	r = request.SetResources(r, rsc)
	if path == APIPath+mnQueryRange {
		cg.client.QueryRangeHandler(w, r)
		return
	}
	cg.client.QueryHandler(w, r)
}

// inProcessTransport answers requests with a handler rather than a network round trip
type inProcessTransport struct {
	handler http.Handler
}

func (t inProcessTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := &bufferedResponse{header: make(http.Header)}
	t.handler.ServeHTTP(w, r)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return &http.Response{
		Status:        strconv.Itoa(w.status) + " " + http.StatusText(w.status),
		StatusCode:    w.status,
		Proto:         r.Proto,
		ProtoMajor:    r.ProtoMajor,
		ProtoMinor:    r.ProtoMinor,
		Header:        w.header,
		Body:          io.NopCloser(bytes.NewReader(w.body.Bytes())),
		ContentLength: int64(w.body.Len()),
		Request:       r,
	}, nil
}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}
