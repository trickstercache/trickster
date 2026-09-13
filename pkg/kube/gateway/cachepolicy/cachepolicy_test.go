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
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// crdPath is the manifest the cluster is given, which the Go types must agree with
const crdPath = "../../../../deploy/kube/crds/trickstercachepolicies.yaml"

func policy(name string, age int, refs ...TargetRef) *CachePolicy {
	return &CachePolicy{
		Namespace: "shop", Name: name, Generation: 1, UID: types.UID("uid-" + name),
		CreationTimestamp: metav1.NewTime(time.Unix(int64(age), 0)),
		Spec:              Spec{TargetRefs: refs},
	}
}

func ref(kind, name string) TargetRef {
	return TargetRef{Kind: kind, Name: name}
}

func known() ir.ConfiguredNames {
	return ir.ConfiguredNames{
		Caches:         sets.New([]string{"objects"}),
		NegativeCaches: sets.New([]string{"api-errors"}),
	}
}

func acceptedReason(t *testing.T, s ir.AncestorStatus) (bool, string) {
	t.Helper()
	c, ok := ir.Find(s.Conditions, string(gwapiv1.PolicyConditionAccepted))
	require.True(t, ok)
	return c.Status, c.Reason
}

func TestUnstructuredRoundTrip(t *testing.T) {
	// what the dynamic client delivers and what is written back are the same object
	p := policy("p", 1, TargetRef{
		Group: gwapiv1.GroupName, Kind: KindHTTPRoute, Name: "web",
		SectionName: "api",
	})
	p.Spec.Provider = "prometheus"
	p.Spec.CacheKeyParams = []string{"query"}
	p.Spec.CacheKeyHeaders = []string{}
	p.Spec.RequestHeaders = map[string]string{"X-A": "1"}
	p.Spec.CORS = &CORS{Mode: "merge", Headers: map[string]string{"Access-Control-Allow-Origin": "*"}}
	p.Status.Ancestors = []gwapiv1.PolicyAncestorStatus{{
		AncestorRef: gwapiv1.ParentReference{Name: "web"}, ControllerName: "c",
		Conditions: []metav1.Condition{{
			Type: "Accepted", Status: metav1.ConditionTrue,
			Reason: "Accepted", LastTransitionTime: metav1.NewTime(time.Unix(0, 0).UTC()),
		}},
	}}
	u, err := ToUnstructured(p)
	require.NoError(t, err)
	require.Equal(t, GroupVersion.String(), u.GetAPIVersion())
	require.Equal(t, Kind, u.GetKind())
	back, err := FromUnstructured(u)
	require.NoError(t, err)
	require.Equal(t, p.Spec, back.Spec)
	require.NotNil(t, back.Spec.CacheKeyHeaders, "an explicitly empty list survives the round trip")
	require.True(t, equality.Semantic.DeepEqual(p.Status, back.Status), "status %+v", back.Status)
	require.Equal(t, "shop", back.Namespace)

	_, err = FromUnstructured(&unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"targetRefs": "not a list"},
	}})
	require.Error(t, err)
}

func TestDeepCopy(t *testing.T) {
	p := policy("p", 1, ref(KindService, "svc"))
	p.Spec.CacheKeyHeaders = []string{"X-T"}
	p.Spec.ResponseHeaders = map[string]string{"X-R": "1"}
	p.Spec.CORS = &CORS{Headers: map[string]string{"A": "b"}}
	c := p.DeepCopy()
	require.Equal(t, p, c)
	c.Spec.TargetRefs[0].Name = "x"
	c.Spec.CacheKeyHeaders[0] = "x"
	c.Spec.ResponseHeaders["X-R"] = "x"
	c.Spec.CORS.Headers["A"] = "x"
	require.Equal(t, "svc", p.Spec.TargetRefs[0].Name)
	require.Equal(t, "X-T", p.Spec.CacheKeyHeaders[0])
	require.Equal(t, "1", p.Spec.ResponseHeaders["X-R"])
	require.Equal(t, "b", p.Spec.CORS.Headers["A"])
	require.NotNil(t, p.DeepCopyObject())
	var none *CachePolicy
	require.Nil(t, none.DeepCopy())
}

