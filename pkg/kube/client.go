package kube

import (
	"github.com/trickstercache/trickster/v2/pkg/kube/options"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	cs kubernetes.Interface
}

// New creates a new Kubernetes client using the provided options.
// The documentation for options covers this process in detail.
func New(opts *options.Options) (*Client, error) {
	var conf *rest.Config
	var err error
	if opts.InCluster {
		conf, err = rest.InClusterConfig()
	} else {
		conf, err = clientcmd.BuildConfigFromFlags("", opts.ConfigPath)
	}
	if err != nil {
		return nil, err
	}
	c := &Client{}
	c.cs, err = kubernetes.NewForConfig(conf)
	return c, err
}
