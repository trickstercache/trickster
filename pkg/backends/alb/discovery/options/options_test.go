package options

import (
	"testing"

	"gopkg.in/yaml.v3"
)

var testYaml = `
provider: kubernetes
kubernetes:
  in_cluster: false
  config_path: ~/.kubeconfig
selector:
  namespace: default
  matchLabels:
    app: prometheus
targets:
  prometheus: prom_mock
`

func TestKubernetes(t *testing.T) {
	var opts *Options
	err := yaml.Unmarshal([]byte(testYaml), &opts)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Provider != "kubernetes" {
		t.Error("expected kubernetes")
	}
	if k8s := opts.Kubernetes; k8s == nil {
		t.Error("expected kubernetes client")
	} else if k8s.InCluster {
		t.Error("expected out-of-cluster")
	} else if k8s.ConfigPath != "~/.kubeconfig" {
		t.Error("wrong configpath")
	}
	if sel := opts.Selector; sel == nil {
		t.Error("expected selector")
	} else if sel.Namespace != "default" {
		t.Error("expected default")
	} else if len(sel.MatchLabels) != 1 {
		t.Errorf("expected populated matchLabels, got %v", sel.MatchLabels)
	}
}
