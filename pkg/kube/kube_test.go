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

package kube

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	kubeopts "github.com/trickstercache/trickster/v2/pkg/kube/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestNew(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("expected error for nil options")
	}
	// a kubeconfig path that does not exist must error, not panic
	if _, err := New(&kubeopts.Options{
		Kubeconfig: "/nonexistent/kubeconfig",
	}); err == nil {
		t.Error("expected error for missing kubeconfig")
	}
}

func TestNewFromClientset(t *testing.T) {
	cs := fake.NewClientset()
	c := NewFromClientset(cs)
	if c.Clientset() != cs {
		t.Error("expected wrapped clientset")
	}
}

func TestDefaultNamespace(t *testing.T) {
	// outside a cluster the service account namespace file is absent
	if ns := DefaultNamespace(); ns != metav1.NamespaceDefault {
		t.Errorf("expected %q, got %q", metav1.NamespaceDefault, ns)
	}
}

func TestKlogSink(t *testing.T) {
	s := &klogSink{}
	s.Init(struct{ CallDepth int }{})
	if !s.Enabled(2) {
		t.Error("expected sink to be enabled")
	}
	// client-go logs request and response bodies at verbosity 8, which a
	// Secret watch must never reach
	if s.Enabled(maxKlogVerbosity + 1) {
		t.Error("expected body-dumping verbosity to be disabled")
	}
	// exercise the pair-building and level mapping without asserting log
	// output (the logger package owns formatting)
	s.Info(0, "informational", "key", "value", 42, "answer")
	s.Error(errors.New("boom"), "failed", "key", "value")
	s.Error(nil, "no error attached")

	named, ok := s.WithName("reflector").(*klogSink)
	if !ok || named.name != "reflector" {
		t.Fatalf("expected named sink, got %+v", named)
	}
	renamed, _ := named.WithName("watch").(*klogSink)
	if renamed.name != "reflector.watch" {
		t.Errorf("expected nested name, got %q", renamed.name)
	}
	withVals, ok := named.WithValues("a", 1).(*klogSink)
	if !ok {
		t.Fatal("expected sink from WithValues")
	}
	p := withVals.pairs([]any{"b", 2})
	for _, k := range []string{"scope", "logger", "a", "b"} {
		if _, exists := p[k]; !exists {
			t.Errorf("expected pair %q in %v", k, logging.Pairs(p))
		}
	}
}

const testKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- cluster: {server: "https://127.0.0.1:6443"}
  name: test
contexts:
- context: {cluster: test, user: test}
  name: test
current-context: test
users:
- name: test
  user: {token: not-a-real-token}
