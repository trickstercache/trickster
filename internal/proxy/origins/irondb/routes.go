package irondb

import (
	"net/http"

	"github.com/Comcast/trickster/internal/config"
)

var handlers = make(map[string]http.Handler)
var handlersRegistered = false

func (c *Client) registerHandlers() {
	handlersRegistered = true
	// This is the registry of handlers that Trickster supports for Prometheus,
	// and are able to be referenced by name (map key) in Config Files
	handlers["health"] = http.HandlerFunc(c.HealthHandler)
	handlers[mnRaw] = http.HandlerFunc(c.RawHandler)
	handlers[mnRollup] = http.HandlerFunc(c.RollupHandler)
	handlers[mnFetch] = http.HandlerFunc(c.FetchHandler)
	handlers[mnRead] = http.HandlerFunc(c.TextHandler)
	handlers[mnHistogram] = http.HandlerFunc(c.HistogramHandler)
	handlers[mnFind] = http.HandlerFunc(c.FindHandler)
	handlers[mnState] = http.HandlerFunc(c.StateHandler)
	handlers[mnCAQL] = http.HandlerFunc(c.CAQLHandler)
	handlers["proxy"] = http.HandlerFunc(c.ProxyHandler)
}

// Handlers returns a map of the HTTP Handlers the client has registered
func (c *Client) Handlers() map[string]http.Handler {
	if !handlersRegistered {
		c.registerHandlers()
	}
	return handlers
}

// DefaultPathConfigs returns the default PathConfigs for the given OriginType
func (c *Client) DefaultPathConfigs() (map[string]*config.ProxyPathConfig, []string) {

	paths := map[string]*config.ProxyPathConfig{

		"/" + mnRaw: &config.ProxyPathConfig{
			Path:            "/" + mnRaw,
			HandlerName:     mnRaw,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		"/" + mnRollup: &config.ProxyPathConfig{
			Path:            "/" + mnRollup,
			HandlerName:     mnRollup,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		"/" + mnFetch: &config.ProxyPathConfig{
			Path:            "/" + mnFetch,
			HandlerName:     mnFetch,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		"/" + mnRead: &config.ProxyPathConfig{
			Path:            "/" + mnRead,
			HandlerName:     mnRead,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		"/" + mnHistogram: &config.ProxyPathConfig{
			Path:            "/" + mnHistogram,
			HandlerName:     mnHistogram,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		"/" + mnFind: &config.ProxyPathConfig{
			Path:            "/" + mnFind,
			HandlerName:     mnFind,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		"/" + mnState: &config.ProxyPathConfig{
			Path:            "/" + mnState,
			HandlerName:     mnState,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		"/" + mnCAQL: &config.ProxyPathConfig{
			Path:            "/" + mnCAQL,
			HandlerName:     mnCAQL,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		"/" + mnCAQLPub: &config.ProxyPathConfig{
			Path:            "/" + mnCAQLPub,
			HandlerName:     mnCAQL,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{}, // TODO: Populate
			CacheKeyHeaders: []string{},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		"/": &config.ProxyPathConfig{
			Path:        "/",
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet},
		},
	}

	orderedPaths := []string{"/" + mnRaw, "/" + mnRollup, "/" + mnFetch, "/" + mnRead,
		"/" + mnHistogram, "/" + mnFind, "/" + mnState, "/" + mnCAQL, "/"}

	return paths, orderedPaths

}
