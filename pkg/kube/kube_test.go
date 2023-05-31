package kube

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func MockClient() *Client {
	pod := &v1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "V1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{"test-label": "test-pod"},
		},
		Status: v1.PodStatus{
			PodIP: "111.111.11.1",
		},
	}
	svc := &v1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "V1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-svc",
			Namespace: "default",
			Labels:    map[string]string{"test-label": "test-svc"},
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "222.222.22.2",
			Ports:     []v1.ServicePort{v1.ServicePort{Port: 80}, v1.ServicePort{Port: 443}},
		},
	}
	ing := &netv1.Ingress{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Ingress",
			APIVersion: "V1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ing",
			Namespace: "default",
			Labels:    map[string]string{"test-label": "test-ing"},
		},
		Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{
				{
					Host: "test.com",
					IngressRuleValue: netv1.IngressRuleValue{HTTP: &netv1.HTTPIngressRuleValue{
						Paths: []netv1.HTTPIngressPath{
							{Path: "/test/path"},
						},
					}},
				},
			},
		},
	}
	return &Client{
		cs: fake.NewSimpleClientset(pod, svc, ing),
	}
}

func TestKubeClient(t *testing.T) {
	// Create the client directly to use a mock cluster
	c := MockClient()
	pods, err := Pods(context.TODO(), c, &Selector{
		HasLabels: []string{"test-label"},
	})
	if err != nil {
		t.Error(err)
	} else {
		pod := pods[0]
		if pod.Meta.Name != "test-pod" {
			t.Errorf("expected name test-pod, got %s", pod.Meta.Name)
		}
		if v, ok := pod.Meta.Labels["test-label"]; !ok || v != "test-pod" {
			t.Errorf("expected ok, test-pod; got %t, %s", ok, v)
		}
	}
	svcs, err := Services(context.TODO(), c, &Selector{
		HasLabels: []string{"test-label"},
	})
	if err != nil {
		t.Error(err)
	} else {
		svc := svcs[0]
		if svc.Meta.Name != "test-svc" {
			t.Errorf("expected name test-pod, got %s", svc.Meta.Name)
		}
		if v, ok := svc.Meta.Labels["test-label"]; !ok || v != "test-svc" {
			t.Errorf("expected ok, test-pod; got %t, %s", ok, v)
		}
	}
	ings, err := Ingresses(context.TODO(), c, &Selector{
		HasLabels: []string{"test-label"},
	})
	if err != nil {
		t.Error(err)
	} else {
		ing := ings[0]
		if ing.Meta.Name != "test-ing" {
			t.Errorf("expected name test-ing, got %s", ing.Meta.Name)
		}
		if v, ok := ing.Meta.Labels["test-label"]; !ok || v != "test-ing" {
			t.Errorf("expected ok, test-ing; got %t, %s", ok, v)
		}
	}
}
