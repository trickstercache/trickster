package confpool

import (
	"errors"
	"testing"

	"gopkg.in/yaml.v3"
)

type testObject interface {
	Result() int
}

type testObject1 struct {
	Val1 int `yaml:"val1"`
	Val2 int `yaml:"val2"`
}

func (to *testObject1) Result() int {
	return to.Val1 + to.Val2
}

type testObject2 struct {
	Val3 int `yaml:"val3"`
	Val4 int `yaml:"val4"`
}

func (to *testObject2) Result() int {
	return to.Val3 - to.Val4
}

type testObjectBuilder struct{}

func (b *testObjectBuilder) Build(key string, value *yaml.Node) (testObject, error) {
	var to testObject
	if key == "to1" {
		to = &testObject1{}
	} else if key == "to2" {
		to = &testObject2{}
	} else {
		return nil, errors.New("invalid test object type")
	}
	err := value.Decode(to)
	return to, err
}

var testYaml1 string = `
obj:
    key: to1
    val1: 1
    val2: 2
`
var testYaml2 string = `
obj:
    key: to2
    val3: 1
    val4: 2
`

func TestFactory(t *testing.T) {
	f := New[testObject, *testObjectBuilder]()
	f.SetKey("key")
	err := yaml.Unmarshal([]byte(testYaml1), f)
	if err != nil {
		t.Error(err)
	} else {
		obj, ok := f.Get("obj")
		if !ok {
			t.Error("Expected obj 'obj' to be set on unmarshal")
		}
		if obj.Result() != 3 {
			t.Errorf("Expected result 3, got %d", obj.Result())
		}
		objs := f.All()
		obj, ok = objs["obj"]
		if !ok {
			t.Errorf("Expected f.All to give a map with obj set")
		}
		if obj.Result() != 3 {
			t.Errorf("Expected result 3, got %d", obj.Result())
		}
	}
	err = yaml.Unmarshal([]byte(testYaml2), f)
	if err != nil {
		t.Error(err)
	} else {
		obj, ok := f.Get("obj")
		if !ok {
			t.Error("Expected obj 'obj' to be set on unmarshal")
		}
		if obj.Result() != -1 {
			t.Errorf("Expected result -1, got %d", obj.Result())
		}
	}
}

var testYamlErr1 string = `
obj:
    key: to3
    val3: 1
    val4: 2
`
var testYamlErr2 string = `
obj:
    val3: 1
    val4: 2
`
var testYamlErr3 string = `
obj:
    key: 5
    val3: 1
    val4: 2
`
var testYamlErr4 string = `
obj:
    bad format
	key: to1
	val1: 0
	val2: 1
`

func TestPoolErr(t *testing.T) {
	f := New[testObject, *testObjectBuilder]()
	f.SetKey("key")
	err := yaml.Unmarshal([]byte(testYamlErr1), f)
	if err == nil {
		t.Error("testBuilder should fail with unrecognized key")
	}
	err = yaml.Unmarshal([]byte(testYamlErr2), f)
	if err == nil {
		t.Error("testBuilder should fail with no key")
	}
	err = yaml.Unmarshal([]byte(testYamlErr3), f)
	if err == nil {
		t.Error("testBuilder should fail with int key")
	}
	err = yaml.Unmarshal([]byte(testYamlErr4), f)
	if err == nil {
		t.Error("testBuilder should fail with bad yaml")
	}
}
