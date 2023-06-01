package kube

import (
	"testing"

	"gopkg.in/yaml.v3"
)

var testYaml = `
namespace: default
name: testName
hasLabels: [ app ]
matchLabels:
  app: trickster
`

func TestSelector(t *testing.T) {
	var sel *Selector
	err := yaml.Unmarshal([]byte(testYaml), &sel)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Namespace != "default" {
		t.Error()
	}
	if sel.Name != "testName" {
		t.Error()
	}
	if len(sel.HasLabels) != 1 {
		t.Error(sel.HasLabels)
	}
	if len(sel.MatchLabels) != 1 {
		t.Error(sel.MatchLabels)
	}
}
