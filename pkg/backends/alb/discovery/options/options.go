package options

import (
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	ko "github.com/trickstercache/trickster/v2/pkg/kube/options"
)

type Options struct {
	Kubernetes *ko.Options    `yaml:"kubernetes"`
	Selector   *kube.Selector `yaml:"selector"`
	Template   *bo.Options    `yaml:"template"`
}
