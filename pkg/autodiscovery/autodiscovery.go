// package autodiscovery provides methods for backends to discover other backend services.
package autodiscovery

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/methods"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/methods/extkube"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/methods/intkube"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/methods/mock"
	adopt "github.com/trickstercache/trickster/v2/pkg/autodiscovery/options"
	beopt "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/templates"
	betemps "github.com/trickstercache/trickster/v2/pkg/backends/templates"
)

func init() {
	methods.Methods[methods.MOCK] = &mock.Mock{}
	methods.Methods[methods.EXTKUBE] = &extkube.ExtKube{}
	methods.Methods[methods.INTKUBE] = &intkube.IntKube{}
}

// Run autodiscovery with a set of options, returning backend options for matched queries
func DiscoverWithOptions(opts *adopt.Options) ([]*beopt.Options, error) {
	fmt.Printf("Running autodiscovery with\nQueries:%+v\nBackends:%+v\n", opts.Queries, opts.Backends)
	queries := opts.Queries
	backends := opts.Backends
	out := make([]*beopt.Options, 0)
	// Range over all autodiscovery queries
	for _, queryOpts := range queries {
		// If there's no backend attached to this query, there's not much point in running it.
		backend, hasBackend := backends[queryOpts.UseTemplate]
		if !hasBackend {
			continue
		}
		// Get the method for this query.
		method, err := methods.GetMethod(queryOpts.Method)
		if err != nil {
			return nil, err
		}
		// Run the query and store the results
		results, err := method.Query(queryOpts)
		if err != nil {
			return nil, err
		}
		// Get and resolve the query template for each result
		template, err := betemps.GetTemplateBackend(backend.UseBackend)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			newBackend, err := templates.ResolveTemplateBackend(template, backend.Override, result)
			if err != nil {
				return nil, err
			}
			out = append(out, newBackend)
		}
	}
	return out, nil
}
