package irondb

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/md5"
)

// HistogramHandler handles requests for historgam timeseries data and processes
// them through the delta proxy cache.
func (c *Client) HistogramHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)
	engines.DeltaProxyCacheRequest(
		model.NewRequest(c.name, otIRONdb, "HistogramHandler",
			r.Method, u, r.Header, c.config.Timeout, r, c.webClient),
		w, c, c.cache, c.cache.Configuration().TimeseriesTTL)
}

// histogramHandlerSetExtent will change the upstream request query to use the
// provided Extent.
func (c Client) histogramHandlerSetExtent(r *model.Request,
	extent *timeseries.Extent) {
	trq := r.TimeRangeQuery
	var err error
	if trq == nil {
		if trq, err = c.ParseTimeRangeQuery(r); err != nil {
			return
		}
	}

	st := extent.Start.UnixNano() - (extent.Start.UnixNano() % int64(trq.Step))
	et := extent.End.UnixNano() - (extent.End.UnixNano() % int64(trq.Step))
	ps := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 6)
	if len(ps) < 6 || ps[0] != "histogram" {
		return
	}

	sb := new(strings.Builder)
	if strings.HasPrefix(r.URL.Path, "/") {
		sb.WriteString("/")
	}

	sb.WriteString("histogram")
	sb.WriteString("/" + strconv.FormatInt(time.Unix(0, st).Unix(), 10))
	sb.WriteString("/" + strconv.FormatInt(time.Unix(0, et).Unix(), 10))
	sb.WriteString("/" + strings.Join(ps[3:], "/"))
	r.URL.Path = sb.String()
}

// histogramHandlerParseTimeRangeQuery parses the key parts of a TimeRangeQuery
// from the inbound HTTP Request.
func (c *Client) histogramHandlerParseTimeRangeQuery(
	r *model.Request) (*timeseries.TimeRangeQuery, error) {
	trq := &timeseries.TimeRangeQuery{}
	p := r.URL.Path
	ps := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 6)
	if len(ps) < 6 || ps[0] != "histogram" {
		return nil, errors.NotTimeRangeQuery()
	}

	trq.Statement = "/histogram/" + strings.Join(ps[4:], "/")

	var err error
	if trq.Extent.Start, err = parseTimestamp(ps[1]); err != nil {
		return nil, err
	}

	if trq.Extent.End, err = parseTimestamp(ps[2]); err != nil {
		return nil, err
	}

	if !strings.HasSuffix(ps[3], "s") {
		ps[3] += "s"
	}

	if trq.Step, err = parseDuration(ps[3]); err != nil {
		return nil, err
	}

	return trq, nil
}

// histogramHandlerDeriveCacheKey calculates a query-specific keyname based on
// the user request.
func (c Client) histogramHandlerDeriveCacheKey(r *model.Request,
	extra string) string {
	var sb strings.Builder
	sb.WriteString(r.URL.Path)
	ps := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 6)
	if len(ps) >= 6 || ps[0] == "histogram" {
		sb.WriteString("/histogram/" + strings.Join(ps[3:], "/"))
	}

	sb.WriteString(extra)
	return md5.Checksum(sb.String())
}

// histogramHandlerFastForwardURL returns the url to fetch the Fast Forward value
// based on a timerange URL.
func (c *Client) histogramHandlerFastForwardURL(
	r *model.Request) (*url.URL, error) {
	var err error
	u := model.CopyURL(r.URL)
	trq := r.TimeRangeQuery
	if trq == nil {
		trq, err = c.ParseTimeRangeQuery(r)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now().Unix()
	start := now - (now % int64(trq.Step.Seconds()))
	end := start + int64(trq.Step.Seconds())
	ps := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 6)
	if len(ps) < 6 || ps[0] != "histogram" {
		return nil, errors.InvalidPath(u.Path)
	}

	sb := new(strings.Builder)
	if strings.HasPrefix(u.Path, "/") {
		sb.WriteString("/")
	}

	sb.WriteString("histogram")
	sb.WriteString("/" + strconv.FormatInt(time.Unix(start, 0).Unix(), 10))
	sb.WriteString("/" + strconv.FormatInt(time.Unix(end, 0).Unix(), 10))
	sb.WriteString("/" + strings.Join(ps[3:], "/"))
	u.Path = sb.String()
	return u, nil
}
