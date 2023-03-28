package flux

import (
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

type Query struct {
	Extent    timeseries.Extent
	Step      time.Duration
	Statement string
}
