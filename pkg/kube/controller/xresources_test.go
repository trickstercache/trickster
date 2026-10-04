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
	"errors"
	"testing"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/gatewayapi"

	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestXWatchableKinds(t *testing.T) {
	// a served experimental kind is watched only where this identity may list and watch it, since
	// an informer the API server refuses would hang startup; other kinds of the group are not read
	cs := kubefake.NewClientset()
	cs.Resources = []*metav1.APIResourceList{{
		GroupVersion: gatewayapi.XGroupVersion,
		APIResources: []metav1.APIResource{{Name: gatewayapi.ResourceBackendTrafficPolicies}, {Name: "xmeshes"}},
	}}
	allowed := true
	var failure error
	cs.PrependReactor("create", "selfsubjectaccessreviews",
		func(a k8stesting.Action) (bool, runtime.Object, error) {
			r := a.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			r.Status.Allowed = allowed
			return true, r, failure
		})
	c := &Controller{cfg: Config{Client: kube.NewFromClientset(cs), Options: kubecfg.New()}}
	got, err := c.xWatchable()
	require.NoError(t, err)
	require.Equal(t, []string{gatewayapi.ResourceBackendTrafficPolicies}, got)
	allowed = false
	got, err = c.xWatchable()
	require.NoError(t, err)
	require.Empty(t, got)
	failure = errors.New("review failed")
	got, err = c.xWatchable()
	require.NoError(t, err, "a review that cannot be made leaves the kind unread, not the controller unbuilt")
	require.Empty(t, got)

	_, err = (&Controller{cfg: Config{Client: kube.NewFromClientset(nil)}}).xWatchable()
	require.Error(t, err)
}
