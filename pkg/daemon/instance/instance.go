package instance

import (
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
)

type ServerInstance struct {
	Config *config.Config
	Caches cache.CacheLookup
}
