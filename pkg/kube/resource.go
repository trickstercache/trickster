package kube

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Metadata approximates the metadata attached to all kubernetes objects.
// It also includes an extra status string where available.
type Metadata struct {
	Name        string
	Namespace   string
	Labels      map[string]string
	Annotations map[string]string
	Status      string
}

type Pod struct {
	Obj     corev1.Pod
	Meta    Metadata
	Address string
}

func newPod(p corev1.Pod) *Pod {
	return &Pod{
		Obj: p,
		Meta: Metadata{
			Name:        p.Name,
			Namespace:   p.Namespace,
			Labels:      p.Labels,
			Annotations: p.Annotations,
			Status:      p.Status.Message,
		},
		Address: p.Status.PodIP,
	}
}

type Service struct {
	Obj     corev1.Service
	Meta    Metadata
	Address string
	Ports   []int32
}

func newService(s corev1.Service) *Service {
	out := &Service{
		Obj: s,
		Meta: Metadata{
			Name:        s.Name,
			Namespace:   s.Namespace,
			Labels:      s.Labels,
			Annotations: s.Annotations,
			Status:      s.Status.String(),
		},
		Address: s.Spec.ClusterIP,
		Ports:   make([]int32, len(s.Spec.Ports)),
	}
	for i, p := range s.Spec.Ports {
		out.Ports[i] = p.Port
	}
	return out
}

type PathBackend struct {
	Obj  *netv1.IngressServiceBackend
	Name string
}

type Path struct {
	Obj      netv1.HTTPIngressPath
	Path     string
	PathType string
	Backend  *PathBackend
}

type Rule struct {
	Obj   netv1.IngressRule
	Host  string
	Paths []*Path
}

type Ingress struct {
	Obj   netv1.Ingress
	Meta  Metadata
	Rules []*Rule
}

func newIngress(ing netv1.Ingress) *Ingress {
	out := &Ingress{
		Obj: ing,
		Meta: Metadata{
			Name:        ing.Name,
			Namespace:   ing.Namespace,
			Labels:      ing.Labels,
			Annotations: ing.Annotations,
			Status:      ing.Status.String(),
		},
		Rules: make([]*Rule, len(ing.Spec.Rules)),
	}
	for i, rule := range ing.Spec.Rules {
		out.Rules[i] = &Rule{
			Obj:   rule,
			Host:  rule.Host,
			Paths: make([]*Path, len(rule.HTTP.Paths)),
		}
		for j, path := range rule.HTTP.Paths {
			out.Rules[i].Paths[j] = &Path{
				Obj:      path,
				Path:     path.Path,
				PathType: "",
			}
			if path.Backend.Service != nil {
				pbs := path.Backend.Service
				out.Rules[i].Paths[j].Backend = &PathBackend{
					Obj:  pbs,
					Name: pbs.Name,
				}
			}
		}
	}
	return out
}

// Pods fetches every pod matching the provided Selector.
func Pods(ctx context.Context, c *Client, sel *Selector) ([]*Pod, error) {
	pods, err := c.cs.CoreV1().Pods(sel.Namespace).List(ctx, v1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]*Pod, 0, pods.Size())
	for _, p := range pods.Items {
		if sel == nil || sel.NameOK(p.Name) && sel.LabelsOK(p.Labels) {
			out = append(out, newPod(p))
		}
	}
	return out, nil
}

// Services fetches every service matching the provided Selector.
func Services(ctx context.Context, c *Client, sel *Selector) ([]*Service, error) {
	svcs, err := c.cs.CoreV1().Services(sel.Namespace).List(ctx, v1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]*Service, 0, svcs.Size())
	for _, s := range svcs.Items {
		if sel == nil || sel.NameOK(s.Name) && sel.LabelsOK(s.Labels) {
			out = append(out, newService(s))
		}
	}
	return out, nil
}

// Ingresses fetches every ingress matching the provided Selector.
func Ingresses(ctx context.Context, c *Client, sel *Selector) ([]*Ingress, error) {
	ings, err := c.cs.NetworkingV1().Ingresses(sel.Namespace).List(ctx, v1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]*Ingress, 0, ings.Size())
	for _, ing := range ings.Items {
		if sel == nil || sel.NameOK(ing.Name) && sel.LabelsOK(ing.Labels) {
			out = append(out, newIngress(ing))
		}
	}
	return out, nil
}
