package queries

type QueryKind string

const (
	NilQuery  = QueryKind("nil")
	EtcdQuery = QueryKind("etcd")
	KubeQuery = QueryKind("kube")
)

type Query struct {
	Kind       QueryKind      `yaml:"kind"`
	Client     string         `yaml:"client"`
	Template   string         `yaml:"template"`
	Parameters map[string]any `yaml:"parameters"`
}

func New() *Query {
	return &Query{
		Kind:     NilQuery,
		Client:   "",
		Template: "",
	}
}

func (q *Query) Clone() *Query {
	return &Query{
		Kind:     q.Kind,
		Client:   q.Client,
		Template: q.Template,
	}
}
