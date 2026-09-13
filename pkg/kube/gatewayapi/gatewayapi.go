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

// Package gatewayapi builds the typed sigs.k8s.io/gateway-api clientset
// over a shared kube.Client's connection. It is a separate package from
// pkg/kube so that only the Gateway controller pulls the gateway-api
// clientset in; the autodiscovery kubernetes provider does not.
package gatewayapi

import (
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/kube"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gwinformers "sigs.k8s.io/gateway-api/pkg/client/informers/externalversions"
)

// InformerFactory is a reference-counted handle to a shared gateway-api
// informer factory
type InformerFactory = kube.InformerFactory[gwinformers.SharedInformerFactory]

// NewClientset returns a typed gateway-api clientset over the connection the kube Client was built
// from; it errors for a Client wrapping a caller-supplied clientset, which carries no REST config
func NewClientset(c *kube.Client) (gwclient.Interface, error) {
	cfg := c.RESTConfig()
	if cfg == nil {
		return nil, kube.ErrNoRESTConfig
	}
	return gwclient.NewForConfig(cfg)
}

// GroupVersion is the Gateway API version this build reads
var GroupVersion = gwapiv1.GroupVersion.String()

// AlphaGroupVersion is the experimental Gateway API version the TCPRoute, TLSRoute and UDPRoute
// kinds are served under, installed only by the experimental channel
var AlphaGroupVersion = gwapiv1a2.GroupVersion.String()

// Available reports whether the cluster serves the Gateway API; an informer over a resource the
// API server does not serve never syncs, so building one would hang startup
func Available(c *kube.Client) (bool, error) {
	_, ok, err := Resources(c)
	return ok, err
}

// Resources returns the sorted resource names the cluster serves in this build's Gateway API group
// version, and whether it serves the group; a cluster may serve the group and still lack a newer kind
func Resources(c *kube.Client) ([]string, bool, error) {
	return ResourcesFor(c, GroupVersion)
}

// AlphaResources returns the sorted resource names the cluster serves in the experimental Gateway
// API group version, and whether it serves that version at all
func AlphaResources(c *kube.Client) ([]string, bool, error) {
	return ResourcesFor(c, AlphaGroupVersion)
}

// ResourcesFor returns the sorted resource names the cluster serves in one group version, and
// whether it serves the group version at all
func ResourcesFor(c *kube.Client, groupVersion string) ([]string, bool, error) {
	if c == nil || c.Clientset() == nil {
		return nil, false, kube.ErrNoConnectionOptions
	}
	list, err := c.Clientset().Discovery().
		ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		// the API server could not be asked; that is not the same as an answer
		return nil, false, err
	}
	names := make([]string, 0, len(list.APIResources))
	for _, r := range list.APIResources {
		if strings.Contains(r.Name, "/") {
			// a subresource such as gateways/status is not a kind
			continue
		}
		names = append(names, r.Name)
	}
	slices.Sort(names)
	return names, true, nil
}

// Informers returns a handle to the shared gateway-api informer factory for the spec over the
// client's connection, in the registry the core factories use; the caller owns one Release per call
func Informers(c *kube.Client, cs gwclient.Interface,
	spec kube.FactorySpec,
) *InformerFactory {
	return kube.GatewayInformerFactory(c, spec,
		func() gwinformers.SharedInformerFactory {
			opts := []gwinformers.SharedInformerOption{
				gwinformers.WithNamespace(spec.Namespace),
			}
			if spec.LabelSelector != "" || spec.FieldSelector != "" {
				opts = append(opts,
					gwinformers.WithTweakListOptions(spec.Tweak()))
			}
			return gwinformers.NewSharedInformerFactoryWithOptions(
				cs, spec.Resync, opts...)
		})
}
