package kubernetes

import (
	"context"
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/backends/alb/discovery/options"
)

type KubeClient struct{}

func (c *KubeClient) Execute(ctx context.Context, opts *do.Options) ([]discovery.Result, error) {
	if opts.Provider != "kubernetes" || opts.Kubernetes == nil {
		return nil, errors.New("KubeClient must be provided a set of options for Kubernetes service discovery")
	}

	return nil, nil
}
