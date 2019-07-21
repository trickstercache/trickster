package irondb

import (
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/routing"
	"github.com/Comcast/trickster/internal/util/log"
)

// RegisterRoutes registers the routes for the Client into the proxy's HTTP
// multiplexer.
func (c *Client) RegisterRoutes(originName string, o *config.OriginConfig) {
	// Setup host header based routing.
	log.Debug("Registering Origin Handlers",
		log.Pairs{"originType": o.Type, "originName": originName})
	routing.Router.PathPrefix("/" + mnHealth).
		HandlerFunc(c.HealthHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnRaw).
		HandlerFunc(c.RawHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnRollup).
		HandlerFunc(c.RollupHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnRead).
		HandlerFunc(c.TextHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnHistogram).
		HandlerFunc(c.HistogramHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnFind).
		HandlerFunc(c.FindHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnState).
		HandlerFunc(c.StateHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnCAQL).
		HandlerFunc(c.CAQLHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnCAQLPub).
		HandlerFunc(c.CAQLHandler).Methods("GET").Host(originName)
	routing.Router.PathPrefix("/").
		HandlerFunc(c.ProxyHandler).Methods("GET").Host(originName)

	// Setup path based routing.
	routing.Router.PathPrefix("/" + originName + "/" + mnHealth).
		HandlerFunc(c.HealthHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/" + mnIRONdb + "/" + mnRaw).
		HandlerFunc(c.RawHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/" + mnIRONdb + "/" + mnRollup).
		HandlerFunc(c.RollupHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/" + mnIRONdb + "/" + mnRead).
		HandlerFunc(c.TextHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/" + mnIRONdb + "/" + mnHistogram).
		HandlerFunc(c.HistogramHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/" + mnIRONdb + "/" + mnFind).
		HandlerFunc(c.FindHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/" + mnIRONdb + "/" + mnState).
		HandlerFunc(c.StateHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/" + mnIRONdb + "/" + mnCAQL).
		HandlerFunc(c.CAQLHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/" + mnIRONdb + "/" + mnCAQLPub).
		HandlerFunc(c.CAQLHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + "/").
		HandlerFunc(c.ProxyHandler).Methods("GET")

	// If default origin, setup those routes too.
	if o.IsDefault {
		log.Debug("Registering Default Origin Handlers",
			log.Pairs{"originType": o.Type})
		routing.Router.PathPrefix("/" + mnHealth).
			HandlerFunc(c.HealthHandler).Methods("GET")
		routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnRaw).
			HandlerFunc(c.RawHandler).Methods("GET")
		routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnRollup + "/").
			HandlerFunc(c.RollupHandler).Methods("GET")
		routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnRead).
			HandlerFunc(c.TextHandler).Methods("GET")
		routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnHistogram).
			HandlerFunc(c.HistogramHandler).Methods("GET")
		routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnFind).
			HandlerFunc(c.FindHandler).Methods("GET")
		routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnState).
			HandlerFunc(c.StateHandler).Methods("GET")
		routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnCAQL).
			HandlerFunc(c.CAQLHandler).Methods("GET")
		routing.Router.PathPrefix("/" + mnIRONdb + "/" + mnCAQLPub).
			HandlerFunc(c.CAQLHandler).Methods("GET")
		routing.Router.PathPrefix("/").
			HandlerFunc(c.ProxyHandler).Methods("GET")
	}
}
