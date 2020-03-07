/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package irondb

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	terr "github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/request"
	"github.com/Comcast/trickster/internal/timeseries"
)

// BaseURL returns a URL in the form of scheme://host/path based on the proxy
// configuration.
func (c Client) BaseURL() *url.URL {
	u := &url.URL{}
	u.Scheme = c.config.Scheme
	u.Host = c.config.Host
	u.Path = c.config.PathPrefix
	return u
}

// BuildUpstreamURL will merge the downstream request with the BaseURL to
// construct the full upstream URL.
func (c Client) BuildUpstreamURL(r *http.Request) *url.URL {
	u := c.BaseURL()
	if strings.HasPrefix(r.URL.Path, "/"+c.name+"/") {
		u.Path += strings.Replace(r.URL.Path, "/"+c.name+"/",
			"/", 1)
		if u.Path == "//" {
			u.Path = "/"
		}
	} else {
		u.Path += r.URL.Path
	}

	u.RawQuery = r.URL.RawQuery
	u.Fragment = r.URL.Fragment
	u.User = r.URL.User
	return u
}

// SetExtent will change the upstream request query to use the provided Extent.
func (c Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {

	rsc := request.GetResources(r)
	if rsc.PathConfig == nil {
		return
	}

	if f, ok := c.extentSetters[rsc.PathConfig.HandlerName]; ok {
		f(r, trq, extent)
	}
}

// FastForwardURL returns the url to fetch the Fast Forward value based on a
// timerange URL.
func (c *Client) FastForwardURL(r *http.Request) (*url.URL, error) {

	rsc := request.GetResources(r)
	if rsc.PathConfig == nil {
		return nil, errors.New("missing path config")
	}

	switch rsc.PathConfig.HandlerName {
	case "RollupHandler":
		return c.rollupHandlerFastForwardURL(r)
	case "HistogramHandler":
		return c.histogramHandlerFastForwardURL(r)
	case "CAQLHandler":
		return c.caqlHandlerFastForwardURL(r)
	}

	return nil, fmt.Errorf("unknown handler name: %s", rsc.PathConfig.HandlerName)
}

// formatTimestamp returns a string containing a timestamp in the format used
// by the IRONdb API.
func formatTimestamp(t time.Time, milli bool) string {
	if milli {
		return fmt.Sprintf("%d.%03d", t.Unix(), t.Nanosecond()/1000000)
	}

	return fmt.Sprintf("%d", t.Unix())
}

// parseTimestamp attempts to parse an IRONdb API timestamp string into a valid
// time value.
func parseTimestamp(s string) (time.Time, error) {
	sp := strings.Split(s, ".")
	sec, nsec := int64(0), int64(0)
	var err error
	if len(sp) > 0 {
		if sec, err = strconv.ParseInt(sp[0], 10, 64); err != nil {
			return time.Time{}, fmt.Errorf("unable to parse timestamp %s: %s",
				s, err.Error())
		}
	}

	if len(sp) > 1 {
		if nsec, err = strconv.ParseInt(sp[1], 10, 64); err != nil {
			return time.Time{}, fmt.Errorf("unable to parse timestamp %s: %s",
				s, err.Error())
		}

		nsec = nsec * 1000000
	}

	return time.Unix(sec, nsec), nil
}

// parseDuration attempts to parse an IRONdb API duration string into a valid
// duration value.
func parseDuration(s string) (time.Duration, error) {
	if !strings.HasSuffix(s, "s") {
		s += "s"
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("unable to parse duration %s: %s",
			s, err.Error())
	}

	return d, nil
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the
// inbound HTTP Request.
func (c *Client) ParseTimeRangeQuery(
	r *http.Request) (*timeseries.TimeRangeQuery, error) {

	rsc := request.GetResources(r)
	if rsc.PathConfig == nil {
		return nil, errors.New("missing path config")
	}

	var trq *timeseries.TimeRangeQuery
	var err error

	if f, ok := c.trqParsers[rsc.PathConfig.HandlerName]; ok {
		trq, err = f(r)
	} else {
		trq = nil
		err = terr.ErrNotTimeRangeQuery
	}
	rsc.TimeRangeQuery = trq
	return trq, err
}
