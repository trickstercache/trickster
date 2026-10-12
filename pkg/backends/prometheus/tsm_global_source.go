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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	pathopts "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

var (
	errNoSeriesAPI          = errors.New("prometheus backend serves no raw series")
	errSeriesAPIUnsupported = errors.New("prometheus series_api is not supported")
)

type globalSourceResult struct {
	source backends.TSMGlobalSource
	err    error
}

// TSMGlobalSource returns the source TSM global evaluation reads the backend's raw series from.
func (c *Client) TSMGlobalSource() (backends.TSMGlobalSource, error) {
	if r := c.globalSource.Load(); r != nil {
		return r.source, r.err
	}
	r := &globalSourceResult{}
	r.source, r.err = c.newGlobalSource()
	// concurrent first callers build equal sources; every caller returns the one stored
	c.globalSource.CompareAndSwap(nil, r)
	r = c.globalSource.Load()
	return r.source, r.err
}

func (c *Client) newGlobalSource() (backends.TSMGlobalSource, error) {
	o := c.Configuration()
	if o == nil || o.Prometheus == nil || o.Prometheus.EffectiveSeriesAPI() == "" {
		return nil, errNoSeriesAPI
	}
	u, err := o.Prometheus.SeriesEndpointURL(o.OriginURL)
	if err != nil {
		return nil, err
	}
	api := o.Prometheus.EffectiveSeriesAPI()
	id := global.SourceIdentity{
		ID: globalSourceIdentity(o, api, u), Headers: globalKeyHeaders(o.Paths),
		Volatile: o.Prometheus.EffectiveSeriesVolatileWindow(),
	}
	switch api {
	case po.SeriesAPIRemoteRead:
		client := c.HTTPClient()
		if client == nil {
			return nil, fmt.Errorf("%w: backend %s has no http client", errNoSeriesAPI, c.Name())
		}
		rr := global.NewRemoteRead(u, client, o.Paths)
		rr.SourceIdentity = id
		return rr, nil
	case po.SeriesAPIStoreAPI:
		tlsConfig, err := o.TLS.ToClientTLSConfig()
		if err != nil {
			return nil, err
		}
		s, err := global.NewStoreAPI(u, tlsConfig, o.Paths)
		if err != nil {
			return nil, err
		}
		s.SourceIdentity = id
		return s, nil
	default:
		return nil, fmt.Errorf("%w: %s", errSeriesAPIUnsupported, api)
	}
}

// globalQueryRoutes returns the path configs a global evaluation's reads match, for each method and path
// the evaluator is asked on
func globalQueryRoutes(paths pathopts.List) []*pathopts.Options {
	out := make([]*pathopts.Options, 0, 4)
	for _, path := range []string{APIPath + mnQuery, APIPath + mnQueryRange} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if pc := paths.Match(method, path); pc != nil && !slices.Contains(out, pc) {
				out = append(out, pc)
			}
		}
	}
	return out
}

// globalSourceIdentity digests what decides a member's raw series besides the query: its series API and
// endpoint, the headers its query paths send, and the credentials it reads with
func globalSourceIdentity(o *bo.Options, api string, endpoint *url.URL) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	write(api, endpoint.String())
	for _, pc := range globalQueryRoutes(o.Paths) {
		write(pc.Path)
		for _, k := range slices.Sorted(maps.Keys(pc.RequestHeaders)) {
			write(k, pc.RequestHeaders[k])
		}
	}
	if o.TLS != nil {
		write(o.TLS.ClientCertPath, o.TLS.ClientKeyPath, o.TLS.ServerName)
		write(o.TLS.CertificateAuthorityPaths...)
	}
	if o.SigV4 != nil {
		write(o.SigV4.Region, o.SigV4.AccessKey, o.SigV4.Profile, o.SigV4.RoleARN, o.SigV4.GetService())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// globalKeyHeaders returns the request headers a member's query paths key their cached answers by
func globalKeyHeaders(paths pathopts.List) []string {
	var out []string
	for _, pc := range globalQueryRoutes(paths) {
		for _, name := range pc.CacheKeyHeaders {
			if name = http.CanonicalHeaderKey(name); !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	slices.Sort(out)
	return out
}

var _ backends.TSMGlobalSourceProvider = (*Client)(nil)
