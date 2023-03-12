package flux

import "github.com/trickstercache/trickster/v2/pkg/timeseries"

type Query struct {
	Extent    timeseries.Extent
	Statement string
}
