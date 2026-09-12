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

package gateway

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// The lowering helpers refuse an absent body and a hostname the Location or
// Host header could not carry
func TestLowerFilterEdges(t *testing.T) {
	_, err := lowerRedirect(nil, true)
	require.ErrorIs(t, err, errFilterBody)
	_, err = lowerURLRewrite(nil, true, true)
	require.ErrorIs(t, err, errFilterBody)
	_, err = lowerHeaderFilter(nil)
	require.ErrorIs(t, err, errFilterBody)

	wild := gwapiv1.PreciseHostname("*.internal")
	_, err = lowerURLRewrite(&gwapiv1.HTTPURLRewriteFilter{Hostname: &wild}, true, true)
	require.ErrorIs(t, err, translate.ErrWildcardHostname)
	empty := gwapiv1.PreciseHostname("  ")
	_, err = lowerRedirect(&gwapiv1.HTTPRequestRedirectFilter{Hostname: &empty}, true)
	require.ErrorIs(t, err, translate.ErrEmptyHostname)

	_, err = lowerHeaderFilter(&gwapiv1.HTTPHeaderFilter{
		Add: []gwapiv1.HTTPHeader{{Name: "X-A", Value: "bad\x00value"}}})
	require.ErrorIs(t, err, errHeaderValue)

	// a redirect with nothing set still redirects, to the request itself
	r, err := lowerRedirect(&gwapiv1.HTTPRequestRedirectFilter{}, true)
	require.NoError(t, err)
	require.Equal(t, &ir.RedirectFilter{}, r)

	// an empty scheme or hostname is an unset one
	scheme := ""
	r, err = lowerRedirect(&gwapiv1.HTTPRequestRedirectFilter{Scheme: &scheme, Hostname: &empty}, true)
	require.ErrorIs(t, err, translate.ErrEmptyHostname)
	require.Nil(t, r)

	require.True(t, singlePrefixMatch(nil), "no match is a prefix match on the root")
	require.False(t, singlePrefixMatch([]gwapiv1.HTTPRouteMatch{{}, {}}))
	require.False(t, rewritesPath(nil))
}

// A policy's lowering is memoized, so several references to one Service
// resolve it once, whether it was honored or refused
func TestBackendTLSIsMemoized(t *testing.T) {
	tr := &translator{}
	tls, reason, governed := tr.backendTLS("shop", "svc", "")
	require.Nil(t, tls)
	require.Empty(t, reason)
	require.False(t, governed, "no index means no policy governs anything")

	p := &gwapiv1.BackendTLSPolicy{}
	p.Namespace, p.Name = "shop", "tls"
	p.Spec.Validation.Hostname = "*.bad"
	tr.tlsPolicies = &backendTLSIndex{
		byTarget: map[string]*gwapiv1.BackendTLSPolicy{targetKey("shop", "svc", ""): p},
		lowered:  map[string]*ir.BackendTLS{},
		failed:   map[string]string{},
	}
	_, reason, governed = tr.backendTLS("shop", "svc", "https")
	require.True(t, governed)
	require.Contains(t, reason, "hostname must be precise")
	_, again, _ := tr.backendTLS("shop", "svc", "")
	require.Equal(t, reason, again, "the failure is remembered rather than re-derived")
	require.Empty(t, tr.problems.List(), "a refused validation is the reference's problem, not the policy's")

	wk := gwapiv1.WellKnownCACertificatesType("System")
	good := &gwapiv1.BackendTLSPolicy{}
	good.Namespace, good.Name = "shop", "ok"
	good.Spec.Validation.Hostname = "svc.internal"
	good.Spec.Validation.WellKnownCACertificates = &wk
	tr.tlsPolicies.byTarget[targetKey("shop", "other", "")] = good
	tls, reason, governed = tr.backendTLS("shop", "other", "")
	require.True(t, governed)
	require.Empty(t, reason)
	require.True(t, tls.System)
	tls2, _, _ := tr.backendTLS("shop", "other", "")
	require.Same(t, tls, tls2)
}
