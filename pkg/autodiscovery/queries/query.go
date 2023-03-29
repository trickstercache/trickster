package queries

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/etcd"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/kube"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/mock"
	"golang.org/x/exp/slices"
)

type Query interface {
	Template() string
}

var queries = []string{etcd.Kind, kube.Kind, mock.Kind}

func IsSupportedKind(kind string) bool {
	return slices.Contains(queries, kind)
}
