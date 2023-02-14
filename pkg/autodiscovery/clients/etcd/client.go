package etcd

import (
	"context"
	"fmt"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	Kind = string("etcd")
)

type Client struct {
	UseQueries    []string `yaml:"queries"`
	Endpoints     []string `yaml:"endpoints"`
	DialTimeoutMS int64    `yaml:"dial_timeout_ms"`
	client        *clientv3.Client
}

func New() *Client {
	return &Client{
		UseQueries:    []string{},
		Endpoints:     []string{},
		DialTimeoutMS: 1000,
		client:        nil,
	}
}

func (client *Client) Queries() []string {
	return client.UseQueries
}

func (client *Client) Connect() (err error) {
	client.client, err = clientv3.New(clientv3.Config{
		Endpoints:   client.Endpoints,
		DialTimeout: time.Millisecond * time.Duration(client.DialTimeoutMS),
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
	if client.client == nil {
		return nil, fmt.Errorf("must connect to etcd.Client before Execute()")
	}
	var query *etcd.Query
	switch q := q.(type) {
	case *etcd.Query:
		query = q
	default:
		return nil, fmt.Errorf("etcd.Client requires etcd.Query")
	}
	qress := make(queries.Results, 0)
	qres := make(queries.Result)
	kvs := clientv3.NewKV(client.client)
	for _, k := range query.Keys {
		res, err := kvs.Get(context.TODO(), k)
		if err != nil {
			continue
		}
		if len(res.Kvs) != 1 {
			return nil, fmt.Errorf("basic etcd queries expect 1 result per key; got %d for %s", len(res.Kvs), k)
		}
		qres[k] = string(res.Kvs[0].Value)
	}
	qress = append(qress, qres)
	return qress, nil
}
