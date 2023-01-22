package queries

type QueryKind string

const (
	NilQuery  = QueryKind("nil")
	EtcdQuery = QueryKind("etcd")
	KubeQuery = QueryKind("kube")
)

type Query interface {
	Client() string
	Template() string
}

func Cast[Q Query](q Query) (casted Q, ok bool) {
	casted, ok = q.(Q)
	return
}
