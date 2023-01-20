package kube

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	v1net "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	Kind = clients.Kind("kube")
)

type Client struct {
	InCluster  bool   `yaml:"in_cluster"`
	ConfigPath string `yaml:"config,omitempty"`
	core       v1core.CoreV1Interface
	net        v1net.NetworkingV1Interface
}

func (client *Client) Default() {
	client.InCluster = false
	client.ConfigPath = ""
	client.core = nil
	client.net = nil
}

func (client *Client) Connect() error {
	var cfg *rest.Config
	var err error
	if !client.InCluster {
		cfg, err = clientcmd.BuildConfigFromFlags("", client.ConfigPath)
	} else {
		cfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	client.core = clientset.CoreV1()
	client.net = clientset.NetworkingV1()
	return nil
}

func (client *Client) Disconnect() {
	client.core = nil
	client.net = nil
}

func (client *Client) Execute(q *queries.Query) (queries.Results, error) {
	if q.Kind != queries.KubeQuery {
		return nil, fmt.Errorf("%s client requires %s query", Kind, queries.KubeQuery)
	}
	out := make(queries.Results, 0)
	return nil, nil
}
