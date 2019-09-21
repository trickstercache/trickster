package irondb

import (
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/md5"
)

// RawHandler handles requests for raw numeric timeseries data and processes
// them through the delta proxy cache.
func (c *Client) RawHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)
	engines.DeltaProxyCacheRequest(
		model.NewRequest("RawHandler",
			r.Method, u, r.Header, c.config.Timeout, r, c.webClient),
		w, c)
}

// rawHandlerSetExtent will change the upstream request query to use the
// provided Extent.
func (c Client) rawHandlerSetExtent(r *model.Request,
	extent *timeseries.Extent) {
	q := r.URL.Query()
	q.Set(upStart, formatTimestamp(extent.Start, true))
	q.Set(upEnd, formatTimestamp(extent.End, true))
	r.URL.RawQuery = q.Encode()
}

// rawHandlerParseTimeRangeQuery parses the key parts of a TimeRangeQuery
// from the inbound HTTP Request.
func (c *Client) rawHandlerParseTimeRangeQuery(
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

	return trq, nil
}

// rawHandlerDeriveCacheKey calculates a query-specific keyname based on the
// user request.
func (c Client) rawHandlerDeriveCacheKey(r *model.Request,
	extra string) string {
	var sb strings.Builder
	sb.WriteString(r.URL.Path)
	sb.WriteString(extra)
	return md5.Checksum(sb.String())
}
