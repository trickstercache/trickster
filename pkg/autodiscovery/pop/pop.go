package pop

import (
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"
)

const (
	DefaultPolyKey = "provider"
)

type PolyObject any

type PolyKey string

type PolyObjectBuilder[O PolyObject] interface {
	Build(string, *yaml.Node) (O, error)
}

type PolyObjectPool[O PolyObject, B PolyObjectBuilder[O]] struct {
	key  PolyKey
	objs map[string]O
}

func New[O PolyObject, B PolyObjectBuilder[O]]() *PolyObjectPool[O, B] {
	return &PolyObjectPool[O, B]{
		key: DefaultPolyKey,
	}
}

func (pool *PolyObjectPool[O, B]) WithKey(key PolyKey) {
	pool.key = key
}

// Unmarshal the contents of a yaml node into a ClientPool.
// In the context of an autodiscovery.Options, this is called on clients: !!map, so
// value.Content[1] is the actual mapping of client configs.
func (pool *PolyObjectPool[O, B]) UnmarshalYAML(value *yaml.Node) error {
	fmt.Println("Unmarshalling pop")
	m := make(popMap)
	err := value.Decode(&m)
	if err != nil {
		return err
	}
	fmt.Println(m)
	b := (*new(B))
	pool.objs = make(map[string]O)
	for name, entry := range m {
		k, ok := entry.Map[string(pool.key)]
		if !ok {
			return fmt.Errorf("object pool entries must contain key %s to assign concrete type", pool.key)
		}
		key, ok := k.(string)
		if !ok {
			return fmt.Errorf("object pool key %s must have string value, got %s", pool.key, reflect.TypeOf(k).String())
		}
		pool.objs[name], err = b.Build(key, entry.Value)
		if err != nil {
			return err
		}
	}
	return nil
}

func (pool *PolyObjectPool[O, B]) Get(name string) (obj O, ok bool) {
	obj, ok = pool.objs[name]
	return
}

func (pool *PolyObjectPool[O, B]) All() map[string]O {
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
