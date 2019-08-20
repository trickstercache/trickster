// Package irondb provides proxy origin support for IRONdb databases.
package irondb

import (
	"net"
	"net/http"
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/go-kit/kit/log"
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
	upCAQLStart  = "start"
	upCAQLEnd    = "end"
	upCAQLPeriod = "period"
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
	logger    log.Logger
}

// NewClient returns a new Client instance.
func NewClient(name string, config *config.OriginConfig,
	cache cache.Cache, logger log.Logger) *Client {
	c := &http.Client{
		Timeout: config.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Dial: (&net.Dialer{
				KeepAlive: time.Duration(config.KeepAliveTimeoutSecs) *
					time.Second,
			}).Dial,
			MaxIdleConns:        config.MaxIdleConns,
			MaxIdleConnsPerHost: config.MaxIdleConns,
		},
	}

	return &Client{name: name, config: config, cache: cache, webClient: c, logger: logger}
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

// Logger will return the Logger instance
func (c *Client) Logger() log.Logger {
	return c.logger
}
