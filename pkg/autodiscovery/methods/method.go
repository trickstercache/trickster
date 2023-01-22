package methods

/*

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// Defines an interface for autodiscovery methods.
type Method interface {
	// Name gives the config name of the autodiscovery method.
	Name() string
	// Init should initialize a struct, starting from empty, ex.
	//   method = &KubeExt2{}.Init()
	// Methods should not do anything before Init() is called.
	Init() error
	// IsInitialized should return if the method has had Init() called yet.
	IsInitialized() bool
	// RequiredParameters maps each required query parameter to its supported string values.
	// For a query to be valid, every required parameter must be included with a supported value.
	// Required parameters cannot have wildcard values.
	RequiredParameters() map[string][]string
	// SupportedParameters maps each supported query parameter for the method to a slice
	// of supported string values. For a query to be valid, every parameter and its value must
	// be supported.
	SupportedParameters() map[string][]string
	// SupportedResults lists the supported result keys for the method.
	// Results must have a value for every supported result key.
	SupportedResults() []string

	Query(*queries.Options) ([]queries.QueryResults, error)
}

// Runs a check against a set of parameters and a Method to check if the query
// is supported.
func ParametersSupported(m Method, q queries.QueryParameters) bool {
	required := m.RequiredParameters()
	supported := m.SupportedParameters()
	// Check required params
	for param, values := range required {
		value, ok := q[param]
		// Required parameter missing; not supported
		if !ok {
			return false
		}
		// Required parameter has unsupported value
		if strings.IndexInSlice(values, value) == -1 {
			return false
		}
	}
	// Check against supported params
	for param, value := range q {
		values, ok := supported[param]
		// Parameter is not supported
		if !ok {
			return false
		}
		// Parameter is supported and has a wildcard value
		if len(values) == 1 && values[0] == "*" {
			continue
		}
		// Parameter is supported but the value is not
		if strings.IndexInSlice(values, value) == -1 {
			return false
		}
	}
	return true
}

// Runs a check against a set of results and a Method to see if a query returned
// a valid set of results.
func ResultSupported(m Method, q queries.QueryResults) bool {
	required := m.SupportedResults()
	for _, requiredKey := range required {
		if _, ok := q[requiredKey]; !ok {
			return false
		}
	}
	return true
}

// QueryResults map each query name to a slice of valid results of a query.
type QueryResult map[string][]string
*/
