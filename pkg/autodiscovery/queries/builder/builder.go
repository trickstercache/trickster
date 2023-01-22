package builder

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/etcd"
	"gopkg.in/yaml.v3"
)

type QueryBuilder struct{}

func (builder *QueryBuilder) Build(kind string, value *yaml.Node) (q queries.Query, err error) {
	switch kind {
	case etcd.Kind:
		q = &etcd.Query{}
	}
	err = value.Decode(q)
	if err != nil {
		return nil, err
	}
	return q, nil
}
