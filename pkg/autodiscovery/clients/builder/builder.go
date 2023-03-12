package builder

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/etcd"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/kube"
	"gopkg.in/yaml.v3"
)

type ClientBuilder struct{}

func (builder *ClientBuilder) Build(provider string, value *yaml.Node) (c clients.Client, err error) {
	switch provider {
	case etcd.Provider:
		c = etcd.New()
	case kube.Provider:
		c = kube.New()
	}
	err = value.Decode(c)
	if err != nil {
		return nil, err
	}
	return c, nil
}
