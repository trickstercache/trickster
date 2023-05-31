package kubernetes

import (
	"context"
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/backends/alb/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/kube"
)

type KubeClient struct{}

func doInternal(ctx context.Context, kc *kube.Client, opts *do.Options) ([]discovery.Result, error) {
	pods, err := kube.Pods(ctx, kc, opts.Selector)
	if err != nil {
		return nil, err
	}
	ress := make([]discovery.Result, len(pods))
	for i, pod := range pods {
		ress[i] = discovery.Result{
			Name: pod.Meta.Name,
			URL:  pod.Address,
		}
	}
	return ress, nil
}

func doExternal(ctx context.Context, kc *kube.Client, opts *do.Options) ([]discovery.Result, error) {
	svcs, err := kube.Services(ctx, kc, opts.Selector)
	if err != nil {
		return nil, err
	}
	ings, err := kube.Ingresses(ctx, kc, &kube.Selector{})
	if err != nil {
		return nil, err
	}
	// Find every ingress path that matches a service result
	out := make([]discovery.Result, 0)
	// Iterate services
	for _, svc := range svcs {
		host := "localhost"
		// Iterate ingresses
		for _, ing := range ings {
			// Iterate rules
			for _, rule := range ing.Rules {
				// Iterate paths. We can go deeper!
				for _, path := range rule.Paths {
					if path.Backend == nil {
						continue
					} else if path.Backend.Name == svc.Meta.Name {
						if rule.Host != "" {
							host = rule.Host
						}
						out = append(out, discovery.Result{
							Name: svc.Meta.Name,
							URL:  host + path.Path,
						})
					}
				}
			}
		}
	}
	return out, nil
}

func (c *KubeClient) Execute(ctx context.Context, opts *do.Options) ([]discovery.Result, error) {
	if opts.Provider != "kubernetes" || opts.Kubernetes == nil {
		return nil, errors.New("KubeClient must be provided a set of options for Kubernetes service discovery")
	}
	kc, err := kube.New(opts.Kubernetes)
	if err != nil {
		return nil, err
	}
	if opts.Kubernetes.InCluster {
		return doInternal(ctx, kc, opts)
	} else {
		return doExternal(ctx, kc, opts)
	}
}
