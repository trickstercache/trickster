package etcd

const (
	Kind = string("etcd")
)

type Query struct {
	UseTemplate string   `yaml:"template"`
	Keys        []string `yaml:"keys"`
}

func (q *Query) Template() string {
	return q.UseTemplate
}
