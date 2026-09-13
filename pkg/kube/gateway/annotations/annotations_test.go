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

package annotations

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
)

func TestParseFullSet(t *testing.T) {
	set, problems := Parse(map[string]string{
		"kubernetes.io/ingress.class": "trickster",
		Handler:                       ir.HandlerProxyCache,
		CacheName:                     "objects",
		MaxTTL:                        "10m",
		Timeout:                       "15s",
		NegativeCacheName:             "api-errors",
		CollapsedForwarding:           "progressive",
		CORSMode:                      "merge",
		CORSHeaders:                   "Access-Control-Allow-Origin: https://x.example.com",
		RequestHeaders:                "X-A: 1\n\n-X-B:\n",
		ResponseHeaders:               "+Vary: Accept-Encoding",
		UseRegex:                      "true",
		RewriteTarget:                 "/v2/${1}",
		HealthMode:                    "probe",
	})
	require.Empty(t, problems)
	require.True(t, set.UseRegex)
	require.True(t, set.ConfiguresPolicy())
	p := set.Policy
	require.Equal(t, ir.HandlerProxyCache, p.Handler)
	require.Equal(t, "objects", p.CacheName)
	require.Equal(t, int64(600000), p.MaxTTLMS)
	require.Equal(t, int64(15000), p.TimeoutMS)
	require.Equal(t, "api-errors", p.NegativeCacheName)
	require.Equal(t, "progressive", p.CollapsedForwarding)
	require.Equal(t, "merge", p.CORSMode)
	require.Equal(t, map[string]string{
		"Access-Control-Allow-Origin": "https://x.example.com",
	}, p.CORSHeaders)
	require.Equal(t, map[string]string{"X-A": "1", "-X-B": ""}, p.RequestHeaders,
		"a blank line between entries is not an entry")
	require.Equal(t, map[string]string{"+Vary": "Accept-Encoding"},
		p.ResponseHeaders)
	require.Equal(t, map[string]string{"+Vary": "Accept-Encoding"}, p.ResponseHeaders)
	require.Equal(t, "/v2/${1}", p.RewriteTarget)
	require.Equal(t, "probe", p.HealthMode)
}

// An annotation outside this controller's namespace is another
// controller's, or nobody's, and is passed over in silence
func TestParseIgnoresForeignAnnotations(t *testing.T) {
	set, problems := Parse(map[string]string{
		"acme.example.com/rewrite-target": "/",
		"example.com/anything":            "",
	})
	require.Empty(t, problems)
	require.False(t, set.ConfiguresPolicy())
}

func TestParseEmpty(t *testing.T) {
	set, problems := Parse(nil)
	require.Empty(t, problems)
	require.False(t, set.ConfiguresPolicy())
	var nilSet *Set
	require.False(t, nilSet.ConfiguresPolicy())
}

// A rejected annotation is not applied, and everything else on the object
// still is: one typo must not delete a live route
func TestParseRejections(t *testing.T) {
	tests := []struct {
		name, key, value, reason string
	}{
		{"unknown", Prefix + "made-up", "1", reasonUnknown},
		{"empty", Handler, "  ", reasonEmpty},
		{"handler", Handler, "cache", "must be"},
		{"max ttl unitless", MaxTTL, "600", "duration with a unit"},
		{"timeout negative", Timeout, "-5s", "greater than zero"},
		{"cors mode", CORSMode, "sometimes", "must be one of"},
		{"collapsed forwarding", CollapsedForwarding, "eager", "must be"},
		{"use regex", UseRegex, "yes please", "must be a boolean"},
		{"rewrite whitespace", RewriteTarget, "/a b", "must not contain whitespace"},
		{"health mode", HealthMode, "guess", "must be"},
		{"header shape", RequestHeaders, "X-A 1", "must be 'Name: value'"},
		{"header name", RequestHeaders, "X A: 1", "not a valid header name"},
		{"header operator only", ResponseHeaders, "-: 1", "not a valid header name"},
		{"header blank", ResponseHeaders, "\n\n", reasonEmpty},
		{"header no name", RequestHeaders, ": 1", "not a valid header name"},
		{"cors header name", CORSHeaders, "X A: 1", "not a valid header name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set, problems := Parse(map[string]string{
				test.key: test.value, CacheName: "objects",
			})
			require.Len(t, problems, 1)
			require.Equal(t, test.key, problems[0].Annotation)
			require.Contains(t, problems[0].Reason, test.reason)
			require.Contains(t, problems[0].String(), test.key)
			require.Equal(t, "objects", set.Policy.CacheName,
				"a rejected annotation must not stop the others from applying")
		})
	}
}

// The rejection list is what an Event is built from, so it has to be the
// same list every pass over the same object
func TestParseRejectionsAreOrdered(t *testing.T) {
	in := map[string]string{
		Prefix + "zebra": "1", Prefix + "alpha": "1", Prefix + "middle": "1",
	}
	for range 8 {
		_, problems := Parse(in)
		require.Len(t, problems, 3)
		require.Equal(t, Prefix+"alpha", problems[0].Annotation)
		require.Equal(t, Prefix+"middle", problems[1].Annotation)
		require.Equal(t, Prefix+"zebra", problems[2].Annotation)
	}
}

// A header value may not smuggle a newline into the response
func TestParseRejectsInvalidHeaderValue(t *testing.T) {
	_, problems := Parse(map[string]string{ResponseHeaders: "X-A: on\re"})
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Reason, "is not a valid value")
}

// Each field on its own is enough to make a policy worth emitting
func TestConfiguresPolicyPerField(t *testing.T) {
	fields := map[string]string{
		Handler:             ir.HandlerProxy,
		CacheName:           "objects",
		MaxTTL:              "1m",
		Timeout:             "1s",
		NegativeCacheName:   "api-errors",
		RequestHeaders:      "X-A: 1",
		ResponseHeaders:     "X-A: 1",
		CORSMode:            "disable",
		CORSHeaders:         "X-A: 1",
		CollapsedForwarding: "basic",
		RewriteTarget:       "/",
		HealthMode:          "provider",
	}
	for key, value := range fields {
		t.Run(key, func(t *testing.T) {
			set, problems := Parse(map[string]string{key: value})
			require.Empty(t, problems)
			require.True(t, set.ConfiguresPolicy())
		})
	}
	// use-regex is the translator's, and never reaches a policy
	set, problems := Parse(map[string]string{UseRegex: "true"})
	require.Empty(t, problems)
	require.False(t, set.ConfiguresPolicy())
}
