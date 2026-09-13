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

package class

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"

	"github.com/stretchr/testify/require"
	netv1 "k8s.io/api/networking/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const ours = appinfo.Domain + "/gateway-controller"

func gatewayClass(name, controller string) *gwapiv1.GatewayClass {
	return &gwapiv1.GatewayClass{
		Name: name,
		Spec: gwapiv1.GatewayClassSpec{
			ControllerName: gwapiv1.GatewayController(controller),
		},
	}
}

func ingressClass(name, controller string, isDefault bool) *netv1.IngressClass {
	ic := &netv1.IngressClass{
		Name: name,
		Spec: netv1.IngressClassSpec{Controller: controller},
	}
	if isDefault {
		ic.Annotations = map[string]string{DefaultClassAnnotation: "true"}
	}
	return ic
}

func ingress(name string, className *string, annotations map[string]string) *netv1.Ingress {
	return &netv1.Ingress{
		Name: name, Namespace: "shop", Annotations: annotations,
		Spec: netv1.IngressSpec{IngressClassName: className},
	}
}

// A GatewayClass belongs to whichever controller it names
func TestGatewayClass(t *testing.T) {
	c := New(ours, "")
	require.True(t, c.GatewayClass(gatewayClass("trickster", ours)))
	require.False(t, c.GatewayClass(gatewayClass("acme", "example.com/acme-controller")))
	require.False(t, c.GatewayClass(gatewayClass("blank", "")))
	require.False(t, c.GatewayClass(nil))
	require.False(t, (*Claimer)(nil).GatewayClass(gatewayClass("t", ours)))
	require.Equal(t, ours, c.ControllerName())
}

func TestGateway(t *testing.T) {
	c := New(ours, "")
	claimed := c.ClaimedGatewayClasses([]*gwapiv1.GatewayClass{
		gatewayClass("trickster", ours),
		gatewayClass("acme", "example.com/acme-controller"),
	})
	require.Len(t, claimed, 1)

	gw := func(className string) *gwapiv1.Gateway {
		return &gwapiv1.Gateway{
			Name: "gw", Namespace: "infra",
			Spec: gwapiv1.GatewaySpec{
				GatewayClassName: gwapiv1.ObjectName(className),
			},
		}
	}
	require.True(t, c.Gateway(gw("trickster"), claimed))
	require.False(t, c.Gateway(gw("acme"), claimed),
		"another controller's Gateway must be left alone entirely")
	require.False(t, c.Gateway(gw("absent"), claimed))
	require.False(t, c.Gateway(nil, claimed))
	require.False(t, (*Claimer)(nil).Gateway(gw("trickster"), claimed))
}

// An IngressClass is ours when it names us; a configured ingress_class
// narrows that further, which is how two instances split one cluster
func TestIngressClass(t *testing.T) {
	c := New(ours, "")
	require.True(t, c.IngressClass(ingressClass("trickster", ours, false)))
	require.True(t, c.IngressClass(ingressClass("other-trickster", ours, false)))
	require.False(t, c.IngressClass(ingressClass("acme", "example.com/acme-controller", false)))
	require.False(t, c.IngressClass(nil))
	require.False(t, (*Claimer)(nil).IngressClass(ingressClass("t", ours, false)))

	narrowed := New(ours, "trickster")
	require.True(t, narrowed.IngressClass(ingressClass("trickster", ours, false)))
	require.False(t, narrowed.IngressClass(ingressClass("other-trickster", ours, false)),
		"a configured ingress_class excludes our own other classes")

	require.True(t, IsDefaultIngressClass(ingressClass("t", ours, true)))
	require.False(t, IsDefaultIngressClass(ingressClass("t", ours, false)))
	require.False(t, IsDefaultIngressClass(nil))
}

// An Ingress naming a class is ours only if that class is
func TestIngressByClassName(t *testing.T) {
	c := New(ours, "")
	claimed := c.ClaimedIngressClasses([]*netv1.IngressClass{
		ingressClass("trickster", ours, false),
		ingressClass("acme", "example.com/acme-controller", true),
	})
	require.Equal(t, map[string]bool{"trickster": false}, claimed)

	name := "trickster"
	require.True(t, c.Ingress(ingress("a", &name, nil), claimed))
	other := "acme"
	require.False(t, c.Ingress(ingress("b", &other, nil), claimed))
	absent := "gone"
	require.False(t, c.Ingress(ingress("c", &absent, nil), claimed))
	require.False(t, c.Ingress(nil, claimed))
	require.False(t, (*Claimer)(nil).Ingress(ingress("a", &name, nil), claimed))
}

// An unclassed Ingress is ours only when one of our classes is the cluster
// default; where another controller holds the default, we must not take it
func TestIngressWithoutClassName(t *testing.T) {
	c := New(ours, "")
	unclassed := ingress("a", nil, nil)
	empty := ""
	emptyClass := ingress("b", &empty, nil)

	notDefault := c.ClaimedIngressClasses([]*netv1.IngressClass{
		ingressClass("trickster", ours, false),
	})
	require.False(t, c.Ingress(unclassed, notDefault))
	require.False(t, c.Ingress(emptyClass, notDefault))

	isDefault := c.ClaimedIngressClasses([]*netv1.IngressClass{
		ingressClass("trickster", ours, true),
	})
	require.True(t, c.Ingress(unclassed, isDefault))
	require.True(t, c.Ingress(emptyClass, isDefault),
		"an empty class name is no class name")

	// another controller owns the default
	theirs := c.ClaimedIngressClasses([]*netv1.IngressClass{
		ingressClass("acme", "example.com/acme-controller", true),
	})
	require.False(t, c.Ingress(unclassed, theirs))
}

// The legacy annotation names a class the way Ingresses did before
// spec.ingressClassName existed
func TestIngressLegacyAnnotation(t *testing.T) {
	c := New(ours, "trickster")
	claimed := c.ClaimedIngressClasses([]*netv1.IngressClass{
		ingressClass("trickster", ours, true),
	})

	legacy := ingress("a", nil,
		map[string]string{LegacyIngressClassAnnotation: "trickster"})
	require.True(t, c.Ingress(legacy, claimed))

	foreign := ingress("b", nil,
		map[string]string{LegacyIngressClassAnnotation: "acme"})
	require.False(t, c.Ingress(foreign, claimed),
		"the annotation naming another class must lose to it, not to our default")

	// spec.ingressClassName wins over the annotation
	name := "trickster"
	both := ingress("c", &name,
		map[string]string{LegacyIngressClassAnnotation: "acme"})
	require.True(t, c.Ingress(both, claimed))

	// without a configured ingress_class there is nothing to match against
	unconfigured := New(ours, "")
	require.False(t, unconfigured.Ingress(legacy, claimed))
}
