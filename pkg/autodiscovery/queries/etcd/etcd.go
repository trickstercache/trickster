package etcd

import "github.com/trickstercache/trickster/v2/pkg/autodiscovery/pop"

const (
	Kind = pop.Kind("etcd")
)

type Query struct {
	UseClient   string   `yaml:"client"`
	UseTemplate string   `yaml:"template"`
	Keys        []string `yaml:"keys"`
}

func (q *Query) Client() string {
	return q.UseClient
}

func (q *Query) Template() string {
	return q.UseTemplate
}
