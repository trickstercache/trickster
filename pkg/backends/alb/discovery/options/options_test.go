package options

import (
	"testing"

	"gopkg.in/yaml.v3"
)

var testYaml = `
kubernetes:
  in_cluster: false
  config_path: ~/.kubeconfig
selector:
  namespace: default
  matchLabels:
    app: prometheus
template:
  latency_max_ms: 150
  latency_min_ms: 50
  is_default: true
  provider: prometheus
  cache_name: mem1
  tls:
    full_chain_cert_path: >-
      /private/data/trickster/docker-compose/data/trickster-config/127.0.0.1.pem
    private_key_path: >-
      /private/data/trickster/docker-compose/data/trickster-config/127.0.0.1-key.pem
    insecure_skip_verify: true
`

func TestKubernetes(t *testing.T) {
	var opts *Options
	err := yaml.Unmarshal([]byte(testYaml), &opts)
	if err != nil {
		t.Fatal(err)
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
