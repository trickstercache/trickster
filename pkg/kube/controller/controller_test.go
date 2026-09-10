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

package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/events"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/class"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/compile"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/kube/gatewayapi"
	"github.com/trickstercache/trickster/v2/pkg/kube/leader"
	"github.com/trickstercache/trickster/v2/pkg/kube/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	networkingv1 "k8s.io/client-go/kubernetes/typed/networking/v1"
	ktesting "k8s.io/client-go/testing"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

// publisher records the overlays a controller produces
type publisher struct {
	mtx      sync.Mutex
	overlays []*config.Overlay
	err      error
	// onPublish runs inside Publish, so a test can act between the store and
	// the certificate push that follows it
	onPublish func()
}

func (p *publisher) Publish(o *config.Overlay) (bool, error) {
	p.mtx.Lock()
	p.overlays = append(p.overlays, o)
	err := p.err
	hook := p.onPublish
	p.mtx.Unlock()
	if hook != nil {
		hook()
	}
	return err == nil, err
}

func (p *publisher) count() int {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return len(p.overlays)
}

func (p *publisher) last() *config.Overlay {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if len(p.overlays) == 0 {
		return nil
	}
	return p.overlays[len(p.overlays)-1]
}

// certSink records what reached the runtime certificate stores
type certSink struct {
	mtx       sync.Mutex
	listeners []string
	set       map[string]string
	removed   []string
	err       error
	// rejected counts the certificates the store refused, so a test can
	// wait for a rotation attempt that changes nothing observable
	rejected atomic.Int32
}

func newCertSink(listeners ...string) *certSink {
	return &certSink{listeners: listeners, set: make(map[string]string)}
}

func (c *certSink) TLSListeners() []string {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	return append([]string(nil), c.listeners...)
}

// MemoryCerts reports what the named listener is serving. A listener the
// sink no longer has is gone, and so is everything that was on it.
func (c *certSink) MemoryCerts(listener string) ([]string, bool) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if !slices.Contains(c.listeners, listener) {
		return nil, false
	}
	var out []string
	for k := range c.set {
		name, source, _ := strings.Cut(k, "|")
		if name == listener {
			out = append(out, source)
		}
	}
	slices.Sort(out)
	return out, true
}

func (c *certSink) destroyListener(listener string) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.listeners = slices.DeleteFunc(c.listeners, func(n string) bool {
		return n == listener
	})
	for k := range c.set {
		if name, _, _ := strings.Cut(k, "|"); name == listener {
			delete(c.set, k)
		}
	}
}

func (c *certSink) createListener(listener string) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if !slices.Contains(c.listeners, listener) {
		c.listeners = append(c.listeners, listener)
	}
}

// SetMemoryCert records the certificate by the name it was generated under
func (c *certSink) SetMemoryCert(listener, source string, crt, _ []byte) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.err != nil {
		c.rejected.Add(1)
		return c.err
	}
	c.set[listener+"|"+source] = tlstest.CommonName(crt)
	return nil
}

func (c *certSink) RemoveMemoryCert(listener, source string) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	delete(c.set, listener+"|"+source)
	c.removed = append(c.removed, listener+"|"+source)
	return c.err
}

func (c *certSink) snapshot() map[string]string {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	out := make(map[string]string, len(c.set))
	maps.Copy(out, c.set)
	return out
}

func options(t *testing.T) *kubecfg.Options {
	// options is a validated section with a short debounce
	t.Helper()
	o := kubecfg.New()
	o.Defaults.RoutingMode = kubecfg.RoutingModeService
	o.DebounceWindow = 1
	// a sole writer: status and Events are written through the fakes on every pass, and the
	// election is exercised by the tests that turn it on
	o.LeaderElection.Enabled = new(false)
	require.NoError(t, o.Validate())
	return o
}

// tlsListener is the listener the fixtures' Ingresses are served on: with no
// ingress.listener_names configured, the default frontend
const tlsListener = listenerconfig.DefaultFrontendName

func ingressClass() *netv1.IngressClass {
	return &netv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "trickster",
			Annotations: map[string]string{class.DefaultClassAnnotation: "true"},
		},
		Spec: netv1.IngressClassSpec{
			Controller: kubecfg.DefaultGatewayClassControllerName},
	}
}

func otherNamespace() []runtime.Object {
	prefix := netv1.PathTypePrefix
	className := "trickster"
	return []runtime.Object{
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "warehouse", Name: "web-svc"},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{Port: 8080}}},
		},
		warehouseSecret("cert-b"),
		&netv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "warehouse", Name: "stock"},
			Spec: netv1.IngressSpec{
				IngressClassName: &className,
				TLS: []netv1.IngressTLS{
					{SecretName: "warehouse-tls"}},
				Rules: []netv1.IngressRule{{
					Host: "stock.example.com",
					IngressRuleValue: netv1.IngressRuleValue{
						HTTP: &netv1.HTTPIngressRuleValue{
							Paths: []netv1.HTTPIngressPath{{
								Path: "/api", PathType: &prefix,
								Backend: netv1.IngressBackend{
									Service: &netv1.IngressServiceBackend{
										Name: "web-svc",
										Port: netv1.ServiceBackendPort{
											Number: 8080},
									},
								},
							}},
						},
					},
				}},
			},
		},
	}
}

func warehouseSecret(label string) *corev1.Secret {
	key, crt := tlstest.NamedKeyAndCert(label)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "warehouse", Name: "warehouse-tls"},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       crt,
			corev1.TLSPrivateKeyKey: key,
		},
	}
}

func controllerConfig(t *testing.T, sink CertSink, state *CertState,
	namespace string,
) Config {
	t.Helper()
	o := options(t)
	if namespace != "" {
		o.WatchNamespaces = []string{namespace}
	}
	return Config{Options: o, Certs: sink, CertState: state}
}

func newController(t *testing.T, cfg Config, objects ...runtime.Object,
) (*Controller, *kubefake.Clientset) {
	t.Helper()
	if cfg.Publisher == nil {
		cfg.Publisher = &publisher{}
	}
	cs := kubefake.NewClientset(objects...)
	cfg.Client = kube.NewFromClientset(cs)
	cfg.GatewayClient = gwfake.NewSimpleClientset()
	c, err := New(cfg)
	require.NoError(t, err)
	return c, cs
}

func service() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-svc"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
}

func webIngress(tls ...netv1.IngressTLS) *netv1.Ingress {
	className := "trickster"
	prefix := netv1.PathTypePrefix
	return &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
		Spec: netv1.IngressSpec{
			IngressClassName: &className,
			TLS:              tls,
			Rules: []netv1.IngressRule{{
				Host: "shop.example.com",
				IngressRuleValue: netv1.IngressRuleValue{
					HTTP: &netv1.HTTPIngressRuleValue{
						Paths: []netv1.HTTPIngressPath{{
							Path: "/api", PathType: &prefix,
							Backend: netv1.IngressBackend{
								Service: &netv1.IngressServiceBackend{
									Name: "web-svc",
									Port: netv1.ServiceBackendPort{Number: 8080},
								},
							},
						}},
					},
				},
			}},
		},
	}
}

func tlsSecret(label string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "shop-tls"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{},
	}
	if label != "" {
		s.Data[corev1.TLSPrivateKeyKey], s.Data[corev1.TLSCertKey] = tlstest.NamedKeyAndCert(label)
	}
	return s
}

func garbageSecret() *corev1.Secret {
	s := tlsSecret("")
	s.Data[corev1.TLSCertKey] = []byte("garbage")
	s.Data[corev1.TLSPrivateKeyKey] = []byte("garbage")
	return s
}

