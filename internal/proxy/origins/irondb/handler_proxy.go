package irondb

import (
	"net/http"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/model"
)

// ProxyHandler sends a request through the basic reverse proxy to the origin
// for non-cacheable API calls.
func (c *Client) ProxyHandler(w http.ResponseWriter, r *http.Request) {
	engines.ProxyRequest(model.NewRequest(c.name, otIRONdb, "ProxyHandler",
		r.Method, c.BuildUpstreamURL(r), r.Header, c.config.Timeout, r,
		c.webClient), w, c.Logger())
}
