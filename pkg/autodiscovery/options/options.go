// package options implements YAML config options for service discovery.
package options

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/templates"
)

// struct Options represents the configuration options for a backend provider that is
// using automatic service discovery.
type Options struct {
	Queries  map[string]*queries.Options   `yaml:"queries,omitempty"`
	Backends map[string]*templates.Options `yaml:"templates,omitempty"`
}

// Return an empty Options
func New() *Options {
	return &Options{
		Queries:  make(map[string]*queries.Options),
		Backends: make(map[string]*templates.Options),
	}
}

// Returns a perfect deep copy of the calling Options
func (opt *Options) Clone() *Options {
	queriesOut := make(map[string]*queries.Options)
	for k, v := range opt.Queries {
		queriesOut[k] = v.Clone()
	}
	backendsOut := make(map[string]*templates.Options)
	for k, v := range opt.Backends {
		backendsOut[k] = v.Clone()
	}
	return &Options{
		Queries:  queriesOut,
		Backends: backendsOut,
	}
}