func start(t *testing.T, p *publisher, certs CertSink,
	objects ...runtime.Object,
) (*Controller, *kubefake.Clientset) {
	// start builds and starts a controller over the supplied objects
	t.Helper()
	cs := kubefake.NewClientset(objects...)
	c, err := New(Config{
		Options:       options(t),
		Publisher:     p,
		Certs:         certs,
		Client:        kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	return c, cs
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond, msg)
}

func TestNewRejectsIncompleteConfig(t *testing.T) {
	_, err := New(Config{})
	require.ErrorIs(t, err, ErrNoOptions)
	_, err = New(Config{Options: options(t)})
	require.ErrorIs(t, err, ErrNoPublisher)
}

func TestControllerPublishesOnFirstSync(t *testing.T) {
	// The first sync publishes what the cluster already holds, without waiting
	// for a change to it
	p := &publisher{}
	start(t, p, nil, ingressClass(), service(), webIngress())
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")
	require.Contains(t, string(p.last().Data), "kgw--ingress.shop.web_r0")
	require.Equal(t, compile.Prefix, p.last().Prefix)
}

func TestControllerSkipsUnchangedOverlays(t *testing.T) {
	// A watch event that changes nothing observable must not reload the daemon
	p := &publisher{}
	_, cs := start(t, p, nil, ingressClass(), service(), webIngress())
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")

	// a label is not part of the translation, so the rebuilt configuration
	// is byte-identical and there is nothing to apply
	changed := webIngress()
	changed.Labels = map[string]string{"team": "shop"}
	_, err := cs.NetworkingV1().Ingresses("shop").
		Update(t.Context(), changed, metav1.UpdateOptions{})
	require.NoError(t, err)

	// a second Ingress does change it, and proves the watch is still live
	second := webIngress()
	second.Name = "other"
	second.Spec.Rules[0].Host = "other.example.com"
	_, err = cs.NetworkingV1().Ingresses("shop").
		Create(t.Context(), second, metav1.CreateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool { return p.count() == 2 }, "a real change was not applied")
	require.Contains(t, string(p.last().Data), "kgw--ingress.shop.other_r0")
}

func TestControllerRetriesAfterFailedApply(t *testing.T) {
	// A failed apply must not be remembered as applied, or the next pass over
	// the same objects would decide there was nothing to do
	p := &publisher{err: errors.New("apply failed")}
	c, _ := start(t, p, nil, ingressClass(), service(), webIngress())
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")
	c.mtx.Lock()
	version := c.version
	c.mtx.Unlock()
	require.Empty(t, version, "a failed apply must leave nothing marked applied")

	p.mtx.Lock()
	p.err = nil
	p.mtx.Unlock()
	c.reconcile(context.Background())
	require.Equal(t, 2, p.count(), "the same configuration must be retried")
}

func TestControllerPushesCertificates(t *testing.T) {
	// A TLS Ingress reaches the listener only through the runtime store: its
	// certificate is in a Secret and cannot travel in configuration
	listener := tlsListener
	sink := newCertSink(listener)
	p := &publisher{}
	c, cs := start(t, p, sink, ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")
	eventually(t, func() bool {
		return sink.snapshot()[listener+"|shop/shop-tls"] == "cert-v1"
	}, "the certificate never reached the listener")

	// rotating a Secret changes no configuration at all, so the push is the
	// only thing that can carry it
	_, err := cs.CoreV1().Secrets("shop").
		Update(t.Context(), tlsSecret("cert-v2"), metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool {
		return sink.snapshot()[listener+"|shop/shop-tls"] == "cert-v2"
	}, "the rotated certificate never reached the listener")
	require.Equal(t, 1, p.count(), "a rotation must not reload the daemon")

	// dropping the TLS block withdraws it again
	_, err = cs.NetworkingV1().Ingresses("shop").
		Update(t.Context(), webIngress(), metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool { return len(sink.snapshot()) == 0 },
		"the certificate was not withdrawn")
	c.Stop()
}

func TestCertStateServesPerListener(t *testing.T) {
	// what serves under a Secret is what each listener's own store holds: two stores may differ,
	// one holding none serves nothing, a withdrawal ends it, and unusable material is nothing itself
	state := NewCertState()
	one := ir.CertIdentity{Names: []string{"a.example.com"}, Digest: "d1"}
	two := ir.CertIdentity{Names: []string{"b.example.com"}, Digest: "d2"}
	state.record(certKey{listener: "kgw--listener-https-443", source: "shop/shop-tls"}, "d1", one)
	state.record(certKey{listener: "kgw--listener-https-8443", source: "shop/shop-tls"}, "d2", two)
	got, ok := state.serving("kgw--listener-https-443", "shop/shop-tls")
	require.True(t, ok)
	require.Equal(t, one, got)
	got, ok = state.serving("kgw--listener-https-8443", "shop/shop-tls")
	require.True(t, ok)
	require.Equal(t, two, got)
	_, ok = state.serving("kgw--listener-https-9443", "shop/shop-tls")
	require.False(t, ok, "a store holding nothing under the Secret serves nothing")
	state.forget(certKey{listener: "kgw--listener-https-443", source: "shop/shop-tls"})
	_, ok = state.serving("kgw--listener-https-443", "shop/shop-tls")
	require.False(t, ok, "a withdrawn certificate serves nothing")
	v := newCertVerdicts()
	v.begin()
	bad := garbageSecret()
	bad.ResourceVersion = "1"
	id, err := v.judge(bad)
	require.ErrorIs(t, err, ir.ErrCertInvalid)
	require.Empty(t, id.Names, "unusable material answers for nothing of itself")
}

func TestReplacementJudgesByWhatServes(t *testing.T) {
	// a replacement controller inherits the inventory, so an unusable Secret is judged by what each
	// port's store holds under it: a colliding Gateway is refused there and admitted elsewhere
	const listener = "kgw--listener-https-443"
	const alt = "kgw--listener-https-8443"
	state := NewCertState()
	sink := newCertSink(listener, alt)
	gwcs := gwfake.NewSimpleClientset(gatewayClass())
	gw := httpsGateway()
	gw.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, gwcs.Tracker().Create(gatewayGVR, gw, gw.Namespace))
	cs := kubefake.NewClientset(service(), tlsSecret("shop.example.com"))
	generation := func() *Controller {
		c, err := New(Config{
			Options: options(t), Publisher: &publisher{}, Certs: sink, CertState: state,
			Client: kube.NewFromClientset(cs), GatewayClient: gwcs,
		})
		require.NoError(t, err)
		t.Cleanup(c.Stop)
		require.NoError(t, c.Start(t.Context()))
		return c
	}
	first := generation()
	eventually(t, func() bool {
		return sink.snapshot()[listener+"|shop/shop-tls"] == "shop.example.com"
	}, "the certificate never reached the listener")
	_, err := cs.CoreV1().Secrets("shop").Update(t.Context(), garbageSecret(), metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool { return first.verdicts.verdictError("shop/shop-tls") != nil },
		"the rotation was never judged")
	first.Stop()

	other := httpsGateway()
	other.Name = "other"
	other.CreationTimestamp = metav1.NewTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	other.Spec.Listeners[0].TLS.CertificateRefs[0].Name = "other-tls"
	require.NoError(t, gwcs.Tracker().Create(gatewayGVR, other, other.Namespace))
	// a different certificate carrying the same name, since one certificate held twice is one
	otherTLS := tlsSecret("")
	otherTLS.Name = "other-tls"
	otherKey, otherCrt, err := tlstest.GetTestKeyAndCertWithNames("shop.example.com")
	require.NoError(t, err)
	otherTLS.Data[corev1.TLSPrivateKeyKey], otherTLS.Data[corev1.TLSCertKey] = otherKey, otherCrt
	_, err = cs.CoreV1().Secrets("shop").Create(t.Context(), otherTLS, metav1.CreateOptions{})
	require.NoError(t, err)
	generation()
	conflicted := func(name string) bool {
		got, err := gwcs.GatewayV1().Gateways("shop").Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		c := listenerCondition(got, "https", string(gwapiv1.ListenerConditionConflicted))
		return c != nil && c.Status == metav1.ConditionTrue
	}
	eventually(t, func() bool { return conflicted("other") },
		"the newer Gateway's colliding certificate was not refused")
	_, ok := sink.snapshot()[listener+"|shop/other-tls"]
	require.False(t, ok, "the refused listener's certificate must not reach the store")
	require.Equal(t, "shop.example.com", sink.snapshot()[listener+"|shop/shop-tls"],
		"the last-good certificate serves on")

	// the unusable Secret newly referenced on another port claims nothing there, so a newer
	// Gateway's different certificate for the name is admitted and installed on that port
	gw.Spec.Listeners = append(gw.Spec.Listeners, gwapiv1.Listener{
		Name: "alt", Port: 8443, Protocol: gwapiv1.HTTPSProtocolType,
		TLS: &gwapiv1.ListenerTLSConfig{CertificateRefs: []gwapiv1.SecretObjectReference{
			{Name: "shop-tls"}}},
	})
	require.NoError(t, gwcs.Tracker().Update(gatewayGVR, gw, gw.Namespace))
	third := httpsGateway()
	third.Name = "third"
	third.CreationTimestamp = metav1.NewTime(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	third.Spec.Listeners[0].Port = 8443
	third.Spec.Listeners[0].TLS.CertificateRefs[0].Name = "third-tls"
	require.NoError(t, gwcs.Tracker().Create(gatewayGVR, third, third.Namespace))
	thirdTLS := tlsSecret("")
	thirdTLS.Name = "third-tls"
	thirdKey, thirdCrt, err := tlstest.GetTestKeyAndCertWithNames("shop.example.com")
	require.NoError(t, err)
	thirdTLS.Data[corev1.TLSPrivateKeyKey], thirdTLS.Data[corev1.TLSCertKey] = thirdKey, thirdCrt
	_, err = cs.CoreV1().Secrets("shop").Create(t.Context(), thirdTLS, metav1.CreateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool {
		return sink.snapshot()[alt+"|shop/third-tls"] == "shop.example.com" && !conflicted("third")
	}, "the certificate on the port holding nothing under the Secret was not admitted")
	_, ok = sink.snapshot()[alt+"|shop/shop-tls"]
	require.False(t, ok, "unusable material is not installed on the new port")
	require.True(t, conflicted("other"), "the port holding the certificate still refuses")

	// a usable rotation away from the name admits the newer Gateway
	_, err = cs.CoreV1().Secrets("shop").Update(t.Context(), tlsSecret("renewed.example.com"),
		metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool {
		return !conflicted("other") && sink.snapshot()[listener+"|shop/other-tls"] == "shop.example.com"
	}, "the rotation did not admit the newer Gateway")

	// the Gateway gone, its certificate is withdrawn and claims nothing more
	require.NoError(t, gwcs.Tracker().Delete(gatewayGVR, "shop", "gw"))
	eventually(t, func() bool {
		_, ok := sink.snapshot()[listener+"|shop/shop-tls"]
		_, serving := state.serving(listener, "shop/shop-tls")
		_, servingAlt := state.serving(alt, "shop/shop-tls")
		return !ok && !serving && !servingAlt
	}, "the withdrawn certificate is still in the inventory")
}

func TestInstalledMaterialIsTheJudgedMaterial(t *testing.T) {
	// what is pushed is the Secret a translator judged this pass, whose identity the listeners were
	// compared by; one not judged this pass, or changed since, waits for its event's reconcile
	c := &Controller{verdicts: newCertVerdicts()}
	ref := ir.CertRef{Name: "shop/shop-tls", Namespace: "shop", SecretName: "shop-tls"}
	c.verdicts.begin()
	crt, key, _, problem := c.secretPEM(ref)
	require.Nil(t, crt)
	require.Nil(t, key)
	require.Nil(t, problem, "nothing judged, nothing installed")
	v1 := tlsSecret("cert-v1")
	v1.ResourceVersion = "1"
	_, err := c.verdicts.judge(v1)
	require.NoError(t, err)
	crt, key, identity, problem := c.secretPEM(ref)
	require.Nil(t, problem)
	require.Equal(t, v1.Data[corev1.TLSCertKey], crt)
	require.Equal(t, v1.Data[corev1.TLSPrivateKeyKey], key)
	require.Equal(t, []string{"cert-v1"}, identity.Names, "the identity travels with the material")
	c.verdicts.sweep()
	c.verdicts.begin()
	crt, _, _, _ = c.secretPEM(ref)
	require.Nil(t, crt, "last pass's judgment does not install this pass")
	v2 := tlsSecret("cert-v2")
	v2.ResourceVersion = "2"
	_, err = c.verdicts.judge(v2)
	require.NoError(t, err)
	crt, _, _, problem = c.secretPEM(ref)
	require.Nil(t, problem)
	require.Equal(t, v2.Data[corev1.TLSCertKey], crt, "the pass installs what it judged")
	bad := garbageSecret()
	bad.ResourceVersion = "3"
	_, err = c.verdicts.judge(bad)
	require.Error(t, err)
	crt, _, _, problem = c.secretPEM(ref)
	require.NotNil(t, problem, "unusable material is reported")
	require.Equal(t, []byte("garbage"), crt, "and handed over unusable, so nothing is withdrawn")
}

func TestControllerSkipsListenersNotYetCreated(t *testing.T) {
	// A listener the reload has not created yet cannot hold a certificate; the
	// pass that creates it ends in another push
	sink := newCertSink()
	p := &publisher{}
	start(t, p, sink, ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")
	require.Empty(t, sink.snapshot())
}

func TestControllerSkipsEmptySecrets(t *testing.T) {
	// A Secret with no material in it is not pushed; an empty certificate would
	// fail every handshake exactly as no certificate does, but silently
	listener := tlsListener
	sink := newCertSink(listener)
	secret := tlsSecret("")
	secret.Data[corev1.TLSCertKey] = nil
	p := &publisher{}
	start(t, p, sink, ingressClass(), service(), secret,
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")
	require.Empty(t, sink.snapshot())
}

func TestControllerRetriesRejectedCertificates(t *testing.T) {
	// A store that rejects a certificate must be retried, not recorded as
	// holding one it does not have
	listener := tlsListener
	sink := newCertSink(listener)
	sink.err = errors.New("bad certificate")
	p := &publisher{}
	c, _ := start(t, p, sink, ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")
	require.Empty(t, c.cfg.CertState.keys())

	sink.mtx.Lock()
	sink.err = nil
	sink.mtx.Unlock()
	c.reconcile(context.Background())
	require.Equal(t, "cert-v1", sink.snapshot()[listener+"|shop/shop-tls"])
}

func TestControllerWithoutCertSink(t *testing.T) {
	// A controller with no certificate sink still serves plaintext
	p := &publisher{}
	start(t, p, nil, ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")
}

func TestControllerPublishesEmptyCluster(t *testing.T) {
	// An empty cluster produces an empty overlay rather than no overlay: the
	// daemon still has to be told that there is nothing to serve
	p := &publisher{}
	start(t, p, nil)
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")
	require.Empty(t, p.last().Data)
}

func TestControllerStopIsIdempotent(t *testing.T) {
	// Stopping twice, and stopping a controller that is already stopped, are
	// both safe: shutdown and a configuration change can race
	p := &publisher{}
	c, _ := start(t, p, nil, ingressClass(), service(), webIngress())
	c.Stop()
	c.Stop()
}

func TestNewBuildsItsOwnClients(t *testing.T) {
	// The clients are built from the configuration when not supplied, and a connection that
	// cannot be resolved is an error rather than a controller that watches nothing
	o := options(t)
	o.Connection.Kubeconfig = "/nonexistent/kubeconfig"
	_, err := New(Config{Options: o, Publisher: &publisher{}})
	require.Error(t, err)

	// a cluster that does not serve the Gateway API still serves Ingress,
	// and must not be given informers over kinds that can never sync
	c, err := New(Config{
		Options: options(t), Publisher: &publisher{},
		Client: kube.NewFromClientset(kubefake.NewClientset()),
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.Nil(t, c.cfg.GatewayClient)
	require.False(t, c.watcher.ServesGatewayAPI())
	require.NoError(t, c.Start(t.Context()))
}

func TestControllerKeepsLastGoodOnCompileError(t *testing.T) {
	// A configuration the compiler cannot express leaves the last-good one in
	// force: one untranslatable object must not take every route down
	p := &publisher{}
	cs := kubefake.NewClientset(ingressClass(), service(), webIngress())
	o := options(t)
	// a routing mode the compiler does not know, set after validation
	o.Defaults.RoutingMode = "teleport"
	c, err := New(Config{
		Options: o, Publisher: p,
		Client:        kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	require.Equal(t, 0, p.count(),
		"a configuration that cannot be compiled must not be published")
}

func TestControllerReportsProblemsOncePerSet(t *testing.T) {
	// An informer resync re-delivers every object unchanged, so the same
	// complaints must not be logged again on a timer
	p := &publisher{}
	missing := webIngress()
	missing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = "absent-svc"
	c, cs := start(t, p, nil, ingressClass(), service(), missing)
	eventually(t, func() bool { return p.count() == 1 }, "no overlay published")
	c.mtx.Lock()
	first := c.problems
	c.mtx.Unlock()
	require.NotEmpty(t, first)

	c.reconcile(context.Background())
	c.mtx.Lock()
	require.Equal(t, first, c.problems,
		"an unchanged set of problems must stay the same set")
	c.mtx.Unlock()

	// resolving the reference changes the set
	fixed := webIngress()
	_, err := cs.NetworkingV1().Ingresses("shop").
		Update(t.Context(), fixed, metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool {
		c.mtx.Lock()
		defer c.mtx.Unlock()
		return c.problems != first
	}, "a resolved problem must change the reported set")
}

func TestNewBuildsGatewayClientWhereAvailable(t *testing.T) {
	// Where the cluster does serve the Gateway API, the clientset is built from
	// the same connection; a client that carries no REST config cannot supply one
	cs := kubefake.NewClientset()
	cs.Resources = []*metav1.APIResourceList{
		{GroupVersion: gatewayapi.GroupVersion},
	}
	_, err := New(Config{
		Options: options(t), Publisher: &publisher{},
		Client: kube.NewFromClientset(cs),
	})
	require.ErrorIs(t, err, kube.ErrNoRESTConfig)
}

func TestControllerForgetsCertificatesItCannotWithdraw(t *testing.T) {
	// A store that will not give a certificate up is reported and forgotten
	// anyway, so the next pass pushes whatever the cluster now says
	listener := tlsListener
	sink := newCertSink(listener)
	p := &publisher{}
	c, cs := start(t, p, sink, ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	eventually(t, func() bool { return len(sink.snapshot()) == 1 },
		"the certificate never reached the listener")

	sink.mtx.Lock()
	sink.err = errors.New("cannot withdraw")
	sink.mtx.Unlock()
	_, err := cs.NetworkingV1().Ingresses("shop").
		Update(t.Context(), webIngress(), metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool {
		return len(c.cfg.CertState.keys()) == 0
	}, "a certificate that could not be withdrawn was still tracked")
}

func TestControllerSkipsMissingSecrets(t *testing.T) {
	// A Secret that has gone missing between the translation and the push is
	// simply not pushed
	listener := tlsListener
	sink := newCertSink(listener)
	p := &publisher{}
	c, _ := start(t, p, sink, ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	eventually(t, func() bool { return len(sink.snapshot()) == 1 },
		"the certificate never reached the listener")
	require.Nil(t, c.watcher.Secret("shop", "absent"))
}

func TestReconcilesAreSerialized(t *testing.T) {
	// Two reconciles cannot overlap: the older one's translation could be published after the
	// newer one's, keeping a route the cluster no longer has until the next resync
	var inFlight, overlaps atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	p := &publisher{}
	p.onPublish = func() {
		if inFlight.Add(1) > 1 {
			overlaps.Add(1)
		}
		// hold the first pass open long enough for a second delivery to
		// arrive, which is exactly the race
		once.Do(func() {
			close(entered)
			<-release
		})
		inFlight.Add(-1)
	}
	c, cs := newController(t, Config{Options: options(t), Publisher: p},
		ingressClass(), service(), webIngress())
	t.Cleanup(c.Stop)
	started := make(chan error, 1)
	go func() { started <- c.Start(t.Context()) }()
	<-entered

	second := webIngress()
	second.Name = "other"
	second.Spec.Rules[0].Host = "other.example.com"
	_, err := cs.NetworkingV1().Ingresses("shop").
		Create(t.Context(), second, metav1.CreateOptions{})
	require.NoError(t, err)
	// let the watch deliver while the first pass is still held
	time.Sleep(50 * time.Millisecond)
	close(release)
	require.NoError(t, <-started)

	eventually(t, func() bool { return p.count() == 2 },
		"the second change was never applied")
	require.Zero(t, overlaps.Load(), "two reconciles ran at once")
	require.Contains(t, string(p.last().Data), "kgw--ingress.shop.other_r0",
		"the newest cache state must be what is left applied")
}

func TestStopWaitsForAReconcileInFlight(t *testing.T) {
	// Stop waits for a pass that is already running, so a controller being
	// replaced cannot still be publishing after its replacement has taken over
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var finished atomic.Bool
	p := &publisher{}
	p.onPublish = func() {
		once.Do(func() {
			close(entered)
			<-release
			finished.Store(true)
		})
	}
	c, _ := newController(t, Config{Options: options(t), Publisher: p},
		ingressClass(), service(), webIngress())
	go func() { _ = c.Start(t.Context()) }()
	<-entered

	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a reconcile was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-stopped
	require.True(t, finished.Load())
}

func TestCertificateOwnershipSurvivesReplacement(t *testing.T) {
	// The certificate stores outlive any one controller, so a controller built from a
	// narrowed configuration can only withdraw what it is told its predecessor installed
	listener := tlsListener
	sink := newCertSink(listener)
	state := NewCertState()

	objects := append([]runtime.Object{
		ingressClass(), service(), tlsSecret("cert-a"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}),
	}, otherNamespace()...)

	// the first controller claims both namespaces
	first, _ := newController(t, controllerConfig(t, sink, state, ""),
		objects...)
	require.NoError(t, first.Start(t.Context()))
	eventually(t, func() bool { return len(sink.snapshot()) == 2 },
		"both certificates should have reached the listener")
	first.Stop()
	require.Len(t, sink.snapshot(), 2,
		"stopping must not blank the listener it is being replaced on")

	// its replacement is scoped to one of them, and inherits the inventory
	second, _ := newController(t, controllerConfig(t, sink, state, "warehouse"),
		objects...)
	t.Cleanup(second.Stop)
	require.NoError(t, second.Start(t.Context()))
	eventually(t, func() bool {
		_, gone := sink.snapshot()[listener+"|shop/shop-tls"]
		return !gone
	}, "the certificate of a namespace no longer claimed was left installed")
	require.Contains(t, sink.snapshot(), listener+"|warehouse/warehouse-tls")
}

func TestRejectedApplyKeepsLiveCertificates(t *testing.T) {
	// A configuration the daemon rejected is not running, so the live routes keep their
	// certificates, and a rotation for one of those routes must still reach the store
	listener := tlsListener
	sink := newCertSink(listener)
	p := &publisher{}
	objects := append([]runtime.Object{
		ingressClass(), service(), tlsSecret("cert-a"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}),
	}, otherNamespace()...)
	c, cs := newController(t,
		Config{Options: options(t), Publisher: p, Certs: sink}, objects...)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	eventually(t, func() bool { return len(sink.snapshot()) == 2 },
		"both certificates should have reached the listener")

	// the next configuration drops one TLS route and is rejected, so the
	// old routes are still the ones serving
	p.mtx.Lock()
	p.err = errors.New("configuration rejected")
	p.mtx.Unlock()
	err := cs.NetworkingV1().Ingresses("shop").
		Delete(t.Context(), "web", metav1.DeleteOptions{})
	require.NoError(t, err)
	eventually(t, func() bool { return p.count() == 2 },
		"the change was never attempted")
	require.Equal(t, "cert-a", sink.snapshot()[listener+"|shop/shop-tls"],
		"a rejected configuration must not withdraw a live route's certificate")

	// a rotation for a route that is still serving is still applied, even
	// though the configuration around it is being rejected
	_, err = cs.CoreV1().Secrets("warehouse").
		Update(t.Context(), warehouseSecret("cert-b2"), metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool {
		return sink.snapshot()[listener+"|warehouse/warehouse-tls"] == "cert-b2"
	}, "a rotation for a live route must still be applied")
	require.Len(t, sink.snapshot(), 2)
}

func TestRejectedRotationKeepsOwnership(t *testing.T) {
	// The store validates a pair before replacing anything, so a rejected rotation leaves
	// the previous certificate installed and still ours to withdraw
	listener := tlsListener
	sink := newCertSink(listener)
	p := &publisher{}
	c, cs := start(t, p, sink, ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	eventually(t, func() bool { return len(sink.snapshot()) == 1 },
		"the certificate never reached the listener")

	sink.mtx.Lock()
	sink.err = errors.New("malformed pem")
	sink.mtx.Unlock()
	_, err := cs.CoreV1().Secrets("shop").
		Update(t.Context(), tlsSecret("bad-pem"), metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool { return sink.rejected.Load() > 0 },
		"the rotation was never attempted")
	require.Equal(t, "cert-v1", sink.snapshot()[listener+"|shop/shop-tls"],
		"the rejected rotation must leave the old certificate installed")
	require.Len(t, c.cfg.CertState.keys(), 1,
		"a certificate still installed is still ours to withdraw")

	// removing the reference must now withdraw it, which it cannot do for
	// an entry it has forgotten
	sink.mtx.Lock()
	sink.err = nil
	sink.mtx.Unlock()
	_, err = cs.NetworkingV1().Ingresses("shop").
		Update(t.Context(), webIngress(), metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool { return len(sink.snapshot()) == 0 },
		"the certificate left by a rejected rotation was never withdrawn")
	c.Stop()
}

func TestRetiredControllerTouchesNothing(t *testing.T) {
	// A controller whose publisher has retired stops there: its translation describes a scope
	// this process no longer has, and the shared certificate inventory is no longer its to write
	listener := tlsListener
	sink := newCertSink(listener)
	p := &publisher{err: ErrRetired}

	// the inventory already holds what the live configuration installed,
	// which is what a retired pass could otherwise rotate out from under it
	state := NewCertState()
	live := certKey{listener: listener, source: "shop/shop-tls"}
	state.record(live, "installed-by-the-live-generation", ir.CertIdentity{})

	cfg := Config{
		Options: options(t), Publisher: p, Certs: sink, CertState: state,
	}
	c, _ := newController(t, cfg, ingressClass(), service(),
		tlsSecret("cert-v1"), webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	eventually(t, func() bool { return p.count() == 1 },
		"no overlay was offered")

	require.Empty(t, sink.snapshot(),
		"a retired controller must not write to the certificate stores")
	digest, owned := state.digest(live)
	require.True(t, owned)
	require.Equal(t, "installed-by-the-live-generation", digest,
		"a retired controller rewrote the live generation's inventory")
}

func TestReconcilePanicDoesNotKillTheWorker(t *testing.T) {
	// A panic in translation must not kill the worker and freeze the data plane
	// at its last-good configuration without saying so
	var panics atomic.Int32
	p := &publisher{}
	p.onPublish = func() {
		if panics.Add(1) == 1 {
			panic("translation exploded")
		}
	}
	c, cs := newController(t, Config{Options: options(t), Publisher: p},
		ingressClass(), service(), webIngress())
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))

	second := webIngress()
	second.Name = "other"
	second.Spec.Rules[0].Host = "other.example.com"
	_, err := cs.NetworkingV1().Ingresses("shop").
		Create(t.Context(), second, metav1.CreateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool { return p.count() == 2 },
		"the worker did not survive a panicking pass")
}

func TestStartReportsAWatchThatCannotSync(t *testing.T) {
	// Start reports a cache that never syncs rather than blocking on a first
	// pass that will not come
	c, _ := newController(t, Config{Options: options(t), Publisher: &publisher{}})
	t.Cleanup(c.Stop)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, c.Start(ctx))
}

func TestCertificatesReinstallAfterTheListenerIsDestroyed(t *testing.T) {
	// A listener's certificates live only as long as the listener, so re-enabling the controller
	// with an unchanged Secret must install it again rather than trust a remembered digest
	listener := tlsListener
	sink := newCertSink(listener)
	state := NewCertState()
	objects := []runtime.Object{
		ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}),
	}

	first, _ := newController(t, controllerConfig(t, sink, state, ""),
		objects...)
	require.NoError(t, first.Start(t.Context()))
	eventually(t, func() bool { return len(sink.snapshot()) == 1 },
		"the certificate never reached the listener")
	first.Stop()

	// disabling the controller unloads the generated configuration, which
	// takes the listener and its certificates with it
	sink.destroyListener(listener)
	require.Empty(t, sink.snapshot())

	// re-enabling recreates the listener, with the empty store it starts
	// life with, and the same Secret it had before
	sink.createListener(listener)
	second, _ := newController(t, controllerConfig(t, sink, state, ""),
		objects...)
	t.Cleanup(second.Stop)
	require.NoError(t, second.Start(t.Context()))
	eventually(t, func() bool {
		return sink.snapshot()[listener+"|shop/shop-tls"] == "cert-v1"
	}, "the certificate was never reinstalled on the recreated listener")
}

func TestResyncBeforeSyncTranslatesNothing(t *testing.T) {
	// Nothing may be translated before the caches have synced: a pass over a cache still
	// filling describes a cluster missing most of itself and would delete the unseen routes
	p := &publisher{}
	sink := newCertSink(tlsListener)
	c, _ := newController(t,
		Config{Options: options(t), Publisher: p, Certs: sink},
		ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	t.Cleanup(c.Stop)

	// this is the supervisor's path when a configuration adds a cache, and
	// it does not go through the watch layer's own gate
	for range 5 {
		c.Resync()
	}
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, p.count(),
		"a pass ran before the caches had synced")
	require.Empty(t, sink.snapshot())

	// once the caches sync, the request that was waiting is served
	require.NoError(t, c.Start(t.Context()))
	require.Equal(t, 1, p.count())
	require.Contains(t, string(p.last().Data), "kgw--ingress.shop.web_r0")
}

func TestRetiringControllerLeavesCertificatesAlone(t *testing.T) {
	// A controller that is being retired stops touching the certificate stores,
	// so a pass that overlaps its replacement cannot undo the replacement's work
	listener := tlsListener
	sink := newCertSink(listener)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	p := &publisher{}
	p.onPublish = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	c, _ := newController(t,
		Config{Options: options(t), Publisher: p, Certs: sink},
		ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}))
	go func() { _ = c.Start(t.Context()) }()
	<-entered

	// the supervisor retires it while the pass is still between publishing
	// and applying its certificates
	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()
	eventually(t, func() bool { return c.stopping() }, "Stop never began")
	close(release)
	<-stopped
	require.Empty(t, sink.snapshot(),
		"a controller being retired wrote to the certificate stores")
}

// gatewayGVR is the Gateway resource, named explicitly because the fake tracker pluralizes
// "Gateway" as "gatewaies" and files seeded objects where the client never reads
var gatewayGVR = schema.GroupVersionResource{
	Group: gwapiv1.GroupName, Version: "v1", Resource: "gateways"}

func TestControllerTranslatesGatewayAPIObjects(t *testing.T) {
	// A cluster serving the Gateway API is translated alongside its Ingresses: a Gateway mints
	// listeners, an HTTPRoute becomes a backend, and the HTTPS Secret reaches its store
	https := gwapiv1.HTTPSProtocolType
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "gw"},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "trickster",
			Listeners: []gwapiv1.Listener{
				{Name: "http", Port: 80, Protocol: gwapiv1.HTTPProtocolType},
				{Name: "https", Port: 443, Protocol: https,
					TLS: &gwapiv1.ListenerTLSConfig{CertificateRefs: []gwapiv1.SecretObjectReference{
						{Name: "shop-tls"}}}},
			},
		},
	}
	port := gwapiv1.PortNumber(8080)
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}}},
			Hostnames:       []gwapiv1.Hostname{"shop.example.com"},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{{BackendRef: gwapiv1.BackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: "web-svc", Port: &port}}}},
			}},
		},
	}
	gwcs := gwfake.NewSimpleClientset(
		&gwapiv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "trickster"},
			Spec: gwapiv1.GatewayClassSpec{
				ControllerName: kubecfg.DefaultGatewayClassControllerName},
		},
		route,
	)
	require.NoError(t, gwcs.Tracker().Create(gatewayGVR, gw, gw.Namespace))
	p := &publisher{}
	sink := newCertSink("kgw--listener-https-443")
	c, err := New(Config{
		Options:       options(t),
		Publisher:     p,
		Certs:         sink,
		Client:        kube.NewFromClientset(kubefake.NewClientset(service(), tlsSecret("cert-a"))),
		GatewayClient: gwcs,
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))

	data := string(p.last().Data)
	require.Contains(t, data, "kgw--listener-http-80")
	require.Contains(t, data, "kgw--listener-https-443")
	require.Contains(t, data, "kgw--httproute.shop.web_r0")
	require.Contains(t, data, "kgw--httproute.shop.web_r1",
		"the route is served on both listeners")
	require.Contains(t, data, "origin_url: http://web-svc.shop.svc:8080")
	eventually(t, func() bool {
		return sink.snapshot()["kgw--listener-https-443|shop/shop-tls"] == "cert-a"
	}, "the Gateway listener's certificate was not pushed")
}

