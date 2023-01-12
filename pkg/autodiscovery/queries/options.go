package queries

type QueryParams interface{}

// QueryParameters map query parameters to their required values.
// ex. query kubernetes for any service with name "test-service" that is accessed through an ingress
type QueryParameters map[string]string

// QueryResults map values of successful query objects to template keys.
// ex. for every service found, map its external address to a template key ORIGIN_URL.
type QueryResults map[string]string

// Represents the values needed to perform a query
type Options struct {
	Method      string          `yaml:"method"`
	UseTemplate string          `yaml:"use_template"`
	Parameters  QueryParameters `yaml:"parameters,omitempty"`
	Results     QueryResults    `yaml:"results,omitempty"`
}

// Create an empty query options object
func New() *Options {
	return &Options{
		Method:      "NO METHOD",
		UseTemplate: "NO TEMPLATE",
		Parameters:  make(QueryParameters),
		Results:     make(QueryResults),
	}
}

func (opt *Options) Clone() *Options {
	outParams := make(QueryParameters)
	for k, v := range opt.Parameters {
		outParams[k] = v
	}
	outResults := make(QueryResults)
	for k, v := range opt.Results {
		outResults[k] = v
	}
	return &Options{
		Method:      opt.Method,
		UseTemplate: opt.UseTemplate,
		Parameters:  outParams,
		Results:     outResults,
	}
}
