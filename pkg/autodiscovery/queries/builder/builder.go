package builder

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/etcd"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/kube"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/mock"
	"gopkg.in/yaml.v3"
)

type QueryBuilder struct{}

func (builder *QueryBuilder) Build(kind string, value *yaml.Node) (q queries.Query, err error) {
	switch kind {
	case etcd.Kind:
		eq := &etcd.Query{}
		err = value.Decode(&eq)
		q = eq
	case kube.Kind:
		kq := &kube.Query{}
		err = value.Decode(&kq)
		q = kq
	case mock.Kind:
		mq := &mock.Query{}
		err = value.Decode(&mq)
		q = mq
	default:
		return nil, fmt.Errorf("invalid query builder kind %s", kind)
	}
	if err != nil {
		return nil, err
	}
	return q, nil
}
