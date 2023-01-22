package etcd

const (
	Kind = string("etcd")
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
