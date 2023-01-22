package builder

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/etcd"
	"gopkg.in/yaml.v3"
)

type ClientBuilder struct{}

func (builder *ClientBuilder) Build(kind string, value *yaml.Node) (c clients.Client, err error) {
	switch kind {
	case etcd.Kind:
		c = &etcd.Client{}
	}
	err = value.Decode(c)
	if err != nil {
		return nil, err
	}
	return c, nil
}
