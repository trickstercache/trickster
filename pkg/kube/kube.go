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
// client construction (in-cluster service account or kubeconfig path), the
// process-wide shared informer registry, and kubernetes-ecosystem logging
// integration. It is consumed by the autodiscovery kubernetes provider; the
// Kubernetes Gateway/Ingress controller initiative builds on this same
// layer so the process holds one client stack per configured connection.
package kube

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	kubeopts "github.com/trickstercache/trickster/v2/pkg/kube/options"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ErrNoConnectionOptions is returned when no kubernetes connection options
// are provided
var ErrNoConnectionOptions = errors.New("no kubernetes connection options provided")

// ErrNoRESTConfig is returned when a REST config is required but absent
var ErrNoRESTConfig = errors.New("no kubernetes rest config provided")

// Client wraps a Kubernetes clientset for Trickster subsystems
type Client struct {
	cs      kubernetes.Interface
	cfg     *rest.Config
	timeout time.Duration
	// id is this client's connection identity, scoping the shared informer registry so two
	// clients pointed at the same API server with the same credentials share watches
	id string
}

// clientsetSeq numbers clients built from a caller-supplied clientset,
// which carry no REST config to derive a connection identity from
var clientsetSeq atomic.Uint64

// New constructs a Client from the provided connection options: the pod's
// service account when InCluster, otherwise the referenced kubeconfig file
func New(o *kubeopts.Options) (*Client, error) {
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
	return NewFromRESTConfig(cfg, o)
}

// NewFromRESTConfig constructs a Client over an already-resolved REST config, applying the options'
// tuning knobs, for subsystems whose config comes from other than the in-cluster/kubeconfig pair
func NewFromRESTConfig(cfg *rest.Config, o *kubeopts.Options) (*Client, error) {
	if cfg == nil {
		return nil, ErrNoRESTConfig
	}
	if o == nil {
		o = kubeopts.New()
	}
	routeKlogOnce()
	// copy so that a caller-owned config is not mutated by the tuning below
	cfg = rest.CopyConfig(cfg)
	if o.QPS > 0 {
		cfg.QPS = o.QPS
	}
	if o.Burst > 0 {
		cfg.Burst = o.Burst
	}
	cfg.UserAgent = o.UserAgent
	if cfg.UserAgent == "" {
		cfg.UserAgent = UserAgent()
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{
		cs:      cs,
		cfg:     cfg,
		timeout: time.Duration(o.Timeout),
		id:      connectionID(cfg),
	}, nil
}

// NewFromClientset wraps an existing clientset; used by tests (client-go
// fakes) and by subsystems that construct their own client
func NewFromClientset(cs kubernetes.Interface) *Client {
	return &Client{
		cs:      cs,
		timeout: kubeopts.DefaultTimeout,
		id:      fmt.Sprintf("clientset-%d", clientsetSeq.Add(1)),
	}
}

// Clientset returns the underlying kubernetes clientset
func (c *Client) Clientset() kubernetes.Interface {
	return c.cs
}

// RESTConfig returns the REST config the client was built from, for subsystems needing their own
// typed clientset over the same connection; nil for a client wrapping a caller-supplied clientset
func (c *Client) RESTConfig() *rest.Config {
	return c.cfg
}

// Timeout returns the configured bound on a one-shot API call
func (c *Client) Timeout() time.Duration {
	return c.timeout
}

// Preflight verifies that the API server is reachable and answering, so an
// unreachable cluster surfaces at startup rather than as an empty pool
func (c *Client) Preflight(ctx context.Context) error {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	d := c.cs.Discovery()
	if rc := d.RESTClient(); rc != nil {
		return rc.Get().AbsPath("/version").Do(ctx).Error()
	}
	// clientsets without a REST client (the client-go fake) answer only
	// through the discovery interface, which takes no context
	_, err := d.ServerVersion()
	return err
}

// UserAgent returns the User-Agent Trickster presents to the API server
func UserAgent() string {
	v := appinfo.Version
	if v == "" {
		v = "unknown"
	}
	return fmt.Sprintf("%s/%s (%s/%s)", appinfo.AppName, v,
		runtime.GOOS, runtime.GOARCH)
}

func connectionID(cfg *rest.Config) string {
	// clients sharing an identity share a clientset and its credentials, so every authentication
	// input is part of it; a config with an incomparable callback shares with nothing
	if cfg.Proxy != nil || cfg.WrapTransport != nil || cfg.Dial != nil ||
		cfg.Transport != nil || cfg.RateLimiter != nil ||
		cfg.AuthConfigPersister != nil {
		return "unshareable-" + strconv.FormatUint(clientsetSeq.Add(1), 10)
	}
	var b strings.Builder
	// each value is terminated so that concatenation cannot be ambiguous
	// (an empty field next to a populated one must not read as one value)
	write := func(values ...string) {
		for _, v := range values {
			b.WriteString(v)
			b.WriteByte(0)
		}
	}
	write(cfg.Host, cfg.APIPath, cfg.UserAgent)
	write(cfg.Username, cfg.Password, cfg.BearerToken, cfg.BearerTokenFile)
	write(cfg.Impersonate.UserName, cfg.Impersonate.UID)
	write(cfg.Impersonate.Groups...)
	writeSortedMap(write, cfg.Impersonate.Extra)

	tls := cfg.TLSClientConfig
	write(strconv.FormatBool(tls.Insecure), tls.ServerName,
		tls.CertFile, tls.KeyFile, tls.CAFile)
	write(string(tls.CertData), string(tls.KeyData), string(tls.CAData))
	write(tls.NextProtos...)

	if ap := cfg.AuthProvider; ap != nil {
		write("auth-provider", ap.Name)
		for _, k := range slices.Sorted(maps.Keys(ap.Config)) {
			write(k, ap.Config[k])
		}
	}
	if ep := cfg.ExecProvider; ep != nil {
		write("exec-provider", ep.Command, ep.APIVersion,
			string(ep.InteractiveMode),
			strconv.FormatBool(ep.ProvideClusterInfo))
		write(ep.Args...)
		for _, e := range ep.Env {
			write(e.Name, e.Value)
		}
	}
	// the rate limit lives on the shared clientset, so two consumers that
	// disagree about it must not share one
	write(strconv.FormatFloat(float64(cfg.QPS), 'g', -1, 32),
		strconv.Itoa(cfg.Burst))
	return b.String()
}

func writeSortedMap(write func(...string), m map[string][]string) {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		write(k)
		write(m[k]...)
	}
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
