package discovery

import (
	"context"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	do "github.com/trickstercache/trickster/v2/pkg/backends/alb/discovery/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
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
func DiscoverServices(ctx context.Context, c Client, opts *do.Options, bs backends.Backends) ([]*bo.Options, error) {
	var template *bo.Options
	for _, b := range bs {
		conf := b.Configuration()
		if conf.IsTemplate && conf.Name == opts.Target {
			template = conf.Clone()
			break
		}
	}
	if template == nil {
		return nil, fmt.Errorf("discovery requested template backend %s, but it was not provided", opts.Target)
	}
	ress, err := c.Execute(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make([]*bo.Options, len(ress))
	for i, res := range ress {
		t := template.Clone()
		if res.Name != "" {
			t.Name = res.Name
		} else {
			t.Name = fmt.Sprintf("%s_%d", t.Name, i)
		}
		t.OriginURL = res.URL
	}
	return out, nil
}
