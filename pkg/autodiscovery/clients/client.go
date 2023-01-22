package clients

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
)

type Kind string

type Client interface {
	Default()
	Connect() error
	Disconnect()
	Execute(queries.Query) (queries.Results, error)
}
