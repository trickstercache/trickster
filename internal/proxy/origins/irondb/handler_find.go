package irondb

import (
	"net/http"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/model"
)

// FindHandler handles requests to find metirc information and processes them
// through the object proxy cache.
func (c *Client) FindHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)
	engines.ObjectProxyCacheRequest(
		model.NewRequest("FindHandler",
			r.Method, u, r.Header, c.config.Timeout, r, c.webClient),
		w, c, false)
}
