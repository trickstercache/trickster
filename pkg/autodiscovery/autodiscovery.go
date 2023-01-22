// package autodiscovery provides methods for backends to discover other backend services.
package autodiscovery

import (
	"fmt"

	adopt "github.com/trickstercache/trickster/v2/pkg/autodiscovery/options"
	beopt "github.com/trickstercache/trickster/v2/pkg/backends/options"
	betemps "github.com/trickstercache/trickster/v2/pkg/backends/templates"
)

// Run autodiscovery with a set of options, returning backend options for matched queries
func DiscoverWithOptions(opts *adopt.Options) ([]*beopt.Options, error) {
	fmt.Printf("Running autodiscovery with\nQueries:%+v\nTemplates:%+v\n", opts.Queries, opts.Templates)
	clients := opts.Clients
	queries := opts.Queries
	templates := opts.Templates
	out := make([]*beopt.Options, 0)
	// Range over all autodiscovery queries
	for queryName, query := range queries.All() {
		// If there's no backend attached to this query, there's not much point in running it.
		template, hasTemplate := templates[query.Template()]
		if !hasTemplate {
			continue
		}
		// Get the method for this query.
		client, ok := clients.Get(query.Client())
		if !ok {
			return nil, fmt.Errorf("failed to get client %s from pool for query %s", query.Client(), queryName)
		}
		// Run the query and store the results
		results, err := client.Execute(query)
		if err != nil {
			return nil, err
		}
		// Get and resolve the query template for each result
		backend, err := betemps.GetTemplateBackend(template.UseBackend)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			newBackend, err := betemps.ResolveTemplateBackend(backend, template.Override, result)
			if err != nil {
				return nil, err
			}
			out = append(out, newBackend)
		}
	}
	return out, nil
}
