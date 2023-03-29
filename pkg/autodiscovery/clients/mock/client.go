package mock

import (
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/mock"
)

const (
	Provider = string("mock")
)

type Client struct {
	UseQueries []string `yaml:"queries"`
	connected  bool
}

func New() *Client {
	return &Client{}
}

func (client *Client) Queries() []string {
	return client.UseQueries
}

func (client *Client) Connect() error {
	client.connected = true
	return nil
}

func (client *Client) Disconnect() {
	client.connected = false
}

func (client *Client) Execute(q queries.Query) (queries.Results, error) {
	if !client.connected {
		return nil, errors.New("client not connected")
	}
	var query *mock.Query
	switch q := q.(type) {
	case *mock.Query:
		{
			query = q
		}
	default:
		{
			return nil, errors.New("not a mock query")
		}
	}
	qress := make(queries.Results, 0)
	qres := make(queries.Result)
	qres["result"] = query.GiveResult
	qress = append(qress, qres)
	return qress, nil
}
