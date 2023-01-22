package options

import (
	"fmt"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/templates"
	"gopkg.in/yaml.v3"
)

func TestOptions(t *testing.T) {
	// New; new options object should have empty Queries/Backends
	o1 := New()
	if i := len(o1.Templates); i != 0 {
		t.Errorf("New options should have Backends len 0, has len %d", i)
	}
	if i := len(o1.Queries.All()); i != 0 {
		t.Errorf("New options should have Queries len 0, has len %d", i)
	}

	// Create a test backend and clone the options object; test equality
	o1.Templates["test_backend"] = &templates.Options{
		UseBackend: "test",
		Override:   make(templates.OverrideMap),
	}
	o2 := o1.Clone()

	if b1, b2 := o1.Templates["test_backend"].UseBackend, o2.Templates["test_backend"].UseBackend; b1 != b2 {
		t.Errorf("Options should have equivalent backends[test_backend].UseBackend; got %s and %s", b1, b2)
	}

	// Replacing value in o2 shouldn't change o1
	o2.Templates["test_backend"].UseBackend = "test2"

	if b1 := o1.Templates["test_backend"].UseBackend; b1 == "test2" {
		t.Errorf("Changing an options clone shouldn't change the original")
	}
}

var testConf1 string = `
  clients:
    client_etcd:
      provider: etcd
      queries: [query_etcd]
      endpoints:
        - 127.0.0.1:8080
        - localhost:8080
  queries:
    query_etcd:
      provider: etcd
      template: template_test
      keys: [url, path]
  templates:
    template_test:
      use_backend: mock
      override:
        some_backend_key: 'https://$[url]$[path]'
    
`

var testConf2 string = `
clients:
  client_kube_int:
    provider: kube
    in_cluster: true
    queries: [query_kube_pod]
  client_kube_ext:
    provider: kube
    config: /some/path
    queries: [query_kube_service, query_kube_ingress]
queries:
  query_kube_pod:
    provider: kube
    namespace: default
    resource_kinds: [pod]
    resource_name: target_pod
    has_label_with_value:
      is_target: ["true"]
    access_by: resource_ip
  query_kube_service:
    provider: kube
    namespace: default
    resource_kinds: [service]
    has_label_with_value:
      is_target: ["true"]
    access_by: resource_ip
  query_kube_ingress:
    provider: kube
    namespace: default
    resource_kinds: [service, ingress]
    has_label_with_value:
      is_target: ["true"]
    access_by: ingress
templates:
  template_kube:
    use_backend: mock
    override:
      some_backend_key: "https://$[address]"
`

func TestYAML(t *testing.T) {
	t.Run("etcd", func(t *testing.T) {
		opts := New()
		err := yaml.Unmarshal([]byte(testConf1), &opts)
		if err != nil {
			t.Error(err)
		}
		client, ok := opts.Clients.Get("client_etcd")
		if !ok {
			t.Error("Expected client_etcd to be ok")
		}
		query, ok := opts.Queries.Get("query_etcd")
		if !ok {
			t.Error("Expected query_etcd to be ok")
		}
		fmt.Printf("%+v\n", client)
		fmt.Printf("+%v\n", query)
	})
	t.Run("kube", func(t *testing.T) {
		opts := New()
		err := yaml.Unmarshal([]byte(testConf2), &opts)
		if err != nil {
			t.Error(err)
		}
		_, ok := opts.Clients.Get("client_kube_int")
		if !ok {
			t.Error("Expected client_kube_int to be ok")
		}
		_, ok = opts.Clients.Get("client_kube_ext")
		if !ok {
			t.Error("Expected client_kube_ext to be ok")
		}
		_, ok = opts.Queries.Get("query_kube_pod")
		if !ok {
			t.Error("Expected query_kube_pod to be ok")
		}
		_, ok = opts.Queries.Get("query_kube_service")
		if !ok {
			t.Error("Expected query_kube_service to be ok")
		}
		_, ok = opts.Queries.Get("query_kube_ingress")
		if !ok {
			t.Error("Expected query_kube_ingress to be ok")
		}
	})
}
