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

package translate

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type fakeCache struct {
	secrets map[string]*corev1.Secret
}

func (f fakeCache) Service(string, string) *corev1.Service { return nil }
func (f fakeCache) Secret(namespace, name string) *corev1.Secret {
	return f.secrets[NamespacedName(namespace, name)]
}

func TestProblems(t *testing.T) {
	var none *Problems
	require.Nil(t, none.List())
	src := ir.Source{Kind: ir.KindIngress, Namespace: "shop", Name: "web"}
	plain := NewProblems(false)
	plain.Reject(src, "thing %d", 1)
	plain.Reject(src, "thing %d", 1)
	require.Len(t, plain.List(), 2)
	require.Equal(t, "thing 1", plain.List()[0].Detail)
	deduped := NewProblems(true)
	deduped.Reject(src, "thing %d", 1)
	deduped.Reject(src, "thing %d", 1)
	deduped.Reject(src, "thing %d", 2)
	require.Len(t, deduped.List(), 2)
}

func TestSourceAndByAge(t *testing.T) {
	now := metav1.NewTime(time.Unix(100, 0))
	later := metav1.NewTime(time.Unix(200, 0))
	a := &corev1.Service{
		Name: "a", Namespace: "z",
		Generation: 3, CreationTimestamp: later,
	}
	b := &corev1.Service{Name: "b", Namespace: "y", CreationTimestamp: now}
	c := &corev1.Service{Name: "c", Namespace: "y", CreationTimestamp: now}
	require.Equal(t, ir.Source{Kind: "Service", Namespace: "z", Name: "a", Generation: 3},
		Source("Service", a))
	in := []*corev1.Service{c, a, b}
	require.Equal(t, []*corev1.Service{b, c, a}, ByAge(in))
	require.Equal(t, []*corev1.Service{c, a, b}, in, "the input is not reordered")
}

func TestServicePort(t *testing.T) {
	svc := &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
		{Name: "http", Port: 80}, {Name: "", Port: 8080},
	}}}
	p, ok := ServicePort(svc, PortRef{Name: "http"})
	require.True(t, ok)
	require.EqualValues(t, 80, p.Port)
	p, ok = ServicePort(svc, PortRef{Number: 8080})
	require.True(t, ok)
	require.Empty(t, p.Name)
	_, ok = ServicePort(svc, PortRef{Name: "grpc"})
	require.False(t, ok)
	_, ok = ServicePort(svc, PortRef{Number: 443})
	require.False(t, ok)
	_, ok = ServicePort(svc, PortRef{})
	require.False(t, ok, "an empty reference names no port")
	require.Equal(t, `"http"`, PortRef{Name: "http"}.String())
	require.Equal(t, "8080", PortRef{Number: 8080}.String())
}