// recorder captures Events as type, reason and object
type recorder struct {
	mtx    sync.Mutex
	events []string
}

func (r *recorder) Event(obj runtime.Object, typ, reason, _ string) {
	ref, ok := obj.(*corev1.ObjectReference)
	if !ok {
		return
	}
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.events = append(r.events, fmt.Sprintf("%s %s %s/%s/%s", typ, reason,
		ref.Kind, ref.Namespace, ref.Name))
}

func (r *recorder) Eventf(obj runtime.Object, typ, reason, format string, args ...any) {
	r.Event(obj, typ, reason, fmt.Sprintf(format, args...))
}

func (r *recorder) AnnotatedEventf(obj runtime.Object, _ map[string]string, typ, reason,
	format string, args ...any,
) {
	r.Eventf(obj, typ, reason, format, args...)
}

func (r *recorder) list() []string {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return slices.Clone(r.events)
}

func (r *recorder) count() int { return len(r.list()) }

func publishedService() *corev1.Service {
	// publishedService is the Service whose addresses are written into status
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "trickster", Name: "trickster-gateway"},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}}}},
	}
}

func bogusIngress() *netv1.Ingress {
	// bogusIngress is the web Ingress carrying an annotation that is rejected
	ing := webIngress()
	ing.Annotations = map[string]string{appinfo.Domain + "/bogus": "x"}
	return ing
}

