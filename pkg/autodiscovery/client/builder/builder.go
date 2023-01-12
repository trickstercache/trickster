package builder

import (
	"errors"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/client"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/client/etcd"
	"gopkg.in/yaml.v3"
)

const (
	keyKind = "kind"
)

type ClientBuilder struct {
	yaml.Unmarshaler
	Kind  client.Kind `yaml:"kind"`
	Value *yaml.Node
}

func (builder *ClientBuilder) UnmarshalYAML(value *yaml.Node) (err error) {
	vm := make(map[string]any)
	value.Decode(&vm)
	kindValue, ok := vm[keyKind]
	if !ok {
		return errors.New("unmarshalling into ClientBuilder requires keyKind 'kind'")
	}
	kindString, ok := kindValue.(string)
	if !ok {
		return errors.New("unmarshalling into ClientBuilder requires *string* keyKind 'kind'")
	}
	builder.Kind = client.Kind(kindString)
	builder.Value = value
	return nil
}

func (builder *ClientBuilder) Build() (c client.Client, err error) {
	switch builder.Kind {
	case etcd.Kind:
		c = &etcd.Client{}
	}
	fmt.Printf("%+v\n", builder.Value)
	err = builder.Value.Decode(c)
	if err != nil {
		return nil, err
	}
	return c, nil
}
