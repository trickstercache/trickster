package irondb

import (
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/routing"
	"github.com/Comcast/trickster/internal/util/log"
)

// RegisterRoutes registers the routes for the Client into the proxy's HTTP
// multiplexer.
func (c *Client) RegisterRoutes(originName string, o *config.OriginConfig) {
	prefix := "/"
	if o.APIPath != "" {
		prefix += o.APIPath + "/"
	}

	// Setup host header based routing.
	log.Debug(c.Logger(), "Registering Origin Handlers",
		log.Pairs{"originType": o.Type, "originName": originName})
	routing.Router.PathPrefix("/" + mnHealth).
		HandlerFunc(c.HealthHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix(prefix + mnRaw).
		HandlerFunc(c.RawHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix(prefix + mnRollup).
		HandlerFunc(c.RollupHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix(prefix + mnRead).
		HandlerFunc(c.TextHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix(prefix + mnHistogram).
		HandlerFunc(c.HistogramHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix(prefix + mnFind).
		HandlerFunc(c.FindHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix(prefix + mnState).
		HandlerFunc(c.StateHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix(prefix + mnCAQL).
		HandlerFunc(c.CAQLHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix(prefix + mnCAQLPub).
		HandlerFunc(c.CAQLHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/").
		HandlerFunc(c.ProxyHandler).Methods("GET").Host(originName)

	// Setup path based routing.
	routing.Router.PathPrefix("/" + originName + "/" + mnHealth).
		HandlerFunc(c.HealthHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + prefix + mnRaw).
		HandlerFunc(c.RawHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + prefix + mnRollup).
		HandlerFunc(c.RollupHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + prefix + mnRead).
		HandlerFunc(c.TextHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + prefix + mnHistogram).
		HandlerFunc(c.HistogramHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + prefix + mnFind).
		HandlerFunc(c.FindHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + prefix + mnState).
		HandlerFunc(c.StateHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + prefix + mnCAQL).
		HandlerFunc(c.CAQLHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + prefix + mnCAQLPub).
		HandlerFunc(c.CAQLHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/").
		HandlerFunc(c.ProxyHandler).Methods("GET")

	// If default origin, setup those routes too.
	if o.IsDefault {
		log.Debug(c.Logger(), "Registering Default Origin Handlers",
			log.Pairs{"originType": o.Type})
		routing.Router.PathPrefix("/" + mnHealth).
			HandlerFunc(c.HealthHandler).Methods("GET")
		routing.Router.PathPrefix(prefix + mnRaw).
			HandlerFunc(c.RawHandler).Methods("GET")
		routing.Router.PathPrefix(prefix + mnRollup + "/").
			HandlerFunc(c.RollupHandler).Methods("GET")
		routing.Router.PathPrefix(prefix + mnRead).
			HandlerFunc(c.TextHandler).Methods("GET")
		routing.Router.PathPrefix(prefix + mnHistogram).
			HandlerFunc(c.HistogramHandler).Methods("GET")
		routing.Router.PathPrefix(prefix + mnFind).
			HandlerFunc(c.FindHandler).Methods("GET")
		routing.Router.PathPrefix(prefix + mnState).
			HandlerFunc(c.StateHandler).Methods("GET")
		routing.Router.PathPrefix(prefix + mnCAQL).
			HandlerFunc(c.CAQLHandler).Methods("GET")
		routing.Router.PathPrefix(prefix + mnCAQLPub).
			HandlerFunc(c.CAQLHandler).Methods("GET")
		routing.Router.PathPrefix("/").
			HandlerFunc(c.ProxyHandler).Methods("GET")
	}
}
