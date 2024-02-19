package mysql

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {
	pq, ok := trq.ParsedQuery.(*sqlquery)
	if !ok {
		return
	}
	pq.extent = extent

	trq.Extent = *pq.extent
	trq.Statement = pq.String()
	qi := r.URL.Query()
	qi.Set("query", trq.Statement)
	trq.TemplateURL.RawQuery = qi.Encode()
	v, _, _ := params.GetRequestValues(r)
	v.Set("query", pq.String())
	params.SetRequestValues(r, v)
}