func ingressStatus(t *testing.T, cs *kubefake.Clientset) []netv1.IngressLoadBalancerIngress {
	t.Helper()
	ing, err := cs.NetworkingV1().Ingresses("shop").Get(t.Context(), "web", metav1.GetOptions{})
	require.NoError(t, err)
	return ing.Status.LoadBalancer.Ingress
}

func TestControllerWritesIngressStatus(t *testing.T) {
	// The sole writer publishes the Service's addresses into every claimed Ingress
	o := options(t)
	o.PublishedService = &kubecfg.ServiceRef{Namespace: "trickster", Name: "trickster-gateway"}
	cs := kubefake.NewClientset(ingressClass(), service(), webIngress(), publishedService())
	c, err := New(Config{
		Options: o, Publisher: &publisher{}, Client: kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	require.True(t, c.IsLeader())
	eventually(t, func() bool {
		lb := ingressStatus(t, cs)
		return len(lb) == 1 && lb[0].IP == "10.0.0.1"
	}, "the published address should reach the Ingress status")
}

func TestControllerPublishesEventsOncePerTerm(t *testing.T) {
	// A problem is published once per set: a resync re-delivering the same objects publishes
	// nothing, and a new leadership term publishes everything current again
	rec := &recorder{}
	cs := kubefake.NewClientset(ingressClass(), service(), bogusIngress())
	c, err := New(Config{
		Options: options(t), Publisher: &publisher{}, Client: kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(), Recorder: events.NewWithRecorder(rec),
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	eventually(t, func() bool { return rec.count() == 1 }, "the rejected annotation was not reported")
	require.Contains(t, rec.list()[0], "Warning InvalidAnnotation Ingress/shop/web")

	c.reconcile(context.Background())
	require.Equal(t, 1, rec.count(), "an unchanged pass must not repeat itself")

	c.onLeaderChange(true)
	eventually(t, func() bool { return rec.count() == 2 }, "a new term should republish")
	c.onLeaderChange(false)
	c.reconcile(context.Background())
	require.Equal(t, 2, rec.count())
}

func TestReadOnlyControllerWritesNothing(t *testing.T) {
	// A read-only instance serves the same routes but never writes to the cluster
	o := options(t)
	o.ReadOnly = true
	rec := &recorder{}
	p := &publisher{}
	cs := kubefake.NewClientset(ingressClass(), service(), bogusIngress())
	c, err := New(Config{
		Options: o, Publisher: p, Client: kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(), Recorder: events.NewWithRecorder(rec),
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	require.False(t, c.IsLeader())
	require.Contains(t, string(p.last().Data), "kgw--ingress.shop.web_r0")
	require.Equal(t, 0, rec.count())
	require.Empty(t, ingressStatus(t, cs))
	require.Nil(t, c.status)
	require.Nil(t, c.elector)
}

func TestControllerElection(t *testing.T) {
	// Two replicas over one cluster: the leader writes, the other only serves,
	// and the Lease hands over when the leader stops
	o := options(t)
	o.LeaderElection.Enabled = new(true)
	o.LeaderElection.Namespace = "trickster"
	o.LeaderElection.LeaseDuration = timeconv.Duration(2 * time.Second)
	o.LeaderElection.RenewDeadline = timeconv.Duration(time.Second)
	o.LeaderElection.RetryPeriod = timeconv.Duration(100 * time.Millisecond)
	require.NoError(t, o.Validate())
	cs := kubefake.NewClientset(ingressClass(), service(), bogusIngress())
	build := func(identity string, rec *recorder) *Controller {
		c, err := New(Config{
			Options: o, Publisher: &publisher{}, Identity: identity,
			Client:        kube.NewFromClientset(cs),
			GatewayClient: gwfake.NewSimpleClientset(),
			Recorder:      events.NewWithRecorder(rec),
		})
		require.NoError(t, err)
		t.Cleanup(c.Stop)
		require.NoError(t, c.Start(t.Context()))
		return c
	}
	recA, recB := &recorder{}, &recorder{}
	a := build("a", recA)
	eventually(t, a.IsLeader, "an uncontended lease should be won")
	eventually(t, func() bool { return recA.count() == 1 }, "the leader publishes Events")

	b := build("b", recB)
	require.Contains(t, string(b.cfg.Publisher.(*publisher).last().Data),
		"kgw--ingress.shop.web_r0", "a follower still programs its data plane")
	time.Sleep(300 * time.Millisecond)
	require.False(t, b.IsLeader())
	require.Equal(t, 0, recB.count(), "a follower writes nothing")

	a.Stop()
	eventually(t, b.IsLeader, "the released lease should pass to the follower")
	eventually(t, func() bool { return recB.count() == 1 }, "the new leader publishes")
}

func TestControllerWritesGatewayStatus(t *testing.T) {
	// Gateway API objects get their conditions, the class an acceptance Event
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "gw", Generation: 2},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "trickster",
			Listeners: []gwapiv1.Listener{
				{Name: "http", Port: 80, Protocol: gwapiv1.HTTPProtocolType}},
		},
	}
	port := gwapiv1.PortNumber(8080)
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}}},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{{BackendRef: gwapiv1.BackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: "missing", Port: &port}}}},
			}},
		},
	}
	gwcs := gwfake.NewSimpleClientset(
		&gwapiv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "trickster"},
			Spec: gwapiv1.GatewayClassSpec{
				ControllerName: kubecfg.DefaultGatewayClassControllerName},
		},
		route,
	)
	require.NoError(t, gwcs.Tracker().Create(gatewayGVR, gw, gw.Namespace))
	rec := &recorder{}
	c, err := New(Config{
		Options: options(t), Publisher: &publisher{},
		Client:        kube.NewFromClientset(kubefake.NewClientset(service())),
		GatewayClient: gwcs, Recorder: events.NewWithRecorder(rec),
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))

	eventually(t, func() bool {
		got, err := gwcs.GatewayV1().Gateways("shop").Get(t.Context(), "gw", metav1.GetOptions{})
		return err == nil && len(got.Status.Listeners) == 1 &&
			got.Status.Listeners[0].AttachedRoutes == 1
	}, "the Gateway status was not written")
	got, err := gwcs.GatewayV1().Gateways("shop").Get(t.Context(), "gw", metav1.GetOptions{})
	require.NoError(t, err)
	accepted := meta.FindStatusCondition(got.Status.Conditions, "Accepted")
	require.NotNil(t, accepted)
	require.Equal(t, metav1.ConditionTrue, accepted.Status)
	require.EqualValues(t, 2, accepted.ObservedGeneration)
	require.Equal(t, metav1.ConditionTrue,
		meta.FindStatusCondition(got.Status.Conditions, "Programmed").Status)

	var r *gwapiv1.HTTPRoute
	eventually(t, func() bool {
		r, err = gwcs.GatewayV1().HTTPRoutes("shop").Get(t.Context(), "web", metav1.GetOptions{})
		return err == nil && len(r.Status.Parents) == 1
	}, "the route status was not written")
	require.EqualValues(t, kubecfg.DefaultGatewayClassControllerName, r.Status.Parents[0].ControllerName)
	resolved := meta.FindStatusCondition(r.Status.Parents[0].Conditions, "ResolvedRefs")
	require.Equal(t, metav1.ConditionFalse, resolved.Status)
	require.EqualValues(t, gwapiv1.RouteReasonBackendNotFound, resolved.Reason)

	gc, err := gwcs.GatewayV1().GatewayClasses().Get(t.Context(), "trickster", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, metav1.ConditionTrue,
		meta.FindStatusCondition(gc.Status.Conditions, "Accepted").Status)
	eventually(t, func() bool {
		return slices.Contains(rec.list(), "Normal Accepted GatewayClass//trickster")
	}, "the claimed class should be told so")
	require.Contains(t, rec.list(), "Warning Rejected HTTPRoute/shop/web")
}

