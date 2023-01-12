package etcd

import "github.com/trickstercache/trickster/v2/pkg/autodiscovery/client"

const (
	Kind = client.Kind("etcd")
)

type Client struct {
	Endpoints []string `yaml:"endpoints"`
}

func (client *Client) Default() {
	client.Endpoints = []string{}
}
func (client *Client) Connect(any) error {
	return nil
}
func (client *Client) Get(any) (any, error) {
	return nil, nil
}
