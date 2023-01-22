package pop

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type PolyObject any

type Kind string

type PolyObjectBuilder[O PolyObject] interface {
	Build(Kind, *yaml.Node) (O, error)
}

type PolyObjectPool[O PolyObject, B PolyObjectBuilder[O]] struct {
	objs map[string]O
}

func New[O PolyObject, B PolyObjectBuilder[O]]() *PolyObjectPool[O, B] {
	return &PolyObjectPool[O, B]{}
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
		pool.objs[name], err = b.Build(entry.Kind, entry.Value)
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
	Kind  Kind
	Value *yaml.Node
}

func (entry *popMapEntry) UnmarshalYAML(value *yaml.Node) error {
	m := make(map[string]any)
	value.Decode(m)
	kind, ok := m["kind"]
	if !ok {
		return fmt.Errorf("ClientPool requires kind 'kind' parameter for clients")
	}
	entry.Kind = Kind(kind.(string))
	entry.Value = value
	return nil
}

type popMap map[string]popMapEntry
