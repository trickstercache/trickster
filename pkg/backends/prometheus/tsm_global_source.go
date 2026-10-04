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
	"errors"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
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
	switch api := o.Prometheus.EffectiveSeriesAPI(); api {
	case po.SeriesAPIRemoteRead:
		client := c.HTTPClient()
		if client == nil {
			return nil, fmt.Errorf("%w: backend %s has no http client", errNoSeriesAPI, c.Name())
		}
		return global.NewRemoteRead(u, client, o.Paths), nil
	case po.SeriesAPIStoreAPI:
		tlsConfig, err := o.TLS.ToClientTLSConfig()
		if err != nil {
			return nil, err
		}
		return global.NewStoreAPI(u, tlsConfig, o.Paths)
	default:
		return nil, fmt.Errorf("%w: %s", errSeriesAPIUnsupported, api)
	}
}

var _ backends.TSMGlobalSourceProvider = (*Client)(nil)
