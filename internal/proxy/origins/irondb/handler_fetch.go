package irondb

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/md5"
)

// FetchHandler handles requests for numeric timeseries data with specified
// spans and processes them through the delta proxy cache.
func (c *Client) FetchHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)
	engines.DeltaProxyCacheRequest(
		model.NewRequest(c.Configuration(), "FetchHandler",
			r.Method, u, r.Header, c.config.Timeout, r, c.webClient),
		w, c, c.cache, c.cache.Configuration().TimeseriesTTL)
}

// fetchHandlerSetExtent will change the upstream request query to use the
// provided Extent.
func (c Client) fetchHandlerSetExtent(r *model.Request,
	extent *timeseries.Extent) {
	trq := r.TimeRangeQuery
	var err error
	if trq == nil {
		if trq, err = c.ParseTimeRangeQuery(r); err != nil {
			return
		}
	}

	b, err := ioutil.ReadAll(r.ClientRequest.Body)
	if err != nil {
		return
	}

	fetchReq := map[string]interface{}{}
	if err = json.NewDecoder(bytes.NewBuffer(b)).Decode(&fetchReq); err != nil {
		return
	}

	st := extent.Start.UnixNano() - (extent.Start.UnixNano() % int64(trq.Step))
	et := extent.End.UnixNano() - (extent.End.UnixNano() % int64(trq.Step))
	if st == et {
		et += int64(trq.Step)
	}

	ct := (et - st) / int64(trq.Step)
	fetchReq[rbStart] = time.Unix(0, st).Unix()
	fetchReq[rbCount] = ct
	newBody := &bytes.Buffer{}
	err = json.NewEncoder(newBody).Encode(&fetchReq)
	if err != nil {
		return
	}

	r.ClientRequest.Body = ioutil.NopCloser(newBody)
}

// fetchHandlerParseTimeRangeQuery parses the key parts of a TimeRangeQuery
// from the inbound HTTP Request.
func (c *Client) fetchHandlerParseTimeRangeQuery(
	r *model.Request) (*timeseries.TimeRangeQuery, error) {
	trq := &timeseries.TimeRangeQuery{}
	b, err := ioutil.ReadAll(r.ClientRequest.Body)
	if err != nil {
		return nil, errors.ParseRequestBody(err)
	}

	r.ClientRequest.Body = ioutil.NopCloser(bytes.NewBuffer(b))
	fetchReq := map[string]interface{}{}
	if err = json.NewDecoder(bytes.NewBuffer(b)).Decode(&fetchReq); err != nil {
		return nil, errors.ParseRequestBody(err)
	}

	i := float64(0)
	var ok bool
	if i, ok = fetchReq[rbStart].(float64); !ok {
		return nil, errors.MissingRequestParam(rbStart)
	}

	trq.Extent.Start = time.Unix(int64(i), 0)
	if i, ok = fetchReq[rbPeriod].(float64); !ok {
		return nil, errors.MissingRequestParam(rbPeriod)
	}

	trq.Step = time.Second * time.Duration(i)
	if i, ok = fetchReq[rbCount].(float64); !ok {
		return nil, errors.MissingRequestParam(rbCount)
	}

	trq.Extent.End = trq.Extent.Start.Add(trq.Step * time.Duration(i))
	return trq, nil
}

// fetchHandlerDeriveCacheKey calculates a query-specific keyname based on the
// user request.
func (c Client) fetchHandlerDeriveCacheKey(r *model.Request,
	extra string) string {
	var sb strings.Builder
	sb.WriteString(r.URL.Path)
	newBody := &bytes.Buffer{}
	if b, err := ioutil.ReadAll(r.ClientRequest.Body); err == nil {
		r.ClientRequest.Body = ioutil.NopCloser(bytes.NewBuffer(b))
		fetchReq := map[string]interface{}{}
		err := json.NewDecoder(bytes.NewBuffer(b)).Decode(&fetchReq)
		if err == nil {
			delete(fetchReq, "start")
			delete(fetchReq, "end")
			delete(fetchReq, "count")
			err = json.NewEncoder(newBody).Encode(&fetchReq)
			if err == nil {
				sb.Write(newBody.Bytes())
			}
		}
	}

	sb.WriteString(extra)
	return md5.Checksum(sb.String())
}
