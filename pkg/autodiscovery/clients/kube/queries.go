package kube

import (
	"context"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/kube"
	"golang.org/x/exp/slices"

	v1 "k8s.io/api/core/v1"
	v1net "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	PodResource     = string("pod")
	ServiceResource = string("service")
	IngressResource = string("ingress")
)

const (
	IngressAccess    = string("ingress")
	ResourceIPAccess = string("resource_ip")
)

type ResourceMeta struct {
	Kind   string
	Name   string
	Labels map[string]string
	Base   any
}

type ResourceMetaList []ResourceMeta

func (rm ResourceMeta) Match(query *kube.Query) bool {
	matches := true
	if query.ResourceKinds != nil {
		matches = matches && slices.Contains(query.ResourceKinds, rm.Kind)
	}
	if query.ResourceName != "" {
		matches = matches && query.ResourceName == rm.Name
	}
	if query.HasLabel != nil {
		hasLabel := false
		for _, label := range query.HasLabel {
			_, ok := rm.Labels[label]
			hasLabel = hasLabel || ok
		}
		matches = matches && hasLabel
	}
	if query.HasLabelWithValue != nil {
		hasLabel := false
		for label, values := range query.HasLabelWithValue {
			v, ok := rm.Labels[label]
			hasLabel = hasLabel || (ok && slices.Contains(values, v))
		}
		matches = matches && hasLabel
	}
	return matches
}

func (rms ResourceMetaList) GetAccessFor(rm ResourceMeta, by string) (string, bool) {
	switch by {
	case ResourceIPAccess:
		return rm.getIP()
	case IngressAccess:
		return rm.getIngressExtPath(rms)
	}
	return "", false
}

// Get the ResourceMeta pointing to the first ingress with a rule directing traffic to
func (rm ResourceMeta) getIngressExtPath(fromRMS ResourceMetaList) (string, bool) {
	if rm.Kind != ServiceResource {
		return "", false
	}
	for _, RM := range fromRMS {
		switch ingress := RM.Base.(type) {
		case v1net.Ingress:
			for _, rule := range ingress.Spec.Rules {
				host := rule.Host
				for _, path := range rule.HTTP.Paths {
					route := path.Path
					sname := path.Backend.Service.Name
					if sname == rm.Name {
						return host + route, true
					}
				}
			}
		}
	}
	return "", false
}

func (rm *ResourceMeta) getIP() (string, bool) {
	switch rm := rm.Base.(type) {
	case v1.Pod:
		return rm.Status.PodIP, true
	case v1.Service:
		return rm.Spec.ClusterIP, true
	case v1net.Ingress:
		return rm.Status.LoadBalancer.Ingress[0].IP, true
	}
	return "", false
}

// Aggregate a list of all resources in a namespace.
func (c *Client) aggregateResources(inNamespace string, kinds ...string) (ResourceMetaList, error) {
	out := make([]ResourceMeta, 0)
	if slices.Contains(kinds, PodResource) {
		ps, err := c.core.Pods(inNamespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, p := range ps.Items {
			rm := ResourceMeta{
				Kind:   PodResource,
				Name:   p.Name,
				Labels: p.Labels,
				Base:   p,
			}
			out = append(out, rm)
		}
	}
	if slices.Contains(kinds, ServiceResource) {
		ss, err := c.core.Services(inNamespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, s := range ss.Items {
			rm := ResourceMeta{
				Kind:   ServiceResource,
				Name:   s.Name,
				Labels: s.Labels,
				Base:   s,
			}
			out = append(out, rm)
		}
	}
	if slices.Contains(kinds, IngressResource) {
		is, err := c.net.Ingresses(inNamespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, i := range is.Items {
			rm := ResourceMeta{
				Kind:   IngressResource,
				Name:   i.Name,
				Labels: i.Labels,
				Base:   i,
			}
			out = append(out, rm)
		}
	}
	return out, nil
}
