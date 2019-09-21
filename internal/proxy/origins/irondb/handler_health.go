package irondb

import (
	"net/http"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/model"
)

// HealthHandler checks the health of the configured upstream Origin.
func (c Client) HealthHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BaseURL()
	u.Path += "/" + mnState
	engines.ProxyRequest(model.NewRequest("HealthHandler",
		http.MethodGet, u, r.Header, c.config.Timeout, r, c.webClient), w)
}
