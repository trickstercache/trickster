package pool

import (
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/client"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/client/builder"
	"gopkg.in/yaml.v3"
)

type ClientPool struct {
	Builders map[string]*builder.ClientBuilder `yaml:"clients"`
	clients  map[string]client.Client          `yaml:"-"`
}

// Unmarshal a ClientPool from a yaml.Node.
// Quick reminder of how yaml.Nodes work:
// each has a tag, value, and some content, where the content is a slice of keys and values as yaml.Nodes.
// ClientPool should unmarshal from ["clients", !!map] where !!map can unmarshal *into* pool.Builders.
func (pool *ClientPool) UnmarshalYAML(value *yaml.Node) (err error) {
	pool.Builders = make(map[string]*builder.ClientBuilder)
	err = value.Content[1].Decode(pool.Builders)
	if err != nil {
		return err
	}
	pool.clients = make(map[string]client.Client)
	for name, builder := range pool.Builders {
		pool.clients[name], err = builder.Build()
		if err != nil {
			return err
		}
	}
	return nil
}

func (pool *ClientPool) Get(clientName string) (c client.Client, ok bool) {
	c, ok = pool.clients[clientName]
	return
}

func (pool *ClientPool) List() (clients []string) {
	clients = make([]string, 0)
	for k := range pool.clients {
		clients = append(clients, k)
	}
	return clients
}
