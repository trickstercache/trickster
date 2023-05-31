package clients

import (
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/discovery"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/discovery/clients/kubernetes"
)

func New(provider string) (discovery.Client, error) {
	switch provider {
	case "kubernetes":
		return &kubernetes.KubeClient{}, nil
	}
	return nil, errors.New("unrecognized provider " + provider)
}
