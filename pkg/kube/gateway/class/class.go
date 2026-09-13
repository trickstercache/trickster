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

// Package class decides which Kubernetes objects this controller owns.
//
// Claiming is the first gate in the controller and the strictest: an object
// this instance does not claim is not translated, not counted, and above
// all not statused. Writing status onto another controller's Gateway or
// Ingress is worse than ignoring it, because the owning controller and this
// one then fight over the same field.
package class

import (
	netv1 "k8s.io/api/networking/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// DefaultClassAnnotation marks an IngressClass as the one an Ingress with no
// spec.ingressClassName is served by
const DefaultClassAnnotation = "ingressclass.kubernetes.io/is-default-class"

// LegacyIngressClassAnnotation is the pre-IngressClass way of naming a controller,
// honored for Ingresses that still carry it
const LegacyIngressClassAnnotation = "kubernetes.io/ingress.class"

// Claimer answers ownership questions for one controller configuration
type Claimer struct {
	// controllerName is matched against GatewayClass spec.controllerName and
	// IngressClass spec.controller
	controllerName string
	// ingressClass, when set, narrows ownership to the IngressClass of that
	// name, so two instances can split one cluster's Ingresses
	ingressClass string
}

// New returns a Claimer for the configured controller and ingress class
func New(controllerName, ingressClass string) *Claimer {
	return &Claimer{controllerName: controllerName, ingressClass: ingressClass}
}

// ControllerName returns the controller name this Claimer matches
func (c *Claimer) ControllerName() string { return c.controllerName }

// GatewayClass reports whether a GatewayClass names this controller
func (c *Claimer) GatewayClass(gc *gwapiv1.GatewayClass) bool {
	if c == nil || gc == nil {
		return false
	}
	return string(gc.Spec.ControllerName) == c.controllerName
}

// Gateway reports whether a Gateway belongs to one of the claimed GatewayClasses; the caller
// supplies the claimed class names, since resolving them needs the GatewayClass cache
func (c *Claimer) Gateway(gw *gwapiv1.Gateway, claimed map[string]struct{}) bool {
	if c == nil || gw == nil {
		return false
	}
	_, ok := claimed[string(gw.Spec.GatewayClassName)]
	return ok
}

// IngressClass reports whether an IngressClass belongs to this controller; a configured
// ingress_class narrows the claim to that class alone, which is how two instances divide a cluster
func (c *Claimer) IngressClass(ic *netv1.IngressClass) bool {
	if c == nil || ic == nil {
		return false
	}
	if ic.Spec.Controller != c.controllerName {
		return false
	}
	return c.ingressClass == "" || ic.Name == c.ingressClass
}

// IsDefaultIngressClass reports whether an IngressClass is annotated as the
// cluster default
func IsDefaultIngressClass(ic *netv1.IngressClass) bool {
	return ic != nil && ic.Annotations[DefaultClassAnnotation] == "true"
}

// Ingress reports whether an Ingress belongs to this controller: one naming a class when that class is
// ours, one naming none only when one of ours is the cluster default, the legacy annotation last
func (c *Claimer) Ingress(ing *netv1.Ingress, claimed map[string]bool) bool {
	if c == nil || ing == nil {
		return false
	}
	if name := ing.Spec.IngressClassName; name != nil && *name != "" {
		_, ok := claimed[*name]
		return ok
	}
	if legacy := ing.Annotations[LegacyIngressClassAnnotation]; legacy != "" {
		// the legacy annotation names a class, not a controller; without a
		// configured ingress_class there is nothing to match it against
		return c.ingressClass != "" && legacy == c.ingressClass
	}
	for _, isDefault := range claimed {
		if isDefault {
			return true
		}
	}
	return false
}

// ClaimedGatewayClasses returns the names of the GatewayClasses this
// controller owns, for use with Gateway
func (c *Claimer) ClaimedGatewayClasses(classes []*gwapiv1.GatewayClass) map[string]struct{} {
	out := make(map[string]struct{})
	for _, gc := range classes {
		if c.GatewayClass(gc) {
			out[gc.Name] = struct{}{}
		}
	}
	return out
}

// ClaimedIngressClasses returns the names of the IngressClasses this controller owns, mapped to
// whether each is the cluster default, for use with Ingress
func (c *Claimer) ClaimedIngressClasses(classes []*netv1.IngressClass) map[string]bool {
	out := make(map[string]bool)
	for _, ic := range classes {
		if c.IngressClass(ic) {
			out[ic.Name] = IsDefaultIngressClass(ic)
		}
	}
	return out
}
