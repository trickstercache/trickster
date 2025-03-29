package validate

import (
	"errors"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	tr "github.com/trickstercache/trickster/v2/pkg/observability/tracing/registration"
	"github.com/trickstercache/trickster/v2/pkg/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
)

func Validate(c *config.Config) error {

	for _, w := range c.LoaderWarnings {
		fmt.Println(w)
	}
	var caches = make(map[string]cache.Cache)
	for k := range c.Caches {
		caches[k] = nil
	}
	r := lm.NewRouter()
	mr := lm.NewRouter()
	mr.SetMatchingScheme(0) // metrics router is exact-match only

	tracers, err := tr.RegisterAll(c, true)
	if err != nil {
		return err
	}

	_, err = routing.RegisterProxyRoutes(c, r, mr, caches, tracers, true)
	if err != nil {
		return err
	}
	if c.Frontend.TLSListenPort < 1 && c.Frontend.ListenPort < 1 {
		return errors.New("no http or https listeners configured")
	}

	if c.Frontend.ServeTLS && c.Frontend.TLSListenPort > 0 {
		_, err = c.TLSCertConfig()
		if err != nil {
			return err
		}
	}
	return nil
}