func TestControllerTracesReconcile(t *testing.T) {
	// A pass is traced from translation to status, on the tracer the section names
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tr := &tracing.Tracer{Tracer: tp.Tracer("test")}
	cs := kubefake.NewClientset(ingressClass(), service(), webIngress())
	c, err := New(Config{
		Options: options(t), Publisher: &publisher{}, Client: kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
		Tracer:        func() *tracing.Tracer { return tr },
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	names := func() []string {
		var out []string
		for _, s := range sr.Ended() {
			out = append(out, s.Name())
		}
		return out
	}
	for _, want := range []string{spanReconcile, spanTranslate, spanCompile, spanApply} {
		require.Contains(t, names(), want)
	}
	// status is written on its own worker, so its span ends on its own time
	eventually(t, func() bool { return slices.Contains(names(), spanStatus) },
		"the status span was never recorded")
	// a tracer lookup that answers nothing traces nothing
	c.cfg.Tracer = func() *tracing.Tracer { return nil }
	_, span := c.span(context.Background(), spanReconcile)
	require.Nil(t, span)
	endSpan(nil, nil)
}

func TestControllerObservesModel(t *testing.T) {
	// The model gauges and the route join follow every pass, and are cleared when the
	// controller stops so a retired scope leaves no series behind
	p := &publisher{}
	c, _ := start(t, p, nil, ingressClass(), service(), webIngress())
	require.Equal(t, float64(1),
		testutil.ToFloat64(metrics.KubeGeneratedObjects.WithLabelValues(gaugeRoutes)))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.KubeRouteInfo.WithLabelValues(
		ir.KindIngress, "web", "shop", "kgw--ingress.shop.web_r0")))
	require.Positive(t, testutil.ToFloat64(metrics.KubeLastSuccessfulSync))
	c.Stop()
	require.Equal(t, 0, testutil.CollectAndCount(metrics.KubeRouteInfo))
}

