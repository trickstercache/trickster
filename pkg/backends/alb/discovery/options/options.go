package options

import (
	"github.com/trickstercache/trickster/v2/pkg/kube"
	ko "github.com/trickstercache/trickster/v2/pkg/kube/options"
)

type Options struct {
	Provider   string         `yaml:"provider"`
	Kubernetes *ko.Options    `yaml:"kubernetes"`
	Selector   *kube.Selector `yaml:"selector"`
	Target     string         `yaml:"target"`
}
