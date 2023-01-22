package etcd

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/pop"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	Kind = pop.Kind("etcd")
)

type Client struct {
	Endpoints []string `yaml:"endpoints"`
	client    *clientv3.Client
}

func (client *Client) Default() {
	client.Endpoints = []string{}
}
func (client *Client) Connect() (err error) {
	client.client, err = clientv3.New(clientv3.Config{
		Endpoints: client.Endpoints,
	})
	if err != nil {
		client.client = nil
		return err
	}
	return nil
}
func (client *Client) Disconnect() {
	client.client.Close()
	client.client = nil
}
func (client *Client) Execute(q queries.Query) (queries.Results, error) {
	return nil, nil
}
