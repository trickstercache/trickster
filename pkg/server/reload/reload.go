package reload

import (
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/config"
	te "github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/server/instance"
)

var mtx sync.Mutex

func RequestReload(si *instance.ServerInstance) (bool, error) {
	conf, err := si.ConfigValidator()
	if err != nil {
		return false, err
	}
	if conf == nil || conf.Resources == nil {
		return false, te.ErrInvalidOptions
	}
	mtx.Lock()
	defer mtx.Unlock()
	if conf.IsStale() {
		logger.Warn("configuration reload starting now",
			logging.Pairs{"source": "sighup"})
		_, err := si.ConfigApplicator(si, conf, nil)
		if err != nil {
			logger.Warn(config.ConfigNotReloadedText,
				logging.Pairs{"error": err.Error()})
			return false, err
		}
		logger.Info(config.ConfigReloadedText, nil)
		return true, nil
	}
	logger.Warn(config.ConfigNotReloadedText, nil)
	return false, nil
}
