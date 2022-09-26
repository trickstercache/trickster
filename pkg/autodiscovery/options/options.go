// package options implements YAML config options for service discovery.
package options

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/templates"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"
)

// QueryOptions are used to process queries with any given method of autodiscovery.
type QueryOptions map[string]string

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
	return &Options{
		Queries:  opt.Queries,
		Backends: opt.Backends,
	}
}

// Iterate over the provided options, returning a new options with all defined metadata values
// set to either the provided option or default value.
//
// Note: the current default value is the empty Options.
func SetDefaults(name string, options *Options, metadata yamlx.KeyLookup) (*Options, error) {
	if metadata == nil {
		return nil, fmt.Errorf("Can't call SetDefaults with nil metadata")
	}
	optOut := New()
	return optOut, nil
}
