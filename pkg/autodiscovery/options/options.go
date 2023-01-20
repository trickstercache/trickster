// package options implements YAML config options for service discovery.
package options

import (
	client_pool "github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/pool"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/templates"
)

// struct Options represents the configuration options for a backend provider that is
// using automatic service discovery.
type Options struct {
	Clients   *client_pool.ClientPool       `yaml:"clients,omitempty"`
	Queries   map[string]*queries.Query     `yaml:"queries,omitempty"`
	Templates map[string]*templates.Options `yaml:"templates,omitempty"`
}

// Return an empty Options
func New() *Options {
	return &Options{
		Queries:   make(map[string]*queries.Query),
		Templates: make(map[string]*templates.Options),
	}
}

func (opts *Options) Clone() *Options {
	out := New()
	out.Clients = nil
	for k, v := range opts.Queries {
		out.Queries[k] = v.Clone()
	}
	for k, v := range opts.Templates {
		out.Templates[k] = v.Clone()
	}
	return out
}
