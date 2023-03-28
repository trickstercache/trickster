package confpool

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfKey = "provider"
)

type ConfObject any

type ConfKey string

type ConfObjectBuilder[O ConfObject] interface {
	Build(string, *yaml.Node) (O, error)
}

type ConfObjectPool[O ConfObject, B ConfObjectBuilder[O]] struct {
	key  ConfKey
	objs map[string]O
}

func New[O ConfObject, B ConfObjectBuilder[O]]() *ConfObjectPool[O, B] {
	return &ConfObjectPool[O, B]{
		key: DefaultConfKey,
	}
}

func (pool *ConfObjectPool[O, B]) SetKey(key ConfKey) {
	pool.key = key
}

// UnmarshalYAML unmarshals the contents of a yaml node into a ClientPool.
// In the context of an autodiscovery.Options, this is called on clients: !!map, so
// value.Content[1] is the actual mapping of client configs.
func (pool *ConfObjectPool[O, B]) UnmarshalYAML(value *yaml.Node) error {
	m := make(popMap)
	err := value.Decode(&m)
	if err != nil {
		return err
	}
	b := (*new(B))
	pool.objs = make(map[string]O)
	for name, entry := range m {
		k, ok := entry.Map[string(pool.key)]
		if !ok {
			return fmt.Errorf("object pool entries must contain key %s to assign concrete type", pool.key)
		}
		key, ok := k.(string)
		if !ok {
			return fmt.Errorf("object pool key %s must have string value", pool.key)
		}
		pool.objs[name], err = b.Build(key, entry.Value)
		if err != nil {
			return err
		}
	}
	return nil
}

func (pool *ConfObjectPool[O, B]) Get(name string) (obj O, ok bool) {
	obj, ok = pool.objs[name]
	return
}

func (pool *ConfObjectPool[O, B]) All() map[string]O {
	return pool.objs
}

type popMapEntry struct {
	Map   map[string]any
	Value *yaml.Node
}

func (entry *popMapEntry) UnmarshalYAML(value *yaml.Node) error {
	entry.Map = make(map[string]any)
	err := value.Decode(&entry.Map)
	if err != nil {
		return err
	}
	entry.Value = value
	return nil
}

type popMap map[string]popMapEntry
