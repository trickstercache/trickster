// package options implements YAML config options for service discovery.
package options

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients"
	cbuild "github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/builder"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/confpool"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	qbuild "github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/builder"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/templates"
)

// struct Options represents the configuration options for a backend provider that is
// using automatic service discovery.
type Options struct {
	Clients   *confpool.ConfObjectPool[clients.Client, *cbuild.ClientBuilder] `yaml:"clients"`
	Queries   *confpool.ConfObjectPool[queries.Query, *qbuild.QueryBuilder]   `yaml:"queries,omitempty"`
	Templates map[string]*templates.Options                                   `yaml:"templates,omitempty"`
}

// New returns an empty Options
func New() *Options {
	return &Options{
		Clients:   confpool.New[clients.Client, *cbuild.ClientBuilder](),
		Queries:   confpool.New[queries.Query, *qbuild.QueryBuilder](),
		Templates: make(map[string]*templates.Options),
	}
}

func (opts *Options) Clone() *Options {
	out := New()
	out.Clients = nil
	for k, v := range opts.Templates {
		out.Templates[k] = v.Clone()
	}
	return out
}
