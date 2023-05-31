package kube

import "k8s.io/utils/strings/slices"

type Selector struct {
	// Namespace requires the selected resources to be in one namespace.
	// An empty string will search the default namespace.
	Namespace string `yaml:"namespace"`
	// Name requires the name of the resource to match the provided string.
	Name string `yaml:"name"`
	// HasLabels requires that all labels provided are on every resource found.
	// The value isn't considered; use MatchLabels to match key-value pairs.
	HasLabels []string `yaml:"hasLabel,omitempty"`
	// MatchLabels requires that all labels and values are on every resource found.
	MatchLabels map[string]string `yaml:"matchLabel,omitempty"`
}

// NameOK checks if the name of a resource matches the selector.
func (s *Selector) NameOK(name string) bool {
	return s.Name == "" || s.Name == name
}

// LabelsOK checks if the labels of a resource match the selector.
func (s *Selector) LabelsOK(labels map[string]string) bool {
	found := 0
	for k, v := range labels {
		// Return false if we're matching this label but don't have the value we need
		if need, ok := s.MatchLabels[k]; ok && v != need {
			return false
		}
		// Count the number of required labels found
		if slices.Contains(s.HasLabels, k) {
			found++
		}
	}
	// If all labels matched, need to exactly match the number of requested non-value-match labels.
	return found == len(s.HasLabels)
}
