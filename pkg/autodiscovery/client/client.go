package client

type Kind string

type Client interface {
	Default()
	Connect(any) error
	Get(any) (any, error)
}
