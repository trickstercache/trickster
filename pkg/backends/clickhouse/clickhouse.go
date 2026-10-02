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

// Package clickhouse provides the ClickHouse backend provider
package clickhouse

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	modelch "github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/model"
	chnative "github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/aftership"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var _ backends.TimeseriesBackend = (*Client)(nil)

// Client Implements the Proxy Client Interface
type Client struct {
	backends.TimeseriesBackend
	nativeClient *chnative.NativeClient
	zone         serverZone
}

var _ types.NewBackendClientFunc = NewClient

// StepAlignments returns the step alignment modes ClickHouse supports, and its default
func (c *Client) StepAlignments() (supported, def timeseries.StepAlignment) {
	return sqlanalyzer.StepAlignments, sqlanalyzer.DefaultStepAlignment
}

// NewClient returns a new Client Instance
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends,
	_ types.Lookup,
) (backends.Backend, error) {
	if o != nil {
		o.FastForwardDisable = true
	}
	c := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers, router, cache, modelch.NewModeler())
	c.TimeseriesBackend = b
	if err != nil {
		return c, err
	}
	if o != nil {
		if err := chnative.ValidateOptions(o); err != nil {
			return nil, err
		}
		if strings.EqualFold(o.Protocol, "native") {
			nc, err := chnative.NewNativeClient(o)
			if err != nil {
				return nil, err
			}
			c.nativeClient = nc
			c.HTTPClient().Transport = nc
			c.HealthCheckHTTPClient().Transport = nc
		}
		// the server's zone is learned from the responses that name it
		if next := c.HTTPClient().Transport; next != nil {
			c.HTTPClient().Transport = &zoneObserver{next: next, zone: &c.zone}
		}
	}
	return c, nil
}

// resolveZone reports whether the server's zone is known, or taken as UTC, so a request may be cached
func (c *Client) resolveZone(ctx context.Context) bool {
	return c.zone.resolve(ctx, c.HTTPClient(), c.BaseUpstreamURL())
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	var sqlQuery string
	var qi url.Values
	isBody := methods.HasBody(r.Method)
	var err error
	var originalBody []byte
	if isBody {
		originalBody, err = request.GetBody(r)
		if err != nil {
			return nil, nil, false, err
		}
		sqlQuery = string(originalBody)
	} else {
		qi = r.URL.Query()
		p, ok := qi[upQuery]
		if !ok {
			return nil, nil, false, errors.MissingURLParam(upQuery)
		}
		sqlQuery = p[0]
	}

	trq, ro, canOPC, err := parse(sqlQuery, c.observeAnalysis)
	if err != nil {
		return trq, ro, canOPC, err
	}
	if ro != nil && r.URL != nil {
		plan, _ := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
		output, err := aftership.ResolveOutputFormat(plan, r.URL.Query().Get("default_format"))
		if err != nil {
			return trq, ro, true, err
		}
		ro.OutputFormat = output
	}
	if ro != nil && r.URL != nil {
		settings := r.URL.Query()
		// a DateTime is written in the session's zone, which is the server's unless the request sets one
		zone, ok := sessionZone(settings, sqlQuery)
		if !ok {
			zone, _ = c.zone.get()
		}
		fopts := modelch.NewFormatOptions(settings, zone)
		if ro.OutputFormat == modelch.OutputFormatNative {
			raw := settings.Get("client_protocol_version")
			revision, err := strconv.ParseUint(raw, 10, 64)
			if raw != "" && err != nil {
				return trq, ro, true, ErrUnsupportedOutputFormat
			}
			fopts.Revision = revision
		}
		ro.ProviderRequest = fopts
		if plan, ok := trq.ParsedQuery.(*sqlanalyzer.QueryPlan); ok {
			// a bound read in a zone other than UTC isn't the range the analysis read
			if plan.ZonedBounds && zone != nil {
				return trq, ro, true, ErrZonedBounds
			}
			keyRendering(trq, plan, zone)
		}
	}
	if isBody && trq != nil {
		trq.OriginalBody = originalBody
	}
	var bf time.Duration
	res := request.GetResources(r)
	if res == nil {
		// 60-second default volatile window for ClickHouse
		bf = time.Minute
	} else {
		bf = time.Duration(res.BackendOptions.VolatileWindow)
	}
	if trq.VolatileWindow == 0 {
		trq.VolatileWindow = bf
	}
	trq.TemplateURL = urls.Clone(r.URL)

	if isBody {
		request.SetBody(r, []byte(trq.Statement))
	} else {
		// Swap in the Tokenized Query in the Url Params
		qi.Set(upQuery, trq.Statement)
		trq.TemplateURL.RawQuery = qi.Encode()
	}

	return trq, ro, canOPC, nil
}
