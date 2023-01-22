package kube

import "github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/kube"

type Query struct {
	ResourceKind      kube.ResourceKind   `yaml:"resource_kind"`
	ResourceName      string              `yaml:"resource_name"`
	HasLabel          []string            `yaml:"has_label"`
	HasLabelWithValue map[string][]string `yaml:"has_label_with_value"`
	AccessBy          kube.AccessKind     `yaml:"access_by"`
}
