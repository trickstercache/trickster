package irondb

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
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
		u.Path += strings.Replace(r.URL.Path, "/"+c.name+"/", "/", 1)
	} else {
		u.Path += r.URL.Path
	}

	u.RawQuery = r.URL.RawQuery
	u.Fragment = r.URL.Fragment
	u.User = r.URL.User
	return u
}

// SetExtent will change the upstream request query to use the provided Extent.
func (c Client) SetExtent(r *model.Request, extent *timeseries.Extent) {
	switch r.HandlerName {
	case "RawHandler":
		c.rawHandlerSetExtent(r, extent)
	case "RollupHandler":
		c.rollupHandlerSetExtent(r, extent)
	case "TextHandler":
		c.textHandlerSetExtent(r, extent)
	case "HistogramHandler":
		c.histogramHandlerSetExtent(r, extent)
	case "CAQLHandler":
		c.caqlHandlerSetExtent(r, extent)
	}
}

// FastForwardURL returns the url to fetch the Fast Forward value based on a
// timerange URL.
func (c *Client) FastForwardURL(r *model.Request) (*url.URL, error) {
	switch r.HandlerName {
	case "RollupHandler":
		return c.rollupHandlerFastForwardURL(r)
	case "HistogramHandler":
		return c.histogramHandlerFastForwardURL(r)
	case "CAQLHandler":
		return c.caqlHandlerFastForwardURL(r)
	}

	r.FastForwardDisable = true
	return r.URL, nil
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
	r *model.Request) (*timeseries.TimeRangeQuery, error) {
	switch r.HandlerName {
	case "RawHandler":
		return c.rawHandlerParseTimeRangeQuery(r)
	case "RollupHandler":
		return c.rollupHandlerParseTimeRangeQuery(r)
	case "TextHandler":
		return c.textHandlerParseTimeRangeQuery(r)
	case "HistogramHandler":
		return c.histogramHandlerParseTimeRangeQuery(r)
	case "CAQLHandler":
		return c.caqlHandlerParseTimeRangeQuery(r)
	}

	return nil, errors.NotTimeRangeQuery()
}
