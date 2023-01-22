package kube

import (
	"context"

	"golang.org/x/exp/slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type resourceKind string

const (
	podResource     = resourceKind("pod")
	serviceResource = resourceKind("service")
	ingressResource = resourceKind("ingress")
)

type ResourceMeta struct {
	Kind   resourceKind
	Name   string
	Labels map[string]string
	Base   any
}

// Aggregate a list of all resources in a namespace.
func (c *Client) aggregateResources(inNamespace string, kinds ...resourceKind) ([]ResourceMeta, error) {
	out := make([]ResourceMeta, 0)
	if slices.Contains(kinds, podResource) {
		ps, err := c.core.Pods("").List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, p := range ps.Items {
			rm := ResourceMeta{
				Kind:   podResource,
				Name:   p.Name,
				Labels: p.Labels,
				Base:   p,
			}
			out = append(out, rm)
		}
	}
	if slices.Contains(kinds, serviceResource) {
		ss, err := c.core.Services("").List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, s := range ss.Items {
			rm := ResourceMeta{
				Kind:   serviceResource,
				Name:   s.Name,
				Labels: s.Labels,
				Base:   s,
			}
			out = append(out, rm)
		}
	}
	if slices.Contains(kinds, ingressResource) {
		is, err := c.net.Ingresses("").List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, i := range is.Items {
			rm := ResourceMeta{
				Kind:   ingressResource,
				Name:   i.Name,
				Labels: i.Labels,
				Base:   i,
			}
			out = append(out, rm)
		}
	}
	return out, nil
}