func TestCertRefs(t *testing.T) {
	key, crt := tlstest.NamedKeyAndCert("tls")
	cache := fakeCache{secrets: map[string]*corev1.Secret{
		"shop/tls": {Data: map[string][]byte{
			corev1.TLSCertKey: crt, corev1.TLSPrivateKeyKey: key,
		}},
		"shop/empty": {},
		"shop/bad": {Data: map[string][]byte{
			corev1.TLSCertKey: []byte("garbage"), corev1.TLSPrivateKeyKey: key,
		}},
	}}
	model := &ir.IR{}
	src := ir.Source{Kind: ir.KindIngress, Namespace: "shop", Name: "web"}
	var refs CertRefs
	k, err := refs.Collect(cache, model, src, "shop", "tls")
	require.NoError(t, err)
	require.Equal(t, "shop/tls", k)
	k, err = refs.Collect(cache, model, src, "shop", "tls")
	require.NoError(t, err)
	require.Equal(t, "shop/tls", k)
	require.Len(t, model.Certs, 1, "a Secret is recorded once")
	require.Equal(t, ir.CertRef{Name: "shop/tls", Namespace: "shop", SecretName: "tls", Source: src},
		model.Certs[0])
	k, err = refs.Collect(cache, model, src, "shop", "missing")
	require.ErrorIs(t, err, ErrSecretNotFound)
	require.Equal(t, "shop/missing", k)
	require.Len(t, model.Certs, 1)
	// unusable material is reported, and the reference is still recorded so
	// a certificate already serving under it is not withdrawn
	_, err = refs.Collect(cache, model, src, "shop", "empty")
	require.ErrorIs(t, err, ir.ErrCertEmpty)
	_, err = refs.Collect(cache, model, src, "shop", "bad")
	require.ErrorIs(t, err, ir.ErrCertInvalid)
	_, err = refs.Collect(cache, model, src, "shop", "bad")
	require.ErrorIs(t, err, ir.ErrCertInvalid, "the verdict is shared across references")
	require.Len(t, model.Certs, 3)
	l443 := ir.Listener{Port: 443, Protocol: ir.ProtocolHTTPS}
	require.Equal(t, []string{"tls"}, refs.Identity(l443, "shop/tls").Names)
	require.NotEmpty(t, refs.Identity(l443, "shop/tls").Digest)
	require.Empty(t, refs.Identity(l443, "shop/bad").Names, "unusable material answers for nothing")
	require.Empty(t, refs.Identity(l443, "shop/missing").Digest)

	// unusable material withdraws nothing, so what serves under the Secret is whatever the
	// listener's own store still holds, which Serving answers per listener
	retained := ir.CertIdentity{Names: []string{"kept"}, Digest: "d"}
	refs = CertRefs{Serving: func(l ir.Listener, k string) (ir.CertIdentity, bool) {
		if l.Port != 443 || k != "shop/bad" {
			return ir.CertIdentity{}, false
		}
		return retained, true
	}}
	_, err = refs.Collect(cache, model, src, "shop", "bad")
	require.ErrorIs(t, err, ir.ErrCertInvalid)
	_, err = refs.Collect(cache, model, src, "shop", "tls")
	require.NoError(t, err)
	require.Equal(t, retained, refs.Identity(l443, "shop/bad"))
	require.Empty(t, refs.Identity(ir.Listener{Port: 8443, Protocol: ir.ProtocolHTTPS}, "shop/bad").Names,
		"a store holding nothing under the Secret serves nothing")
	require.Equal(t, []string{"tls"}, refs.Identity(l443, "shop/tls").Names,
		"usable material answers for itself everywhere")
}

func TestHostname(t *testing.T) {
	got, err := Hostname("", HostnameAllowEmpty)
	require.NoError(t, err)
	require.Empty(t, got)
	_, err = Hostname("", HostnameRequired)
	require.ErrorIs(t, err, ErrEmptyHostname)
	got, err = Hostname(" Shop.Example.COM ", HostnameRequired)
	require.NoError(t, err)
	require.Equal(t, "shop.example.com", got)
	got, err = Hostname("*.Example.com", HostnameRequired)
	require.NoError(t, err)
	require.Equal(t, "*.example.com", got)
	for _, bad := range []string{"**.example.com", "*", "a.*.example.com", "*.*.example.com"} {
		_, err = Hostname(bad, HostnameAllowEmpty)
		require.ErrorIs(t, err, ErrBadWildcard, bad)
	}
	_, err = Hostname("*.example.com", HostnamePrecise)
	require.ErrorIs(t, err, ErrWildcardHostname)
	_, err = Hostname("a b.example.com", HostnameRequired)
	require.Error(t, err)
}

func TestUniqueAndCheckName(t *testing.T) {
	require.Nil(t, Unique(nil))
	require.Equal(t, []string{"a", "b"}, Unique([]string{"b", "a", "b"}))
	require.NoError(t, CheckName(nil, "cache", "anything"))
	require.NoError(t, CheckName(sets.New([]string{"x"}), "cache", ""))
	require.NoError(t, CheckName(sets.New([]string{"x"}), "cache", "x"))
	require.EqualError(t, CheckName(sets.New([]string{"x"}), "cache", "y"),
		`no cache named "y" is configured`)
}

func TestReferenceDefaults(t *testing.T) {
	g := gwapiv1.Group("gateway.networking.k8s.io")
	k := gwapiv1.Kind("Gateway")
	ns := gwapiv1.Namespace("infra")
	empty := gwapiv1.Kind("")
	require.Empty(t, GroupOf(nil))
	require.Equal(t, string(g), GroupOf(&g))
	require.Equal(t, "Service", KindOf(nil, "Service"))
	require.Equal(t, "Service", KindOf(&empty, "Service"))
	require.Equal(t, "Gateway", KindOf(&k, "Service"))
	require.Equal(t, "shop", NamespaceOf(nil, "shop"))
	require.Equal(t, "infra", NamespaceOf(&ns, "shop"))
	require.Equal(t, "shop/web", NamespacedName("shop", "web"))
}