func TestNewRejectsAnElectionItCannotHold(t *testing.T) {
	// An election that cannot be held is a construction error, and the watcher
	// built before it is released
	o := options(t)
	o.LeaderElection.Enabled = new(true)
	o.LeaderElection.LeaseDuration = timeconv.Duration(500 * time.Millisecond)
	o.LeaderElection.RenewDeadline = timeconv.Duration(200 * time.Millisecond)
	o.LeaderElection.RetryPeriod = timeconv.Duration(50 * time.Millisecond)
	_, err := New(Config{
		Options: o, Publisher: &publisher{},
		Client:        kube.NewFromClientset(kubefake.NewClientset()),
		GatewayClient: gwfake.NewSimpleClientset(),
	})
	require.ErrorIs(t, err, leader.ErrLeaseTooShort)
}

func TestControllerCountsStatusWriteFailures(t *testing.T) {
	// A status write the API server refuses is counted against the pass and
	// tried again on the next one
	o := options(t)
	o.PublishedService = &kubecfg.ServiceRef{Namespace: "trickster", Name: "trickster-gateway"}
	cs := kubefake.NewClientset(ingressClass(), service(), webIngress(), publishedService())
	cs.PrependReactor("update", "ingresses", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	before := testutil.ToFloat64(metrics.KubeReconcileErrors.WithLabelValues(metrics.KubeStageStatus))
	c, err := New(Config{
		Options: o, Publisher: &publisher{}, Client: kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	eventually(t, func() bool {
		return testutil.ToFloat64(metrics.KubeReconcileErrors.WithLabelValues(
			metrics.KubeStageStatus)) == before+1
	}, "the refused write was not counted")
	require.Empty(t, ingressStatus(t, cs))
}

func TestControllerTracesApplyFailure(t *testing.T) {
	// A pass that fails records the failure on its spans
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tr := &tracing.Tracer{Tracer: tp.Tracer("test")}
	cs := kubefake.NewClientset(ingressClass(), service(), webIngress())
	c, err := New(Config{
		Options: options(t), Publisher: &publisher{err: errors.New("rejected")},
		Client:        kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
		Tracer:        func() *tracing.Tracer { return tr },
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	var failed []string
	for _, s := range sr.Ended() {
		if s.Status().Code == codes.Error {
			failed = append(failed, s.Name())
		}
	}
	require.Contains(t, failed, spanApply)
	require.Contains(t, failed, spanReconcile)
}

// gatedClientset holds every Ingress status write until released or its context ends, as a
// slow endpoint would; it replaces only the writer's client, as it breaks the fake's watches
type gatedClientset struct {
	kubernetes.Interface
	gate    chan struct{}
	entered chan struct{}
	calls   atomic.Int32
}

func newGatedClientset(objects ...runtime.Object) (*gatedClientset, *kubefake.Clientset) {
	cs := kubefake.NewClientset(objects...)
	return &gatedClientset{
		Interface: cs, gate: make(chan struct{}), entered: make(chan struct{}, 16),
	}, cs
}

func gateStatus(c *Controller, g *gatedClientset) {
	// gateStatus makes the controller write status through the gated client
	c.status = status.New(status.Config{
		Client: g, GatewayClient: c.cfg.GatewayClient, Cache: c.watcher,
		ControllerName: c.cfg.Options.GatewayClassControllerName,
	})
}

func (g *gatedClientset) NetworkingV1() networkingv1.NetworkingV1Interface {
	return gatedNetworking{g.Interface.NetworkingV1(), g}
}

type gatedNetworking struct {
	networkingv1.NetworkingV1Interface
	g *gatedClientset
}

func (n gatedNetworking) Ingresses(ns string) networkingv1.IngressInterface {
	return gatedIngresses{n.NetworkingV1Interface.Ingresses(ns), n.g}
}

type gatedIngresses struct {
	networkingv1.IngressInterface
	g *gatedClientset
}

func (i gatedIngresses) UpdateStatus(ctx context.Context, ing *netv1.Ingress,
	opts metav1.UpdateOptions,
) (*netv1.Ingress, error) {
	i.g.calls.Add(1)
	select {
	case i.g.entered <- struct{}{}:
	default:
	}
	select {
	case <-i.g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return i.IngressInterface.UpdateStatus(ctx, ing, opts)
}

func awaitEntry(t *testing.T, g *gatedClientset) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no status write was attempted")
	}
}

func apiIngress() *netv1.Ingress {
	// apiIngress is a second claimed Ingress in the shop namespace
	ing := webIngress()
	ing.Name = "api"
	ing.Spec.Rules[0].HTTP.Paths[0].Path = "/v2"
	return ing
}

func TestStatusWritesDoNotBlockReconciliation(t *testing.T) {
	// A status write that is stuck does not hold up the data plane: a route
	// added and a certificate rotated while it waits are served before it returns
	o := options(t)
	o.PublishedService = &kubecfg.ServiceRef{Namespace: "trickster", Name: "trickster-gateway"}
	g, cs := newGatedClientset(ingressClass(), service(), tlsSecret("cert-v1"),
		webIngress(netv1.IngressTLS{SecretName: "shop-tls"}), publishedService())
	sink := newCertSink(tlsListener)
	p := &publisher{}
	c, err := New(Config{
		Options: o, Publisher: p, Certs: sink, Client: kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
	})
	require.NoError(t, err)
	gateStatus(c, g)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()), "starting does not wait for status")
	awaitEntry(t, g)

	_, err = cs.NetworkingV1().Ingresses("shop").Create(t.Context(), apiIngress(),
		metav1.CreateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool {
		last := p.last()
		return last != nil && strings.Contains(string(last.Data), "kgw--ingress.shop.api_r0")
	}, "the new route waited behind the status write")
	_, err = cs.CoreV1().Secrets("shop").Update(t.Context(), tlsSecret("cert-v2"),
		metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool {
		return sink.snapshot()[tlsListener+"|shop/shop-tls"] == "cert-v2"
	}, "the rotation waited behind the status write")
	require.EqualValues(t, 1, g.calls.Load(), "the status write is still held")

	close(g.gate)
	eventually(t, func() bool {
		ing, err := cs.NetworkingV1().Ingresses("shop").Get(t.Context(), "api", metav1.GetOptions{})
		return err == nil && len(ing.Status.LoadBalancer.Ingress) == 1
	}, "the latest report was not written once the endpoint answered")
}

func TestLeadershipLossCancelsClusterWrites(t *testing.T) {
	// A replica that loses the Lease mid-batch stops there: the write in flight is cut short,
	// nothing follows it, and no Event is published until it leads again
	o := options(t)
	o.LeaderElection.Enabled = new(true)
	o.LeaderElection.Namespace = "trickster"
	o.LeaderElection.LeaseDuration = timeconv.Duration(2 * time.Second)
	o.LeaderElection.RenewDeadline = timeconv.Duration(time.Second)
	o.LeaderElection.RetryPeriod = timeconv.Duration(100 * time.Millisecond)
	o.PublishedService = &kubecfg.ServiceRef{Namespace: "trickster", Name: "trickster-gateway"}
	require.NoError(t, o.Validate())
	g, cs := newGatedClientset(ingressClass(), service(), webIngress(), apiIngress(),
		publishedService())
	var blockRenewal atomic.Bool
	cs.PrependReactor("update", "leases", func(ktesting.Action) (bool, runtime.Object, error) {
		if blockRenewal.Load() {
			return true, nil, errors.New("api server unreachable")
		}
		return false, nil, nil
	})
	rec := &recorder{}
	c, err := New(Config{
		Options: o, Publisher: &publisher{}, Identity: "a", Client: kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(), Recorder: events.NewWithRecorder(rec),
	})
	require.NoError(t, err)
	gateStatus(c, g)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	eventually(t, c.IsLeader, "an uncontended lease should be won")
	awaitEntry(t, g)

	blockRenewal.Store(true)
	eventually(t, func() bool { return !c.IsLeader() }, "a lease that cannot be renewed is lost")
	time.Sleep(300 * time.Millisecond)
	require.EqualValues(t, 1, g.calls.Load(), "nothing is written after the term ended")
	require.Empty(t, ingressStatus(t, cs))

	// a problem found while not leading is not published
	_, err = cs.NetworkingV1().Ingresses("shop").Update(t.Context(), bogusIngress(),
		metav1.UpdateOptions{})
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, 0, rec.count())

	// leading again, the current verdict is written under the new term
	close(g.gate)
	blockRenewal.Store(false)
	eventually(t, c.IsLeader, "the lease should be regained once renewal works")
	eventually(t, func() bool { return len(ingressStatus(t, cs)) == 1 },
		"the new term should write status")
	eventually(t, func() bool { return rec.count() == 1 }, "the new term should publish")
}

func listenerCondition(gw *gwapiv1.Gateway, section, typ string) *metav1.Condition {
	for _, l := range gw.Status.Listeners {
		if string(l.Name) == section {
			return meta.FindStatusCondition(l.Conditions, typ)
		}
	}
	return nil
}

func httpsGateway() *gwapiv1.Gateway {
	// httpsGateway is a Gateway with one HTTPS listener serving the shop TLS Secret
	return &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "gw"},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "trickster",
			Listeners: []gwapiv1.Listener{{
				Name: "https", Port: 443, Protocol: gwapiv1.HTTPSProtocolType,
				TLS: &gwapiv1.ListenerTLSConfig{CertificateRefs: []gwapiv1.SecretObjectReference{
					{Name: "shop-tls"}}},
			}},
		},
	}
}

