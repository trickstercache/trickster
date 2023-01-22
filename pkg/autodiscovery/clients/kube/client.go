package kube

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/kube"
	"k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	v1net "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	Provider = string("kube")
)

type Client struct {
	UseQueries []string `yaml:"queries"`
	InCluster  bool     `yaml:"in_cluster"`
	ConfigPath string   `yaml:"config,omitempty"`
	core       v1core.CoreV1Interface
	net        v1net.NetworkingV1Interface
}

func (client *Client) Queries() []string {
	return client.UseQueries
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

func (client *Client) Execute(q queries.Query) (queries.Results, error) {
	var query *kube.Query
	switch q := q.(type) {
	case *kube.Query:
		query = q
	default:
		return nil, fmt.Errorf("kube.Client requires kube.Query query")
	}
	rms, err := client.aggregateResources(query.Namespace, query.ResourceKinds...)
	if err != nil {
		return nil, err
	}
	qress := make(queries.Results, 0)
	for _, rm := range rms {
		matches := rm.Match(query)
		if matches {
			add, ok := rms.GetAccessFor(rm, query.AccessBy)
			if !ok {
				continue
			}
			res := queries.Result{
				"address": add,
			}
			qress = append(qress, res)
		}
	}
	return qress, nil
}
