// Package irondb provides proxy origin support for IRONdb databases.
package irondb

import (
	"net/http"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
)

// Origin types.
const (
	otIRONdb = "irondb"
)

// IRONdb API path segments.
const (
	mnHealth    = "health"
	mnRaw       = "raw"
	mnRollup    = "rollup"
	mnFetch     = "fetch"
	mnRead      = "read"
	mnHistogram = "histogram"
	mnFind      = "find"
	mnCAQL      = "extension/lua/caql_v1"
	mnCAQLPub   = "extension/lua/public/caql_v1"
	mnState     = "state"
	mnJSON      = "json"
)

// Common IRONdb URL query parameter names.
const (
	upQuery      = "query"
	upStart      = "start_ts"
	upEnd        = "end_ts"
	upSpan       = "rollup_span"
	upEngine     = "get_engine"
	upType       = "type"
	upActStart   = "activity_start_seconds"
	upActEnd     = "activity_end_seconds"
	upCAQLQuery  = "q"
	upCAQLStart  = "start"
	upCAQLEnd    = "end"
	upCAQLPeriod = "period"
)

// IRONdb request body field names.
const (
	rbStart  = "start"
	rbCount  = "count"
	rbPeriod = "period"
)

// Client values provide access to IRONdb and implement the Trickster proxy
// client interface.
type Client struct {
	name      string
	user      string
	pass      string
	config    *config.OriginConfig
	cache     cache.Cache
	webClient *http.Client
}

// NewClient returns a new Client Instance
func NewClient(name string, oc *config.OriginConfig, cache cache.Cache) *Client {
	c := proxy.NewHTTPClient(oc)
	return &Client{name: name, config: oc, cache: cache, webClient: c}
}

// Configuration returns the upstream Configuration for this Client.
func (c *Client) Configuration() *config.OriginConfig {
	return c.config
}

// HTTPClient returns the HTTP Transport this client is using.
func (c *Client) HTTPClient() *http.Client {
	return c.webClient
}

// Cache returns a handle to the Cache instance used by this Client.
func (c *Client) Cache() cache.Cache {
	return c.cache
}

// Name returns the name of the origin Configuration proxied by the Client.
func (c *Client) Name() string {
	return c.name
}