func TestValueParsers(t *testing.T) {
	v, err := RoutingMode("endpoint")
	require.NoError(t, err)
	require.Equal(t, "endpoint", v)
	_, err = RoutingMode("dns")
	require.ErrorContains(t, err, "must be")
	v, err = HealthMode("probe")
	require.NoError(t, err)
	require.Equal(t, "probe", v)
	_, err = HealthMode("x")
	require.Error(t, err)
	v, err = Handler("proxycache")
	require.NoError(t, err)
	require.Equal(t, "proxycache", v)
	_, err = Handler("x")
	require.Error(t, err)
	v, err = CORSMode("merge")
	require.NoError(t, err)
	require.Equal(t, "merge", v)
	_, err = CORSMode("x")
	require.Error(t, err)
	v, err = CollapsedForwarding("progressive")
	require.NoError(t, err)
	require.Equal(t, "progressive", v)
	_, err = CollapsedForwarding("x")
	require.Error(t, err)
}

func TestHeaders(t *testing.T) {
	h, err := Headers("X-A: 1\n\n +X-B: 2 \n-X-C:")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"X-A": "1", "+X-B": "2", "-X-C": ""}, h)
	for in, want := range map[string]string{
		"nocolon":    "each line must be",
		"X A: 1":     "not a valid header name",
		"-: 1":       "not a valid header name",
		": 1":        "not a valid header name",
		"+-X: 1":     "not a valid header name",
		"X-A: a\x00": "is not a valid value",
	} {
		_, err := Headers(in)
		require.ErrorContains(t, err, want, in)
	}
}

func TestServicePortSelectsByTransport(t *testing.T) {
	// one port number over both transports, in either declaration order, resolves to the port
	// of the transport asked for; a port naming no protocol is TCP
	tcp := corev1.ServicePort{Name: "dns-tcp", Port: 53}
	udp := corev1.ServicePort{Name: "dns-udp", Port: 53, Protocol: corev1.ProtocolUDP}
	for _, ports := range [][]corev1.ServicePort{{tcp, udp}, {udp, tcp}} {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{Ports: ports}}
		got, ok := ServicePort(svc, PortRef{Number: 53, Protocol: corev1.ProtocolTCP})
		require.True(t, ok)
		require.Equal(t, "dns-tcp", got.Name)
		got, ok = ServicePort(svc, PortRef{Number: 53, Protocol: corev1.ProtocolUDP})
		require.True(t, ok)
		require.Equal(t, "dns-udp", got.Name)
		got, ok = ServicePort(svc, PortRef{Number: 53})
		require.True(t, ok)
		require.Equal(t, ports[0].Name, got.Name, "no transport accepts the first")
		got, ok = ServicePort(svc, PortRef{Name: "dns-udp", Protocol: corev1.ProtocolUDP})
		require.True(t, ok)
		require.Equal(t, "dns-udp", got.Name)
		_, ok = ServicePort(svc, PortRef{Name: "dns-udp", Protocol: corev1.ProtocolTCP})
		require.False(t, ok, "a named port of the other transport is not the port asked for")
	}
	only := &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{tcp}}}
	_, ok := ServicePort(only, PortRef{Number: 53, Protocol: corev1.ProtocolUDP})
	require.False(t, ok)
}

func TestLoadBalancing(t *testing.T) {
	for _, v := range []string{"rr", "p2c", "lc", "lt", "hrw"} {
		got, err := LoadBalancing(v)
		require.NoError(t, err)
		require.Equal(t, v, got)
	}
	// only a mechanism that commits to one endpoint can balance a Service's endpoints
	for _, v := range []string{"", "fr", "tsm", "ur", "round_robin", "RR"} {
		_, err := LoadBalancing(v)
		require.ErrorContains(t, err, "must be one of", v)
	}
	for _, v := range []string{"client_ip", "sni", "host", "header:X-Tenant", "cookie:session", "query:tenant"} {
		got, err := LoadBalancingKey(" " + v + " ")
		require.NoError(t, err)
		require.Equal(t, v, got)
	}
	for _, v := range []string{"port", "header:", "cookie:a b"} {
		_, err := LoadBalancingKey(v)
		require.Error(t, err, v)
	}
}
