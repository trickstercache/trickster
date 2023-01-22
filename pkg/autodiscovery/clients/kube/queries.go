package kube

import (
	"context"

	"golang.org/x/exp/slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ResourceKind string

const (
	PodResource     = ResourceKind("pod")
	ServiceResource = ResourceKind("service")
	IngressResource = ResourceKind("ingress")
)

type AccessKind string

const (
	IngressAccess    = AccessKind("ingress")
	ResourceIPAccess = AccessKind("resource_ip")
)

type ResourceMeta struct {
	Kind   ResourceKind
	Name   string
	Labels map[string]string
	Base   any
}

// Aggregate a list of all resources in a namespace.
func (c *Client) aggregateResources(inNamespace string, kinds ...ResourceKind) ([]ResourceMeta, error) {
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
