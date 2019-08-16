package trickster

import (
	"fmt"
	"net/http"
	"os"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
	"github.com/Comcast/trickster/pkg/trickster/prometheus"
)

const (
	applicationName    = "trickster"
	applicationVersion = "1.0.9"

	OriginTypePrometheus = "prometheus"
)

type TricksterClient interface {
	Process(w http.ResponseWriter, r *http.Request)
}

func InitTrickster(args []string) error {
	err := config.Load(applicationName, applicationVersion, args)
	if err != nil {
		return err
	}

	if config.Flags.PrintVersion {
		fmt.Println(applicationVersion)
		os.Exit(0)
	}

	log.Init()
	log.Info("application start up", log.Pairs{"name": applicationName, "version": applicationVersion, "logLevel": config.Logging.LogLevel})

	for _, w := range config.LoaderWarnings {
		log.Warn(w, log.Pairs{})
	}

	metrics.Init()
	cr.LoadCachesFromConfig()

	return nil
}

func FinTrickster() {
	log.Logger.Close()
}

func New(origin string) (TricksterClient, error) {
	var originConfig *config.OriginConfig

	for _, o := range config.Origins {
		if o.Type == origin {
			originConfig = o
		}
	}

	if originConfig == nil {
		return nil, fmt.Errorf("%s origin config is missing", origin)
	}

	switch origin {
	case OriginTypePrometheus:
		return prometheus.NewTricksterClient(originConfig)
	}
	return nil, fmt.Errorf("%s trickster client is not supported", origin)
}
