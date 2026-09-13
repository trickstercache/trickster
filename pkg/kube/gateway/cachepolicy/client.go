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

package cachepolicy

import (
	"github.com/trickstercache/trickster/v2/pkg/kube"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"
)

// Served reports whether the cluster serves the resource; an informer over a resource the API
// server does not serve never syncs, so building one would hang startup
func Served(c *kube.Client) (bool, error) {
	if c == nil || c.Clientset() == nil {
		return false, kube.ErrNoConnectionOptions
	}
	list, err := c.Clientset().Discovery().ServerResourcesForGroupVersion(GroupVersion.String())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, r := range list.APIResources {
		if r.Name == Resource {
			return true, nil
		}
	}
	return false, nil
}

// NewDynamicClient returns a dynamic client over the connection the kube Client was built from,
// which is how a custom resource is read without a generated clientset
func NewDynamicClient(c *kube.Client) (dynamic.Interface, error) {
	cfg := c.RESTConfig()
	if cfg == nil {
		return nil, kube.ErrNoRESTConfig
	}
	return dynamic.NewForConfig(cfg)
}
