package irondb

import (
	"net/http"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/model"
)

// StateHandler handles requests for state data and processes them through the
// basic reverse proxy to the origin for non-cacheable API calls.
func (c *Client) StateHandler(w http.ResponseWriter, r *http.Request) {
	engines.ProxyRequest(model.NewRequest(c.Configuration(), "StateHandler",
		r.Method, c.BuildUpstreamURL(r), r.Header, c.config.Timeout, r,
		c.webClient), w)
}
