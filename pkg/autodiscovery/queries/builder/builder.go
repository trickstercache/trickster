package builder

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/etcd"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/kube"
	"gopkg.in/yaml.v3"
)

type QueryBuilder struct{}

func (builder *QueryBuilder) Build(kind string, value *yaml.Node) (q queries.Query, err error) {
	switch kind {
	case etcd.Kind:
		q = &etcd.Query{}
	case kube.Provider:
		q = &kube.Query{}
	default:
		return nil, fmt.Errorf("invalid query builder kind %s", kind)
	}
	err = value.Decode(q)
	if err != nil {
		return nil, err
	}
	return q, nil
}
