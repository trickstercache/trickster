package irondb

import (
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/md5"
)

// FindHandler handles requests to find metirc information and processes them
// through the object proxy cache.
func (c *Client) FindHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)
	engines.ObjectProxyCacheRequest(
		model.NewRequest(c.Configuration(), "FindHandler",
			r.Method, u, r.Header, c.config.Timeout, r, c.webClient),
		w, c, c.cache, c.cache.Configuration().ObjectTTL, false)
}

// findHandlerDeriveCacheKey calculates a query-specific keyname based on the
// user request.
func (c Client) findHandlerDeriveCacheKey(r *model.Request,
	extra string) string {
	var sb strings.Builder
	sb.WriteString(r.URL.Path)
	qp := r.URL.Query()
	sb.WriteString(qp.Get(upQuery))
	sb.WriteString(extra)
	return md5.Checksum(sb.String())
}
