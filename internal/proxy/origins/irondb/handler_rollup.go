package irondb

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/md5"
)

// RollupHandler handles requests for numeric timeseries data with specified
// spans and processes them through the delta proxy cache.
func (c *Client) RollupHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)
	engines.DeltaProxyCacheRequest(
		model.NewRequest(c.Configuration(), "RollupHandler",
			r.Method, u, r.Header, c.config.Timeout, r, c.webClient),
		w, c, c.cache, c.cache.Configuration().TimeseriesTTL)
}

// rollupHandlerSetExtent will change the upstream request query to use the
// provided Extent.
func (c Client) rollupHandlerSetExtent(r *model.Request,
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
	if st == et {
		et += int64(trq.Step)
	}

	q := r.URL.Query()
	q.Set(upStart, formatTimestamp(time.Unix(0, st), true))
	q.Set(upEnd, formatTimestamp(time.Unix(0, et), true))
	r.URL.RawQuery = q.Encode()
}

// rollupHandlerParseTimeRangeQuery parses the key parts of a TimeRangeQuery
// from the inbound HTTP Request.
func (c *Client) rollupHandlerParseTimeRangeQuery(
	r *model.Request) (*timeseries.TimeRangeQuery, error) {
	trq := &timeseries.TimeRangeQuery{}
	trq.Statement = r.URL.Path

	qp := r.URL.Query()
	var err error
	p := ""
	if p = qp.Get(upStart); p == "" {
		return nil, errors.MissingURLParam(upStart)
	}

	if trq.Extent.Start, err = parseTimestamp(p); err != nil {
		return nil, err
	}

	if p = qp.Get(upEnd); p == "" {
		return nil, errors.MissingURLParam(upEnd)
	}

	if trq.Extent.End, err = parseTimestamp(p); err != nil {
		return nil, err
	}

	if p = qp.Get(upSpan); p == "" {
		return nil, errors.MissingURLParam(upSpan)
	}

	if trq.Step, err = parseDuration(p); err != nil {
		return nil, err
	}

	return trq, nil
}

// rollupHandlerDeriveCacheKey calculates a query-specific keyname based on the
// user request.
func (c Client) rollupHandlerDeriveCacheKey(r *model.Request,
	extra string) string {
	var sb strings.Builder
	sb.WriteString(r.URL.Path)
	qp := r.URL.Query()
	sb.WriteString(qp.Get(upSpan))
	sb.WriteString(qp.Get(upEngine))
	sb.WriteString(qp.Get(upType))
	sb.WriteString(extra)
	return md5.Checksum(sb.String())
}

// rollupHandlerFastForwardURL returns the url to fetch the Fast Forward value
// based on a timerange URL.
func (c *Client) rollupHandlerFastForwardURL(
	r *model.Request) (*url.URL, error) {
	var err error
	u := model.CopyURL(r.URL)
	q := u.Query()
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
	q.Set(upStart, formatTimestamp(time.Unix(start, 0), true))
	q.Set(upEnd, formatTimestamp(time.Unix(end, 0), true))
	u.RawQuery = q.Encode()
	return u, nil
}