`

func TestNewFromKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	// client construction succeeds without contacting the cluster
	c, err := New(&kubeopts.Options{Kubeconfig: path})
	if err != nil {
		t.Fatalf("New with valid kubeconfig: %v", err)
	}
	if c.Clientset() == nil {
		t.Fatal("expected a clientset")
	}
	// in-cluster construction outside a cluster errors cleanly
	if _, err = New(&kubeopts.Options{InCluster: true}); err == nil {
		t.Error("expected in-cluster construction to fail outside a cluster")
	}
}

func TestDefaultNamespaceInCluster(t *testing.T) {
	prev := inClusterNamespaceFile
	defer func() { inClusterNamespaceFile = prev }()

	path := filepath.Join(t.TempDir(), "namespace")
	if err := os.WriteFile(path, []byte(" monitoring \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inClusterNamespaceFile = path
	if ns := DefaultNamespace(); ns != "monitoring" {
		t.Errorf("expected monitoring, got %q", ns)
	}
	// an empty namespace file falls back to default
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ns := DefaultNamespace(); ns != metav1.NamespaceDefault {
		t.Errorf("expected default, got %q", ns)
	}
}

func TestNewFromRESTConfigAppliesTuning(t *testing.T) {
	_, err := NewFromRESTConfig(nil, nil)
	require.ErrorIs(t, err, ErrNoRESTConfig)

	in := &rest.Config{Host: "https://127.0.0.1:6443"}
	c, err := NewFromRESTConfig(in, &kubeopts.Options{
		QPS: 33, Burst: 66, UserAgent: "custom/9",
		Timeout: timeconv.Duration(3 * time.Second),
	})
	require.NoError(t, err)
	require.Equal(t, float32(33), c.RESTConfig().QPS)
	require.Equal(t, 66, c.RESTConfig().Burst)
	require.Equal(t, "custom/9", c.RESTConfig().UserAgent)
	require.Equal(t, 3*time.Second, c.Timeout())
	require.Zero(t, in.QPS, "the caller's config must not be mutated")

	// nil options take the package defaults, and an unset user agent is
	// derived rather than left to client-go's binary-name guess
	d, err := NewFromRESTConfig(in, nil)
	require.NoError(t, err)
	require.Equal(t, kubeopts.DefaultQPS, d.RESTConfig().QPS)
	require.Equal(t, kubeopts.DefaultBurst, d.RESTConfig().Burst)
	require.Equal(t, UserAgent(), d.RESTConfig().UserAgent)
	require.NotNil(t, d.Clientset())
}

func TestRESTConfigAbsentForClientset(t *testing.T) {
	// A client over a caller-supplied clientset has no connection to hand out
	require.Nil(t, NewFromClientset(fake.NewClientset()).RESTConfig())
}

func TestUserAgent(t *testing.T) {
	require.Contains(t, UserAgent(), "trickster/")
	require.Contains(t, UserAgent(), "unknown",
		"an unstamped build still names itself")
}

func TestConnectionID(t *testing.T) {
	// Clients agreeing on cluster, credentials and identity share informer factories and so a
	// clientset, so anything changing who talks to what must change the identity
	base := &rest.Config{Host: "https://a", UserAgent: "t/1"}
	require.Equal(t, connectionID(base),
		connectionID(&rest.Config{Host: "https://a", UserAgent: "t/1"}))

	for name, alt := range map[string]*rest.Config{
		"host":         {Host: "https://b", UserAgent: "t/1"},
		"user agent":   {Host: "https://a", UserAgent: "t/2"},
		"api path":     {Host: "https://a", UserAgent: "t/1", APIPath: "/apis"},
		"username":     {Host: "https://a", UserAgent: "t/1", Username: "u"},
		"password":     {Host: "https://a", UserAgent: "t/1", Password: "p"},
		"bearer token": {Host: "https://a", UserAgent: "t/1", BearerToken: "tok"},
		"token file":   {Host: "https://a", UserAgent: "t/1", BearerTokenFile: "/f"},
		"qps":          {Host: "https://a", UserAgent: "t/1", QPS: 99},
		"burst":        {Host: "https://a", UserAgent: "t/1", Burst: 99},
		"ca file": {
			Host: "https://a", UserAgent: "t/1",
			TLSClientConfig: rest.TLSClientConfig{CAFile: "/ca"},
		},
		"server name": {
			Host: "https://a", UserAgent: "t/1",
			TLSClientConfig: rest.TLSClientConfig{ServerName: "s"},
		},
		"insecure": {
			Host: "https://a", UserAgent: "t/1",
			TLSClientConfig: rest.TLSClientConfig{Insecure: true},
		},
		"cert file": {
			Host: "https://a", UserAgent: "t/1",
			TLSClientConfig: rest.TLSClientConfig{CertFile: "/c"},
		},
		"key file": {
			Host: "https://a", UserAgent: "t/1",
			TLSClientConfig: rest.TLSClientConfig{KeyFile: "/k"},
		},
		"cert data": {
			Host: "https://a", UserAgent: "t/1",
			TLSClientConfig: rest.TLSClientConfig{CertData: []byte("c")},
		},
		"key data": {
			Host: "https://a", UserAgent: "t/1",
			TLSClientConfig: rest.TLSClientConfig{KeyData: []byte("k")},
		},
		"ca data": {
			Host: "https://a", UserAgent: "t/1",
			TLSClientConfig: rest.TLSClientConfig{CAData: []byte("ca")},
		},
		"impersonated user": {
			Host: "https://a", UserAgent: "t/1",
			Impersonate: rest.ImpersonationConfig{UserName: "other"},
		},
		"impersonated uid": {
			Host: "https://a", UserAgent: "t/1",
			Impersonate: rest.ImpersonationConfig{UID: "uid"},
		},
		"impersonated groups": {
			Host: "https://a", UserAgent: "t/1",
			Impersonate: rest.ImpersonationConfig{Groups: []string{"admins"}},
		},
		"impersonated extra": {
			Host: "https://a", UserAgent: "t/1",
			Impersonate: rest.ImpersonationConfig{
				Extra: map[string][]string{"scope": {"a"}},
			},
		},
		"auth provider": {
			Host: "https://a", UserAgent: "t/1",
			AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "oidc"},
		},
		"exec provider": {
			Host: "https://a", UserAgent: "t/1",
			ExecProvider: &clientcmdapi.ExecConfig{Command: "get-token"},
		},
	} {
		require.NotEqual(t, connectionID(base), connectionID(alt), name)
	}
}

func TestConnectionIDSeparatesInlineCredentials(t *testing.T) {
	// An inline bearer token is the normal result of loading a kubeconfig, so
	// two clients aimed at one cluster as different identities must not share
	a := connectionID(&rest.Config{Host: "https://a", BearerToken: "token-a"})
	b := connectionID(&rest.Config{Host: "https://a", BearerToken: "token-b"})
	require.NotEqual(t, a, b)

	// the same for client certificates presented inline
	ca := connectionID(&rest.Config{
		Host:     "https://a",
		CertData: []byte("cert-a"),
	})
	cb := connectionID(&rest.Config{
		Host:     "https://a",
		CertData: []byte("cert-b"),
	})
	require.NotEqual(t, ca, cb)
}

func TestConnectionIDIsUnambiguous(t *testing.T) {
	// Adjacent fields must not run together: moving a character across a
	// field boundary is a different identity, not the same concatenation
	require.NotEqual(t,
		connectionID(&rest.Config{Host: "https://a", Username: "ab"}),
		connectionID(&rest.Config{Host: "https://a", Username: "a", Password: "b"}))
	require.NotEqual(t,
		connectionID(&rest.Config{Host: "https://a", BearerToken: "t"}),
		connectionID(&rest.Config{Host: "https://a", BearerTokenFile: "t"}))
	require.NotEqual(t,
		connectionID(&rest.Config{
			Host:        "https://a",
			Impersonate: rest.ImpersonationConfig{Groups: []string{"a", "b"}},
		}),
		connectionID(&rest.Config{
			Host:        "https://a",
			Impersonate: rest.ImpersonationConfig{Groups: []string{"ab"}},
		}))
}

func TestConnectionIDRefusesToShareUncomparableConfigs(t *testing.T) {
	// Two configs differing only in a field the identity cannot compare must not
	// be assumed equal: sharing is an optimization, never a guess
	mk := func() *rest.Config {
		return &rest.Config{
			Host: "https://a", UserAgent: "t/1",
			Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
		}
	}
	require.NotEqual(t, connectionID(mk()), connectionID(mk()))

	wrapped := &rest.Config{
		Host:          "https://a",
		WrapTransport: func(rt http.RoundTripper) http.RoundTripper { return rt },
	}
	require.NotEqual(t, connectionID(wrapped), connectionID(wrapped))
}

func TestPreflightRESTClient(t *testing.T) {
	// Preflight over a real REST client is the production path: it must reach
	// /version and surface a server error rather than reporting health.
	var path string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"major":"1","minor":"33"}`)) //nolint:errcheck
		}))
	defer srv.Close()

	c, err := NewFromRESTConfig(&rest.Config{Host: srv.URL}, nil)
	require.NoError(t, err)
	require.NoError(t, c.Preflight(context.Background()))
	require.Equal(t, "/version", path)

	down := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
	defer down.Close()
	bad, err := NewFromRESTConfig(&rest.Config{Host: down.URL}, nil)
	require.NoError(t, err)
	require.Error(t, bad.Preflight(context.Background()))
}

func TestPreflightTimeout(t *testing.T) {
	// An unreachable API server must fail within the configured bound rather
	// than hanging startup on the default dial timeout
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
	defer srv.Close()
	c, err := NewFromRESTConfig(&rest.Config{Host: srv.URL},
		&kubeopts.Options{Timeout: timeconv.Duration(250 * time.Millisecond)})
	require.NoError(t, err)

	start := time.Now()
	require.Error(t, c.Preflight(context.Background()))
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestPreflightClientset(t *testing.T) {
	// A clientset without a REST client (the client-go fake) answers through
	// the discovery interface instead
	cs := fake.NewClientset()
	require.NoError(t, NewFromClientset(cs).Preflight(context.Background()))

	boom := fake.NewClientset()
	boom.PrependReactor("get", "version",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("api server unreachable")
		})
	err := NewFromClientset(boom).Preflight(context.Background())
	require.ErrorContains(t, err, "api server unreachable")
}
