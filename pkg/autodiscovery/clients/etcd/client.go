package etcd

import (
	"github.com/coreos/etcd/clientv3"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
)

const (
	Kind = clients.Kind("etcd")
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
func (client *Client) Execute(q *queries.Query) (queries.Results, error) {
	return nil, nil
}
