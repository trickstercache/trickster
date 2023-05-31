package discovery

import (
	"context"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	do "github.com/trickstercache/trickster/v2/pkg/backends/alb/discovery/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/router"
)

type Result struct {
	Name string
	URL  string
}

// Clients act on a set of options to fetch valid backend origins.
type Client interface {
	Execute(ctx context.Context, opts *do.Options) ([]Result, error)
}

// Discover services using the provided client.
// This function takes a few steps given the provided inputs:
//   - Ensure that the discovery options match with the provided client
//   - Use the client to find a set of named origin URLs
//   - Use the template options provided to instantiate more options based on targets
//   - Return the resulting instantiated options
func DiscoverServices(ctx context.Context, c Client, opts *do.Options, bs backends.Backends) (backends.Backends, error) {
	// Arrange template mapping
	templates := make(map[string]*bo.Options)
	caches := make(map[string]cache.Cache)
	for _, b := range bs {
		conf := b.Configuration()
		if !conf.IsTemplate {
			continue
		}
		for tFrom, tTo := range opts.Targets {
			if conf.Name == tTo {
				templates[tFrom] = conf.Clone()
				caches[tFrom] = b.Cache()
			}
		}
	}
	ress, err := c.Execute(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make(backends.Backends, len(ress))
	for i, res := range ress {
		t, ok := templates[res.Name]
		if !ok {
			return nil, fmt.Errorf("resolved autodiscovery but could not find template %s", res.Name)
		}
		t = t.Clone()
		if res.Name != "" {
			t.Name = res.Name
		} else {
			t.Name = fmt.Sprintf("%s_%d", t.Name, i)
		}
		t.OriginURL = res.URL
		b, err := backends.New(t.Name, t, nil, router.NewRouter(), caches[res.Name])
		if err != nil {
			return nil, err
		}
		out[b.Name()] = b
	}
	return out, nil
}
