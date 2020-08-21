package rpc

import (
	"net/http"
	"net/url"

	co "github.com/tricksterproxy/trickster/pkg/cache/options"
	"github.com/tricksterproxy/trickster/pkg/cache/registration"
	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
	rpc "github.com/tricksterproxy/trickster/pkg/proxy/origins/reverseproxycache"
	"github.com/tricksterproxy/trickster/pkg/routing"

	"github.com/gorilla/mux"
)

// New returns a new Reverse Proxy Cache
func New(baseURL string) (http.Handler, error) {
	return NewWithOptions(baseURL, nil, nil)
}

// NewWithOptions returns a new Reverse Proxy Cache. only baseURL is required
func NewWithOptions(baseURL string, o *oo.Options, c *co.Options) (http.Handler, error) {
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
	o.OriginType = "rpc"
	o.CacheName = c.Name
	o.Scheme = u.Scheme
	o.Host = u.Host
	o.PathPrefix = u.Path
	r := mux.NewRouter()
	cl, err := rpc.NewClient("default", o, r, cache)
	if err != nil {
		return nil, err
	}
	o.HTTPClient = cl.HTTPClient()
	routing.RegisterPathRoutes(r, cl.Handlers(), cl, o, cache, cl.DefaultPathConfigs(o), nil, "", nil)
	return r, nil
}
