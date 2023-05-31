package discovery

import (
	"context"
	"fmt"

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
func DiscoverServices(ctx context.Context, c Client, opts *do.Options, bs map[string]*bo.Options) ([]*bo.Options, error) {
	// Arrange template mapping
	templates := make(map[string]*bo.Options)
	for _, b := range bs {
		if !b.IsTemplate {
			continue
		}
		for tFrom, tTo := range opts.Targets {
			if b.Name == tTo {
				templates[tFrom] = b.Clone()
			}
		}
	}
	ress, err := c.Execute(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make([]*bo.Options, len(ress))
	for i, res := range ress {
		t, ok := templates[res.Name]
		if !ok {
			t, ok = templates["default"]
			if !ok {
				return nil, fmt.Errorf("resolved autodiscovery but could not find template %s", res.Name)
			}
		}
		t = t.Clone()
		t.IsTemplate = false
		if res.Name != "" {
			t.Name = res.Name
		} else {
			t.Name = fmt.Sprintf("%s_%d", t.Name, i)
		}
		t.OriginURL = res.URL
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}
