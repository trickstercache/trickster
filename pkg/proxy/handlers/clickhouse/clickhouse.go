package clickhouse

import (
	"net/http"
	"net/url"

	co "github.com/tricksterproxy/trickster/pkg/cache/options"
	"github.com/tricksterproxy/trickster/pkg/cache/registration"
	"github.com/tricksterproxy/trickster/pkg/proxy/origins/clickhouse"
	"github.com/tricksterproxy/trickster/pkg/proxy/origins/clickhouse/model"
	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
	"github.com/tricksterproxy/trickster/pkg/routing"

	"github.com/gorilla/mux"
)

// NewAccelerator returns a new ClickHouse Accelerator. only baseURL is required
func NewAccelerator(baseURL string, o *oo.Options, c *co.Options) (http.Handler, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if c == nil {
		c = co.New()
		c.Name = "default"
	}
	cache := registration.NewCache(c.Name, c, nil)
	err = cache.Connect()
	if err != nil {
		return nil, err
	}
	if o == nil {
		o = oo.New()
		o.Name = "default"
	}
	o.OriginType = "clickhouse"
	o.CacheName = c.Name
	o.Scheme = u.Scheme
	o.Host = u.Host
	o.PathPrefix = u.Path
	r := mux.NewRouter()
	cl, err := clickhouse.NewClient("default", o, mux.NewRouter(), cache, model.NewModeler())
	if err != nil {
		return nil, err
	}
	o.HTTPClient = cl.HTTPClient()
	routing.RegisterPathRoutes(r, cl.Handlers(), cl, o, cache, cl.DefaultPathConfigs(o), nil, "", nil)
	return r, nil
}