func gatewayClass() *gwapiv1.GatewayClass {
	return &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "trickster"},
		Spec: gwapiv1.GatewayClassSpec{
			ControllerName: kubecfg.DefaultGatewayClassControllerName},
	}
}

func startHTTPS(t *testing.T, secret *corev1.Secret) (*gwfake.Clientset, *kubefake.Clientset,
	*certSink,
) {
	// startHTTPS starts a controller over the HTTPS Gateway and the given shop Secret
	t.Helper()
	gwcs := gwfake.NewSimpleClientset(gatewayClass())
	gw := httpsGateway()
	require.NoError(t, gwcs.Tracker().Create(gatewayGVR, gw, gw.Namespace))
	cs := kubefake.NewClientset(service(), secret)
	sink := newCertSink("kgw--listener-https-443")
	c, err := New(Config{
		Options: options(t), Publisher: &publisher{}, Certs: sink,
		Client: kube.NewFromClientset(cs), GatewayClient: gwcs,
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	return gwcs, cs, sink
}

func awaitListener(t *testing.T, gwcs *gwfake.Clientset, cond func(*gwapiv1.Gateway) bool,
	msg string,
) *gwapiv1.Gateway {
	t.Helper()
	var gw *gwapiv1.Gateway
	eventually(t, func() bool {
		var err error
		gw, err = gwcs.GatewayV1().Gateways("shop").Get(t.Context(), "gw", metav1.GetOptions{})
		return err == nil && listenerCondition(gw, "https", "Programmed") != nil && cond(gw)
	}, msg)
	return gw
}

func TestListenerProgrammedFollowsCertificates(t *testing.T) {
	// An HTTPS listener is programmed only once it holds a usable certificate; a rotation to
	// unusable material lowers ResolvedRefs but leaves the listener serving and programmed
	gwcs, cs, sink := startHTTPS(t, tlsSecret("cert-a"))
	gw := awaitListener(t, gwcs, func(gw *gwapiv1.Gateway) bool {
		return listenerCondition(gw, "https", "Programmed").Status == metav1.ConditionTrue
	}, "a listener holding its certificate should be programmed")
	require.Equal(t, metav1.ConditionTrue, listenerCondition(gw, "https", "ResolvedRefs").Status)
	require.Equal(t, "cert-a", sink.snapshot()["kgw--listener-https-443|shop/shop-tls"])

	_, err := cs.CoreV1().Secrets("shop").Update(t.Context(), garbageSecret(), metav1.UpdateOptions{})
	require.NoError(t, err)
	gw = awaitListener(t, gwcs, func(gw *gwapiv1.Gateway) bool {
		return listenerCondition(gw, "https", "ResolvedRefs").Status == metav1.ConditionFalse
	}, "unusable material should lower ResolvedRefs")
	resolved := listenerCondition(gw, "https", "ResolvedRefs")
	require.EqualValues(t, gwapiv1.ListenerReasonInvalidCertificateRef, resolved.Reason)
	require.Equal(t, metav1.ConditionTrue, listenerCondition(gw, "https", "Programmed").Status,
		"the certificate already serving keeps the listener programmed")
	require.Equal(t, "cert-a", sink.snapshot()["kgw--listener-https-443|shop/shop-tls"],
		"a rejected rotation leaves the previous certificate installed")
}

func TestListenerWithoutUsableCertificateIsNotProgrammed(t *testing.T) {
	// A new HTTPS listener whose only material is unusable is not programmed:
	// nothing is installed and every handshake would fail
	gwcs, _, sink := startHTTPS(t, garbageSecret())
	gw := awaitListener(t, gwcs, func(gw *gwapiv1.Gateway) bool {
		return listenerCondition(gw, "https", "Programmed").Status == metav1.ConditionFalse
	}, "a listener with nothing to serve must not be programmed")
	programmed := listenerCondition(gw, "https", "Programmed")
	require.EqualValues(t, gwapiv1.ListenerReasonInvalid, programmed.Reason)
	require.Contains(t, programmed.Message, "no usable certificate")
	require.Equal(t, metav1.ConditionFalse, listenerCondition(gw, "https", "ResolvedRefs").Status)
	require.Empty(t, sink.snapshot())
}

func TestClassRefusalPropagatesToRoutes(t *testing.T) {
	// A class whose parameters stop being honorable takes its Gateways and their routes with it,
	// and their status says so rather than keeping the acceptance it had
	ns := gwapiv1.Namespace("infra")
	gc := gatewayClass()
	gc.Spec.ParametersRef = &gwapiv1.ParametersReference{
		Kind: "ConfigMap", Name: "params", Namespace: &ns}
	params := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "params"},
		Data:       map[string]string{"timeout": "5s"},
	}
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "gw"},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "trickster",
			Listeners: []gwapiv1.Listener{
				{Name: "http", Port: 80, Protocol: gwapiv1.HTTPProtocolType}},
		},
	}
	port := gwapiv1.PortNumber(8080)
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}}},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{{BackendRef: gwapiv1.BackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: "web-svc", Port: &port}}}},
			}},
		},
	}
	gwcs := gwfake.NewSimpleClientset(gc, route)
	require.NoError(t, gwcs.Tracker().Create(gatewayGVR, gw, gw.Namespace))
	cs := kubefake.NewClientset(service(), params)
	p := &publisher{}
	c, err := New(Config{
		Options: options(t), Publisher: p, Client: kube.NewFromClientset(cs), GatewayClient: gwcs,
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	routeAccepted := func(want metav1.ConditionStatus) func() bool {
		return func() bool {
			r, err := gwcs.GatewayV1().HTTPRoutes("shop").Get(t.Context(), "web", metav1.GetOptions{})
			if err != nil || len(r.Status.Parents) != 1 {
				return false
			}
			accepted := meta.FindStatusCondition(r.Status.Parents[0].Conditions, "Accepted")
			return accepted != nil && accepted.Status == want
		}
	}
	eventually(t, routeAccepted(metav1.ConditionTrue), "the route should be accepted at first")
	require.Contains(t, string(p.last().Data), "kgw--httproute.shop.web_r0")

	params.Data["bogus"] = "x"
	_, err = cs.CoreV1().ConfigMaps("infra").Update(t.Context(), params, metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, routeAccepted(metav1.ConditionFalse), "the route should be refused with its class")
	eventually(t, func() bool {
		return !strings.Contains(string(p.last().Data), "kgw--httproute.shop.web_r0")
	}, "the route should leave the data plane")
	r, err := gwcs.GatewayV1().HTTPRoutes("shop").Get(t.Context(), "web", metav1.GetOptions{})
	require.NoError(t, err)
	accepted := meta.FindStatusCondition(r.Status.Parents[0].Conditions, "Accepted")
	require.EqualValues(t, gwapiv1.RouteReasonNoMatchingParent, accepted.Reason)
	require.Contains(t, accepted.Message, "cannot be honored")
	got, err := gwcs.GatewayV1().Gateways("shop").Get(t.Context(), "gw", metav1.GetOptions{})
	require.NoError(t, err)
	require.EqualValues(t, gwapiv1.GatewayReasonInvalidParameters,
		meta.FindStatusCondition(got.Status.Conditions, "Accepted").Reason)
	require.Equal(t, metav1.ConditionFalse, listenerCondition(got, "http", "Programmed").Status)
	require.Equal(t, metav1.ConditionTrue, listenerCondition(got, "http", "Accepted").Status)
	gcGot, err := gwcs.GatewayV1().GatewayClasses().Get(t.Context(), "trickster", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, metav1.ConditionFalse,
		meta.FindStatusCondition(gcGot.Status.Conditions, "Accepted").Status)
}

// eventGate holds every Event create until its context ends, noting who held the Lease when it
// did, which is how the order of term end and Lease release is observed
type eventGate struct {
	cs        *kubefake.Clientset
	entered   chan struct{}
	calls     atomic.Int32
	cancelled atomic.Int32
	mtx       sync.Mutex
	holders   []string
}

type gatedEventsClientset struct {
	kubernetes.Interface
	gate *eventGate
}

func (g *gatedEventsClientset) CoreV1() typedcorev1.CoreV1Interface {
	return gatedEventsCore{g.Interface.CoreV1(), g.gate}
}

type gatedEventsCore struct {
	typedcorev1.CoreV1Interface
	gate *eventGate
}

func (c gatedEventsCore) Events(ns string) typedcorev1.EventInterface {
	return gatedEventsAPI{c.CoreV1Interface.Events(ns), c.gate}
}

type gatedEventsAPI struct {
	typedcorev1.EventInterface
	gate *eventGate
}

func (a gatedEventsAPI) Create(ctx context.Context, ev *corev1.Event, opts metav1.CreateOptions,
) (*corev1.Event, error) {
	a.gate.calls.Add(1)
	select {
	case a.gate.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	lease, err := a.gate.cs.CoordinationV1().Leases("trickster").Get(context.Background(),
		"trickster-gateway-controller", metav1.GetOptions{})
	holder := ""
	if err == nil && lease.Spec.HolderIdentity != nil {
		holder = *lease.Spec.HolderIdentity
	}
	a.gate.mtx.Lock()
	a.gate.holders = append(a.gate.holders, holder)
	a.gate.mtx.Unlock()
	a.gate.cancelled.Add(1)
	return nil, ctx.Err()
}

func (g *eventGate) holdersAtCancel() []string {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	return slices.Clone(g.holders)
}

func electionOptions(t *testing.T) *kubecfg.Options {
	t.Helper()
	o := options(t)
	o.LeaderElection.Enabled = new(true)
	o.LeaderElection.Namespace = "trickster"
	o.LeaderElection.LeaseDuration = timeconv.Duration(2 * time.Second)
	o.LeaderElection.RenewDeadline = timeconv.Duration(time.Second)
	o.LeaderElection.RetryPeriod = timeconv.Duration(100 * time.Millisecond)
	require.NoError(t, o.Validate())
	return o
}

func leaseHolder(t *testing.T, cs *kubefake.Clientset) string {
	t.Helper()
	lease, err := cs.CoordinationV1().Leases("trickster").Get(t.Context(),
		"trickster-gateway-controller", metav1.GetOptions{})
	if err != nil || lease.Spec.HolderIdentity == nil {
		return ""
	}
	return *lease.Spec.HolderIdentity
}

func TestStopCancelsEventsBeforeReleasingTheLease(t *testing.T) {
	// Stopping the controller cancels the term's Event calls before the Lease is released, so
	// a successor never finds the old term still speaking under its own
	cs := kubefake.NewClientset(ingressClass(), service(), bogusIngress())
	gate := &eventGate{cs: cs, entered: make(chan struct{}, 8)}
	c, err := New(Config{
		Options: electionOptions(t), Publisher: &publisher{}, Identity: "a",
		Client: kube.NewFromClientset(cs), GatewayClient: gwfake.NewSimpleClientset(),
		Recorder: events.New(&gatedEventsClientset{Interface: cs, gate: gate}),
	})
	require.NoError(t, err)
	require.NoError(t, c.Start(t.Context()))
	eventually(t, c.IsLeader, "an uncontended lease should be won")
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no Event was attempted")
	}
	c.Stop()
	require.EqualValues(t, 1, gate.cancelled.Load(), "the Event in flight was cancelled")
	require.Equal(t, []string{"a"}, gate.holdersAtCancel(),
		"the Lease was still held when the term's Event was cancelled")
	require.Equal(t, "", leaseHolder(t, cs), "the Lease is released once the term has drained")
	time.Sleep(200 * time.Millisecond)
	require.EqualValues(t, 1, gate.calls.Load(), "no Event call follows the term")
}

func TestLeadershipLossEndsTheEventTerm(t *testing.T) {
	// Losing the Lease ends the Event term the same way, and a new term begins publishing again
	cs := kubefake.NewClientset(ingressClass(), service(), bogusIngress())
	var blockRenewal atomic.Bool
	cs.PrependReactor("update", "leases", func(ktesting.Action) (bool, runtime.Object, error) {
		if blockRenewal.Load() {
			return true, nil, errors.New("api server unreachable")
		}
		return false, nil, nil
	})
	gate := &eventGate{cs: cs, entered: make(chan struct{}, 8)}
	c, err := New(Config{
		Options: electionOptions(t), Publisher: &publisher{}, Identity: "a",
		Client: kube.NewFromClientset(cs), GatewayClient: gwfake.NewSimpleClientset(),
		Recorder: events.New(&gatedEventsClientset{Interface: cs, gate: gate}),
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	eventually(t, c.IsLeader, "an uncontended lease should be won")
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no Event was attempted")
	}
	blockRenewal.Store(true)
	eventually(t, func() bool { return gate.cancelled.Load() == 1 },
		"losing the Lease should cancel the Event in flight")
	require.False(t, c.IsLeader())
	time.Sleep(200 * time.Millisecond)
	require.EqualValues(t, 1, gate.calls.Load(), "nothing is published between terms")

	blockRenewal.Store(false)
	eventually(t, c.IsLeader, "the lease should be regained")
	eventually(t, func() bool { return gate.calls.Load() == 2 },
		"the new term publishes the current problems again")
}

func TestCertificateVerdictsAreReused(t *testing.T) {
	// One Secret referenced by many listeners is parsed once, an unchanged pass parses nothing,
	// a rotation parses once more, and a dropped reference is forgotten
	gw := httpsGateway()
	for _, port := range []int32{8443, 9443} {
		gw.Spec.Listeners = append(gw.Spec.Listeners, gwapiv1.Listener{
			Name: gwapiv1.SectionName(fmt.Sprintf("https-%d", port)), Port: gwapiv1.PortNumber(port),
			Protocol: gwapiv1.HTTPSProtocolType,
			TLS: &gwapiv1.ListenerTLSConfig{CertificateRefs: []gwapiv1.SecretObjectReference{
				{Name: "shop-tls"}}},
		})
	}
	gwcs := gwfake.NewSimpleClientset(gatewayClass())
	require.NoError(t, gwcs.Tracker().Create(gatewayGVR, gw, gw.Namespace))
	cs := kubefake.NewClientset(service(), tlsSecret("cert-a"))
	sink := newCertSink("kgw--listener-https-443", "kgw--listener-https-8443",
		"kgw--listener-https-9443")
	c, err := New(Config{
		Options: options(t), Publisher: &publisher{}, Certs: sink,
		Client: kube.NewFromClientset(cs), GatewayClient: gwcs,
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))
	// passes are asked for through the worker: a pass run beside it would read the
	// Secret at another version and the two would take turns replacing the verdict
	awaitPass := func() {
		before := c.verdicts.passCount()
		c.Resync()
		eventually(t, func() bool { return c.verdicts.passCount() > before }, "no pass ran")
	}
	eventually(t, func() bool { return len(sink.snapshot()) == 3 }, "certificates not pushed")
	require.EqualValues(t, 1, c.verdicts.parseCount(), "three references, one parse")

	awaitPass()
	require.EqualValues(t, 1, c.verdicts.parseCount(), "an unchanged pass parses nothing")

	_, err = cs.CoreV1().Secrets("shop").Update(t.Context(), tlsSecret("cert-b"), metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool {
		return sink.snapshot()["kgw--listener-https-443|shop/shop-tls"] == "cert-b"
	}, "the rotation was not pushed")
	require.EqualValues(t, 2, c.verdicts.parseCount(), "a rotation parses once")

	_, err = cs.CoreV1().Secrets("shop").Update(t.Context(), garbageSecret(), metav1.UpdateOptions{})
	require.NoError(t, err)
	eventually(t, func() bool { return c.verdicts.parseCount() == 3 }, "bad material is parsed once")
	awaitPass()
	require.EqualValues(t, 3, c.verdicts.parseCount(), "a negative verdict is kept")
	require.Equal(t, 1, c.verdicts.size())
	require.Equal(t, "cert-b", sink.snapshot()["kgw--listener-https-443|shop/shop-tls"],
		"the certificate already serving stays")

	require.NoError(t, gwcs.Tracker().Delete(gatewayGVR, "shop", "gw"))
	eventually(t, func() bool { return c.verdicts.size() == 0 },
		"a verdict nothing references is swept")
}

func gatewayReport(n int) *ir.Report {
	rep := &ir.Report{}
	for i := range n {
		src := ir.Source{Kind: ir.KindGateway, Namespace: "shop", Name: fmt.Sprintf("gw-%d", i)}
		rep.Gateways = append(rep.Gateways, ir.GatewayStatus{Source: src, Listeners: []ir.ListenerStatus{
			{Name: "http", Conditions: []ir.Condition{{Type: "Programmed", Status: true}}},
			{Name: "https", Conditions: []ir.Condition{{Type: "Programmed", Status: true}}},
		}})
	}
	return rep
}

func httpsListeners(n int) *ir.IR {
	model := &ir.IR{}
	for i := range n {
		src := ir.Source{Kind: ir.KindGateway, Namespace: "shop", Name: fmt.Sprintf("gw-%d", i)}
		model.Listeners = append(model.Listeners, ir.Listener{
			Name: src.Key() + "/https", Section: "https", Port: 443, Protocol: ir.ProtocolHTTPS,
			CertRefs: []string{"shop/tls"}, Source: src,
		})
	}
	model.Certs = []ir.CertRef{{Name: "shop/tls", Namespace: "shop", SecretName: "tls"}}
	return model
}

func TestMarkProgrammedLowersOnlyTheAffectedListener(t *testing.T) {
	// Only the listener without a certificate is lowered, on its own Gateway,
	// with the others on every Gateway left as they were
	rep := gatewayReport(3)
	model := httpsListeners(3)
	installed := map[certKey]struct{}{
		{listener: "kgw--listener-https-443", source: "shop/tls"}: {},
	}
	// every listener shares the port, so every one is served by the installed certificate
	markProgrammed(model, rep, installed)
	for _, gw := range rep.Gateways {
		for _, l := range gw.Listeners {
			require.True(t, condOf(t, l.Conditions, "Programmed").Status)
		}
	}
	// nothing installed: the https listeners are lowered, the http ones untouched
	markProgrammed(model, rep, nil)
	for _, gw := range rep.Gateways {
		require.True(t, condOf(t, gw.Listeners[0].Conditions, "Programmed").Status)
		require.False(t, condOf(t, gw.Listeners[1].Conditions, "Programmed").Status)
	}
	// a listener the report does not describe is skipped
	model.Listeners[0].Section = "nope"
	markProgrammed(model, rep, nil)
	markProgrammed(model, nil, nil)
}

func condOf(t *testing.T, conds []ir.Condition, typ string) ir.Condition {
	t.Helper()
	c, ok := ir.Find(conds, typ)
	require.True(t, ok)
	return c
}

func BenchmarkMarkProgrammedBulkMissingCertificates(b *testing.B) {
	rep := gatewayReport(2000)
	model := httpsListeners(2000)
	b.ReportAllocs()
	for b.Loop() {
		markProgrammed(model, rep, nil)
	}
}

func TestStartAfterStopNeverContends(t *testing.T) {
	// the supervisor can retire a controller between adopting it and starting it; the late
	// Start must not launch an election nobody will ever stop
	cs := kubefake.NewClientset(ingressClass(), service(), webIngress())
	build := func(identity string) *Controller {
		c, err := New(Config{
			Options: electionOptions(t), Publisher: &publisher{}, Identity: identity,
			Client: kube.NewFromClientset(cs), GatewayClient: gwfake.NewSimpleClientset(),
		})
		require.NoError(t, err)
		return c
	}
	retired := build("retired")
	retired.Stop()
	require.NoError(t, retired.Start(t.Context()))
	time.Sleep(300 * time.Millisecond)
	require.False(t, retired.IsLeader())
	require.Equal(t, "", leaseHolder(t, cs), "a retired controller must not take the Lease")

	successor := build("successor")
	t.Cleanup(successor.Stop)
	require.NoError(t, successor.Start(t.Context()))
	eventually(t, successor.IsLeader, "the successor should lead")
	require.Equal(t, "successor", leaseHolder(t, cs))
	// the retired controller stays retired however often it is started
	require.NoError(t, retired.Start(t.Context()))
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, "successor", leaseHolder(t, cs))
}
