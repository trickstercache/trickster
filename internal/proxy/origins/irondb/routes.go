package irondb

import (
	"net/http"

	"github.com/Comcast/trickster/internal/config"
)

func (c *Client) registerHandlers() {
	c.handlersRegistered = true
	c.handlers = make(map[string]http.Handler)
	// This is the registry of handlers that Trickster supports for Prometheus,
	// and are able to be referenced by name (map key) in Config Files
	c.handlers["health"] = http.HandlerFunc(c.HealthHandler)
	c.handlers[mnRaw] = http.HandlerFunc(c.RawHandler)
	c.handlers[mnRollup] = http.HandlerFunc(c.RollupHandler)
	c.handlers[mnFetch] = http.HandlerFunc(c.FetchHandler)
	c.handlers[mnRead] = http.HandlerFunc(c.TextHandler)
	c.handlers[mnHistogram] = http.HandlerFunc(c.HistogramHandler)
	c.handlers[mnFind] = http.HandlerFunc(c.FindHandler)
	c.handlers[mnState] = http.HandlerFunc(c.StateHandler)
	c.handlers[mnCAQL] = http.HandlerFunc(c.CAQLHandler)
	c.handlers["proxy"] = http.HandlerFunc(c.ProxyHandler)
}

// Handlers returns a map of the HTTP Handlers the client has registered
func (c *Client) Handlers() map[string]http.Handler {
	if !c.handlersRegistered {
		c.registerHandlers()
	}
	return c.handlers
}

// DefaultPathConfigs returns the default PathConfigs for the given OriginType
func (c *Client) DefaultPathConfigs(oc *config.OriginConfig) (map[string]*config.PathConfig, []string) {

	paths := map[string]*config.PathConfig{

		"/" + mnRaw: &config.PathConfig{
			Path:            "/" + mnRaw,
			HandlerName:     mnRaw,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
		},

		"/" + mnRollup: &config.PathConfig{
			Path:            "/" + mnRollup,
			HandlerName:     mnRollup,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
		},

		"/" + mnFetch: &config.PathConfig{
			Path:            "/" + mnFetch,
			HandlerName:     mnFetch,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
		},

		"/" + mnRead: &config.PathConfig{
			Path:            "/" + mnRead,
			HandlerName:     mnRead,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
		},

		"/" + mnHistogram: &config.PathConfig{
			Path:            "/" + mnHistogram,
			HandlerName:     mnHistogram,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
		},

		"/" + mnFind: &config.PathConfig{
			Path:            "/" + mnFind,
			HandlerName:     mnFind,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
		},

		"/" + mnState: &config.PathConfig{
			Path:            "/" + mnState,
			HandlerName:     mnState,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
		},

		"/" + mnCAQL: &config.PathConfig{
			Path:            "/" + mnCAQL,
			HandlerName:     mnCAQL,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
		},

		"/" + mnCAQLPub: &config.PathConfig{
			Path:            "/" + mnCAQLPub,
			HandlerName:     mnCAQL,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
		},

		"/": &config.PathConfig{
			Path:        "/",
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet},
		},
	}

	orderedPaths := []string{"/" + mnRaw, "/" + mnRollup, "/" + mnFetch, "/" + mnRead,
		"/" + mnHistogram, "/" + mnFind, "/" + mnState, "/" + mnCAQL, "/"}

	return paths, orderedPaths

}