func TestServedAndClient(t *testing.T) {
	cs := kubefake.NewClientset()
	ok, err := Served(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.False(t, ok, "a cluster without the CRD does not serve it")
	cs.Resources = []*metav1.APIResourceList{{
		GroupVersion: GroupVersion.String(),
		APIResources: []metav1.APIResource{{Name: "other"}},
	}}
	ok, err = Served(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.False(t, ok, "the group may be served without the resource")
	cs.Resources[0].APIResources = append(cs.Resources[0].APIResources,
		metav1.APIResource{Name: Resource})
	ok, err = Served(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.True(t, ok)
	_, err = Served(nil)
	require.ErrorIs(t, err, kube.ErrNoConnectionOptions)

	c, err := kube.NewFromRESTConfig(&rest.Config{Host: "https://127.0.0.1:6443"}, nil)
	require.NoError(t, err)
	dyn, err := NewDynamicClient(c)
	require.NoError(t, err)
	require.NotNil(t, dyn)
	_, err = NewDynamicClient(kube.NewFromClientset(cs))
	require.ErrorIs(t, err, kube.ErrNoRESTConfig)
}

func TestIndexLowersEveryField(t *testing.T) {
	p := policy("full", 1, ref(KindHTTPRoute, "web"))
	p.Spec = Spec{
		TargetRefs:          p.Spec.TargetRefs,
		Handler:             "proxycache",
		Provider:            "prometheus",
		CacheName:           "objects",
		NegativeCacheName:   "api-errors",
		MaxTTL:              "10m",
		Timeout:             "15s",
		CollapsedForwarding: "progressive",
		CacheKeyParams:      []string{"query", "step"},
		CacheKeyHeaders:     []string{"X-Tenant"},
		RequestHeaders:      map[string]string{"X-Forwarded-Host": "shop.example.com", "-X-Internal": ""},
		ResponseHeaders:     map[string]string{"+Vary": "Accept-Encoding"},
		CORS:                &CORS{Mode: "merge", Headers: map[string]string{"Access-Control-Allow-Origin": "*"}},
		HealthMode:          "probe",
		ResultHeader:        "Hide",
	}
	x := New([]*CachePolicy{p}, Config{Known: known()})
	require.Empty(t, x.Problems())
	got, ok := x.Lookup(KindHTTPRoute, "shop", "web", "")
	require.True(t, ok)
	// an explicitly empty list is lowered as empty, an omitted one as nil
	empty := policy("empty", 2, ref(KindService, "svc"))
	empty.Spec.CacheKeyParams = []string{}
	ex := New([]*CachePolicy{empty}, Config{})
	cleared, ok := ex.Lookup(KindService, "shop", "svc", "")
	require.True(t, ok)
	require.NotNil(t, cleared.CacheKeyParams)
	require.Empty(t, cleared.CacheKeyParams)
	require.Nil(t, cleared.CacheKeyHeaders)
	require.Equal(t, ir.Policy{
		Name: "TricksterCachePolicy/shop/full", Source: got.Source,
		Handler: ir.HandlerProxyCache, Provider: "prometheus", CacheName: "objects",
		NegativeCacheName: "api-errors", MaxTTLMS: 600000, TimeoutMS: 15000,
		CollapsedForwarding: "progressive", CacheKeyParams: []string{"query", "step"},
		CacheKeyHeaders: []string{"X-Tenant"},
		RequestHeaders:  map[string]string{"X-Forwarded-Host": "shop.example.com", "-X-Internal": ""},
		ResponseHeaders: map[string]string{"+Vary": "Accept-Encoding"},
		CORSMode:        "merge", CORSHeaders: map[string]string{"Access-Control-Allow-Origin": "*"},
		HealthMode: "probe", ResultHeader: ir.ResultHeaderHide,
	}, *got)
	require.Equal(t, ir.KindCachePolicy, got.Source.Kind)
	require.Equal(t, "uid-full", got.Source.UID)
	// a lookup returns a copy
	got.CacheKeyParams[0] = "changed"
	again, _ := x.Lookup(KindHTTPRoute, "shop", "web", "")
	require.Equal(t, "query", again.CacheKeyParams[0])

	report := x.Report()
	require.Len(t, report, 1)
	require.Len(t, report[0].Ancestors, 1)
	status, reason := acceptedReason(t, report[0].Ancestors[0])
	require.True(t, status)
	require.Equal(t, string(gwapiv1.PolicyReasonAccepted), reason)
	require.Equal(t, ir.ParentRef{Kind: KindHTTPRoute, Namespace: "shop", Name: "web"},
		report[0].Ancestors[0].Ref)
}

func TestIndexRefusesAnInvalidSpecWhole(t *testing.T) {
	// nothing of an invalid policy applies, and every target says why
	cases := map[string]func(*Spec){
		"handler":             func(s *Spec) { s.Handler = "cache" },
		"provider":            func(s *Spec) { s.Provider = "rpc" },
		"cacheName":           func(s *Spec) { s.CacheName = "absent" },
		"negativeCacheName":   func(s *Spec) { s.NegativeCacheName = "absent" },
		"maxTTL":              func(s *Spec) { s.MaxTTL = "600" },
		"timeout":             func(s *Spec) { s.Timeout = "-1s" },
		"collapsedForwarding": func(s *Spec) { s.CollapsedForwarding = "sometimes" },
		"cacheKeyParams":      func(s *Spec) { s.CacheKeyParams = []string{"a b"} },
		"cacheKeyHeaders":     func(s *Spec) { s.CacheKeyHeaders = []string{"bad header"} },
		"requestHeaders":      func(s *Spec) { s.RequestHeaders = map[string]string{"bad name": "1"} },
		"responseHeaders":     func(s *Spec) { s.ResponseHeaders = map[string]string{"X": "bad\x00"} },
		"cors.mode":           func(s *Spec) { s.CORS = &CORS{Mode: "sometimes"} },
		"cors.headers":        func(s *Spec) { s.CORS = &CORS{Headers: map[string]string{"bad name": "1"}} },
		"healthMode":          func(s *Spec) { s.HealthMode = "guess" },
		"resultHeader":        func(s *Spec) { s.ResultHeader = "Maybe" },
	}
	for field, mutate := range cases {
		t.Run(field, func(t *testing.T) {
			p := policy("bad", 1, ref(KindHTTPRoute, "web"), ref(KindService, "svc"))
			p.Spec.CacheName = "objects"
			mutate(&p.Spec)
			x := New([]*CachePolicy{p}, Config{Known: known()})
			_, ok := x.Lookup(KindHTTPRoute, "shop", "web", "")
			require.False(t, ok, "an invalid policy governs nothing")
			_, ok = x.Lookup(KindService, "shop", "svc", "")
			require.False(t, ok)
			problems := x.Problems()
			require.Len(t, problems, 1)
			require.Equal(t, string(gwapiv1.PolicyReasonInvalid), problems[0].Reason)
			require.Contains(t, problems[0].Detail, "spec."+field)
			for _, a := range x.Report()[0].Ancestors {
				status, reason := acceptedReason(t, a)
				require.False(t, status)
				require.Equal(t, string(gwapiv1.PolicyReasonInvalid), reason)
			}
		})
	}
}

func TestIndexTargets(t *testing.T) {
	exists := func(kind, ns, name string) bool {
		return ns == "shop" && name != "absent"
	}
	older := policy("older", 1,
		ref(KindGateway, "gw"),
		ref(KindHTTPRoute, "web"),
		TargetRef{Kind: KindHTTPRoute, Name: "web", SectionName: "api"},
		TargetRef{Kind: KindService, Name: "svc", SectionName: "http"},
		ref(KindIngress, "site"),
		ref(KindService, "absent"),
		TargetRef{Group: "apps", Kind: KindService, Name: "svc"},
		TargetRef{Kind: "Deployment", Name: "d"},
		TargetRef{Kind: KindGateway, Name: "gw", SectionName: "http"},
		TargetRef{Kind: KindIngress, Name: "site", SectionName: "rule"},
		TargetRef{Kind: KindService},
		ref(KindIngress, "site"),
	)
	newer := policy("newer", 2, ref(KindHTTPRoute, "web"), ref(KindService, "svc"))
	x := New([]*CachePolicy{newer, older}, Config{Known: known(), Exists: exists})

	for _, tc := range []struct {
		kind, name, section, owner string
	}{
		{KindGateway, "gw", "", "older"},
		{KindHTTPRoute, "web", "", "older"},
		{KindHTTPRoute, "web", "api", "older"},
		{KindService, "svc", "http", "older"},
		{KindService, "svc", "", "newer"},
		{KindIngress, "site", "", "older"},
	} {
		p, ok := x.Lookup(tc.kind, "shop", tc.name, tc.section)
		require.True(t, ok, "%s %s %q", tc.kind, tc.name, tc.section)
		require.Equal(t, tc.owner, p.Source.Name, "%s %s %q", tc.kind, tc.name, tc.section)
	}
	_, ok := x.Lookup(KindHTTPRoute, "shop", "web", "other")
	require.False(t, ok, "a section is looked up under itself only")
	_, ok = x.Lookup(KindHTTPRoute, "other", "web", "")
	require.False(t, ok)
	var none *Index
	_, ok = none.Lookup(KindHTTPRoute, "shop", "web", "")
	require.False(t, ok)
	require.Nil(t, none.Problems())
	require.Nil(t, none.Report())

	report := x.Report()
	require.Len(t, report, 2)
	require.Equal(t, "older", report[0].Source.Name, "the report is in age order")
	reasons := make([]string, 0, len(report[0].Ancestors))
	for _, a := range report[0].Ancestors {
		_, reason := acceptedReason(t, a)
		reasons = append(reasons, reason)
	}
	require.Equal(t, []string{
		"Accepted", "Accepted", "Accepted", "Accepted", "Accepted",
		"TargetNotFound", "Invalid", "Invalid", "Invalid", "Invalid", "Invalid", "Invalid",
	}, reasons)
	_, r := acceptedReason(t, report[1].Ancestors[0])
	require.Equal(t, string(gwapiv1.PolicyReasonConflicted), r)
	_, r = acceptedReason(t, report[1].Ancestors[1])
	require.Equal(t, string(gwapiv1.PolicyReasonAccepted), r)

	details := make([]string, 0)
	for _, p := range x.Problems() {
		details = append(details, p.Reason+": "+p.Detail)
	}
	require.Len(t, details, 8)
	require.Contains(t, details, "TargetNotFound: targetRef Service absent: targetRef is not "+
		"found in a watched namespace")
	require.Contains(t, details, "Conflicted: targetRef HTTPRoute web: targetRef is already "+
		"governed by the older policy TricksterCachePolicy/shop/older")
	joined := strings.Join(details, "\n")
	require.Contains(t, joined, "group does not match")
	require.Contains(t, joined, "kind is not supported")
	require.Contains(t, joined, "sectionName is not supported")
	require.Contains(t, joined, "names no object")
	require.Contains(t, joined, "named more than once")

	// a policy with no targets is told so, and the empty-name default of Exists finds all
	empty := policy("empty", 3)
	x = New([]*CachePolicy{empty}, Config{})
	require.Len(t, x.Problems(), 1)
	require.Contains(t, x.Problems()[0].Detail, "names no target")
	require.Empty(t, x.Report()[0].Ancestors)
}

func TestIndexProviderConflict(t *testing.T) {
	paths := func(provider string) []string {
		if provider == "prometheus" {
			return []string{"/", "/api/v1/query_range", "/api/v1/label/"}
		}
		return nil
	}
	x := New(nil, Config{ProviderPaths: paths})
	p := &ir.Policy{Provider: "prometheus"}
	matches := func(values ...string) []ir.Match {
		out := make([]ir.Match, 0, len(values))
		for _, v := range values {
			out = append(out, ir.Match{Path: ir.PathMatch{Type: ir.PathPrefix, Value: v}})
		}
		return out
	}
	path, ok := x.ProviderConflict(p, matches("/", "/api", "/api/v1/query_range"))
	require.True(t, ok)
	require.Equal(t, "/api/v1/query_range", path)
	_, ok = x.ProviderConflict(p, matches("/", "/api"))
	require.False(t, ok, "the root and a covering prefix are not conflicts")
	_, ok = x.ProviderConflict(&ir.Policy{Provider: "graphite"}, matches("/api/v1/query_range"))
	require.False(t, ok)
	_, ok = x.ProviderConflict(&ir.Policy{}, matches("/api/v1/query_range"))
	require.False(t, ok)
	_, ok = New(nil, Config{}).ProviderConflict(p, matches("/api/v1/query_range"))
	require.False(t, ok, "with no path source nothing conflicts")
	var none *Index
	_, ok = none.ProviderConflict(p, nil)
	require.False(t, ok)
}

// crdSchema is the part of the manifest the Go types are checked against
type crdSchema struct {
	Spec struct {
		Group string `yaml:"group"`
		Names struct {
			Kind   string `yaml:"kind"`
			Plural string `yaml:"plural"`
		} `yaml:"names"`
		Versions []struct {
			Name         string `yaml:"name"`
			Subresources struct {
				Status *struct{} `yaml:"status"`
			} `yaml:"subresources"`
			Schema struct {
				OpenAPI struct {
					Properties struct {
						Spec struct {
							Properties map[string]struct {
								Properties map[string]any `yaml:"properties"`
								Items      *struct {
									Properties map[string]any `yaml:"properties"`
								} `yaml:"items"`
							} `yaml:"properties"`
						} `yaml:"spec"`
					} `yaml:"properties"`
				} `yaml:"openAPIV3Schema"`
			} `yaml:"schema"`
		} `yaml:"versions"`
	} `yaml:"spec"`
}

func TestManifestAgreesWithTheTypes(t *testing.T) {
	// the cluster validates against the manifest and the controller reads with the Go types, so
	// every field of one must be a field of the other
	data, err := os.ReadFile(filepath.Clean(crdPath))
	require.NoError(t, err)
	var crd crdSchema
	require.NoError(t, yaml.Unmarshal(data, &crd))
	require.Equal(t, Group, crd.Spec.Group)
	require.Equal(t, Kind, crd.Spec.Names.Kind)
	require.Equal(t, Resource, crd.Spec.Names.Plural)
	require.Len(t, crd.Spec.Versions, 1)
	v := crd.Spec.Versions[0]
	require.Equal(t, Version, v.Name)
	require.NotNil(t, v.Subresources.Status, "status is written through the subresource")

	specProps := v.Schema.OpenAPI.Properties.Spec.Properties
	require.Equal(t, jsonFields(reflect.TypeFor[Spec]()), sortedKeys(specProps))
	require.Equal(t, jsonFields(reflect.TypeFor[TargetRef]()),
		sortedKeys(specProps["targetRefs"].Items.Properties))
	require.Equal(t, jsonFields(reflect.TypeFor[CORS]()), sortedKeys(specProps["cors"].Properties))
}

func jsonFields(typ reflect.Type) []string {
	out := make([]string, 0, typ.NumField())
	for field := range typ.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
