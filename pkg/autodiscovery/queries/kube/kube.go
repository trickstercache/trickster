package kube

const (
	Provider = string("kube")
)

type Query struct {
	UseTemplate       string              `yaml:"template"`
	Namespace         string              `yaml:"namespace"`
	ResourceKinds     []string            `yaml:"resource_kinds,omitempty"`
	ResourceName      string              `yaml:"resource_name,omitempty"`
	HasLabel          []string            `yaml:"has_label,omitempty"`
	HasLabelWithValue map[string][]string `yaml:"has_label_with_value,omitempty"`
	AccessBy          string              `yaml:"access_by"`
}

func (q *Query) Template() string {
	return q.UseTemplate
}
