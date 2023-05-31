package discovery

import (
	do "github.com/trickstercache/trickster/v2/pkg/backends/alb/discovery/options"
)

type Result struct {
	Name string
	URL  string
}

// Clients act on a set of options to fetch valid backend origins.
type Client interface {
	Execute(opts *do.Options) ([]Result, error)
}
