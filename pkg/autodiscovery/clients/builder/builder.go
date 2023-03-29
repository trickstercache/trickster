package builder

import (
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/etcd"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/kube"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/mock"
	"gopkg.in/yaml.v3"
)

type ClientBuilder struct{}

func (builder *ClientBuilder) Build(provider string, value *yaml.Node) (c clients.Client, err error) {
	switch provider {
	case etcd.Provider:
		ec := etcd.New()
		err = value.Decode(&ec)
		c = ec
	case kube.Provider:
		kc := kube.New()
		err = value.Decode(&kc)
		c = kc
	case mock.Provider:
		mc := mock.New()
		err = value.Decode(&mc)
		c = mc
	default:
		return nil, errors.New("invalid client provider " + provider)
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}
