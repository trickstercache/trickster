package options

import (
	"testing"

	"gopkg.in/yaml.v3"
)

var testYaml = `
in_cluster: false
config_path: ~/.kubeconfig
`

func TestOptions(t *testing.T) {
	var opts *Options
	err := yaml.Unmarshal([]byte(testYaml), &opts)
	if err != nil {
		t.Fatal(err)
	}
	if opts.InCluster {
		t.Error("expected out-of-cluster")
	}
	if opts.ConfigPath != "~/.kubeconfig" {
		t.Errorf("expected path ~/.kubeconfig, got %s", opts.ConfigPath)
	}
}
