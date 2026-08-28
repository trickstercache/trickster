/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package kube is Trickster's shared Kubernetes client layer. It owns
// client construction (in-cluster service account or kubeconfig path) and
// kubernetes-ecosystem logging integration, and is consumed by the
// autodiscovery kubernetes provider; the Kubernetes Gateway/Ingress
// controller initiative builds on this same layer so the process holds one
// client stack per configured connection.
package kube

import (
	"errors"
	"os"
	"strings"

	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ErrNoConnectionOptions is returned when no kubernetes connection options
// are provided
var ErrNoConnectionOptions = errors.New("no kubernetes connection options provided")

// Client wraps a Kubernetes clientset for Trickster subsystems
type Client struct {
	cs kubernetes.Interface
}

// New constructs a Client from the provided connection options: the pod's
// service account when InCluster, otherwise the referenced kubeconfig file
func New(o *do.KubernetesOptions) (*Client, error) {
	if o == nil {
		return nil, ErrNoConnectionOptions
	}
	routeKlogOnce()
	var cfg *rest.Config
	var err error
	if o.InCluster {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.BuildConfigFromFlags("", o.Kubeconfig)
	}
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{cs: cs}, nil
}

// NewFromClientset wraps an existing clientset; used by tests (client-go
// fakes) and by subsystems that construct their own client
func NewFromClientset(cs kubernetes.Interface) *Client {
	return &Client{cs: cs}
}

// Clientset returns the underlying kubernetes clientset
func (c *Client) Clientset() kubernetes.Interface {
	return c.cs
}

// inClusterNamespaceFile is a var for test override
var inClusterNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// DefaultNamespace returns the namespace to use when a query does not name
// one: the pod's own namespace when running in-cluster, else "default"
func DefaultNamespace() string {
	if b, err := os.ReadFile(inClusterNamespaceFile); err == nil {
		if ns := strings.TrimSpace(string(b)); ns != "" {
			return ns
		}
	}
	return metav1.NamespaceDefault
}
