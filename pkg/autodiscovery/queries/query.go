package queries

type QueryKind string

const (
	NilQuery  = QueryKind("nil")
	EtcdQuery = QueryKind("etcd")
	KubeQuery = QueryKind("kube")
)

type Query interface {
	Template() string
}
