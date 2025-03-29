package instance

import (
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
)

type ConfigValidator func() (*config.Config, error)

type ConfigApplicator func(*ServerInstance, *config.Config,
	func()) (cache.CacheLookup, error)

type ServerInstance struct {
	Config           *config.Config
	Caches           cache.CacheLookup
	ConfigValidator  ConfigValidator
	ConfigApplicator ConfigApplicator
}
