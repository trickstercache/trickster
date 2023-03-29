package clients

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/etcd"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/kube"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/mock"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"golang.org/x/exp/slices"
)

type Kind string

type Client interface {
	Queries() []string
	Connect() error
	Disconnect()
	Execute(queries.Query) (queries.Results, error)
}

var providers = []string{etcd.Provider, kube.Provider, mock.Provider}

func IsSupportedClient(provider string) bool {
	return slices.Contains(providers, provider)
}
