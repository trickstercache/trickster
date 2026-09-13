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

package ir

import (
	"testing"

	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/stretchr/testify/require"
)

func TestReportMerge(t *testing.T) {
	var nilReport *Report
	require.True(t, nilReport.IsEmpty())
	require.True(t, nilReport.Merge(nil).IsEmpty())

	a := &Report{Classes: []ClassStatus{{Source: Source{Kind: KindGatewayClass, Name: "a"}}}}
	b := &Report{
		Gateways:  []GatewayStatus{{Source: Source{Kind: KindGateway, Name: "gw"}}},
		Routes:    []RouteStatus{{Source: Source{Kind: KindHTTPRoute, Name: "r"}}},
		Ingresses: []Source{{Kind: KindIngress, Name: "i"}},
	}
	m := a.Merge(b)
	require.Len(t, m.Classes, 1)
	require.Len(t, m.Gateways, 1)
	require.Len(t, m.Routes, 1)
	require.Len(t, m.Ingresses, 1)
	require.False(t, m.IsEmpty())
	// merging copies rather than aliases
	require.Len(t, a.Gateways, 0)
	require.Same(t, a, a.Merge(nil))
}

func TestConditionFindAndSet(t *testing.T) {
	conds := []Condition{{Type: "Accepted", Status: true, Reason: "Accepted"}}
	c, ok := Find(conds, "Accepted")
	require.True(t, ok)
	require.True(t, c.Status)
	_, ok = Find(conds, "Programmed")
	require.False(t, ok)

	conds = Set(conds, Condition{Type: "Accepted", Status: false, Reason: "Invalid"})
	require.Len(t, conds, 1)
	require.False(t, conds[0].Status)
	conds = Set(conds, Condition{Type: "Programmed", Status: true})
	require.Len(t, conds, 2)
}

func TestProblemEventReason(t *testing.T) {
	require.Equal(t, ReasonRejected, Problem{}.EventReason())
	require.Equal(t, ReasonInvalidAnnotation,
		Problem{Reason: ReasonInvalidAnnotation}.EventReason())
}

func TestHashIgnoresUIDs(t *testing.T) {
	a := &IR{Routes: []Route{{Name: "r", Source: Source{Kind: KindIngress, Name: "web"}}}}
	b := &IR{Routes: []Route{{Name: "r", Source: Source{
		Kind: KindIngress, Name: "web",
		UID: "1234",
	}}}}
	require.Equal(t, a.Hash(), b.Hash())
	require.Equal(t, "1234", b.Routes[0].Source.UID, "hashing must not strip the input")
}

func TestValidateCertPair(t *testing.T) {
	key, crt := tlstest.NamedKeyAndCert("web")
	require.NoError(t, ValidateCertPair(crt, key))
	require.ErrorIs(t, ValidateCertPair(nil, key), ErrCertEmpty)
	require.ErrorIs(t, ValidateCertPair(crt, nil), ErrCertEmpty)
	require.ErrorIs(t, ValidateCertPair([]byte("garbage"), key), ErrCertInvalid)
	other, _ := tlstest.NamedKeyAndCert("other")
	require.ErrorIs(t, ValidateCertPair(crt, other), ErrCertInvalid, "a mismatched key")
}

func TestParseCertPair(t *testing.T) {
	// the identity is the leaf's names as a store indexes them, and a digest of the material
	key, crt, err := tlstest.GetTestKeyAndCertWithNames("A.Example.com", "*.example.com",
		"**.example.org", "b.example.com", "a.example.com.")
	require.NoError(t, err)
	id, err := ParseCertPair(crt, key)
	require.NoError(t, err)
	require.Equal(t, []string{"*.example.com", "a.example.com", "b.example.com"}, id.Names)
	require.NotEmpty(t, id.Digest)
	again, err := ParseCertPair(crt, key)
	require.NoError(t, err)
	require.Equal(t, id, again, "the identity is a function of the material")
	otherKey, otherCrt := tlstest.NamedKeyAndCert("other")
	other, err := ParseCertPair(otherCrt, otherKey)
	require.NoError(t, err)
	require.NotEqual(t, id.Digest, other.Digest)
	require.Equal(t, []string{"other"}, other.Names)
	key, crt, err = tlstest.GetTestKeyAndCertWithNames()
	require.NoError(t, err)
	none, err := ParseCertPair(crt, key)
	require.NoError(t, err)
	require.Empty(t, none.Names, "a certificate naming nothing answers for nothing")
	_, err = ParseCertPair(nil, key)
	require.ErrorIs(t, err, ErrCertEmpty)
}
