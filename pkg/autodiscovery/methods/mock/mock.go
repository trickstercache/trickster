package mock

/*
import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/methods"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
)

type Mock struct {
	Initialized bool
}

func (m *Mock) Name() string {
	return "mock"
}

func (m *Mock) Init() error {
	m.Initialized = true
	return nil
}

func (m *Mock) IsInitialized() bool {
	return m.Initialized
}

func (m *Mock) RequiredParameters() map[string][]string {
	return map[string][]string{
		"RequiredParameter": {"MustBeThisValue"},
	}
}

func (m *Mock) SupportedParameters() map[string][]string {
	return map[string][]string{
		"RequiredParameter":  {"MustBeThisValue"},
		"SupportedParameter": {"*"},
	}
}

func (m *Mock) SupportedResults() []string {
	return []string{
		"RequiredResultKey",
	}
}

func (m *Mock) Query(opts *queries.Options) ([]queries.QueryResults, error) {
	if !m.IsInitialized() {
		m.Init()
	}
	params := opts.Parameters
	resultsMap := opts.Results
	if !methods.ParametersSupported(m, params) {
		return nil, fmt.Errorf("Query is missing required parameter")
	}

	// Mock output
	output := make([]queries.QueryResults, 0)
	output = append(output, make(queries.QueryResults))
	output[0]["RequiredResultKey"] = "SomeResult"

	// Format template results
	templateValues := make([]queries.QueryResults, 0)
	for idx := range output {
		templateValues = append(templateValues, make(queries.QueryResults))
		for queryKey, queryValue := range output[idx] {
			if templateKey, ok := resultsMap[queryKey]; ok {
				templateValues[idx][templateKey] = queryValue
			}
		}
	}

	return templateValues, nil

}
*/
