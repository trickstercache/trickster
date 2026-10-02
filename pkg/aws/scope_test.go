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

package aws

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseScope(t *testing.T) {
	tests := []struct {
		auth string
		want Scope
		ok   bool
	}{
		{"AWS4-HMAC-SHA256 Credential=AKID/20261002/us-west-2/logs/aws4_request, " +
			"SignedHeaders=host;x-amz-date, Signature=abc", Scope{Service: "logs", Region: "us-west-2"}, true},
		{
			"AWS4-HMAC-SHA256 Credential=AKID/20261002/eu-west-1/monitoring/aws4_request",
			Scope{Service: "monitoring", Region: "eu-west-1"},
			true,
		},
		// SigV4a credentials carry no region
		{"AWS4-ECDSA-P256-SHA256 Credential=AKID/20261002/monitoring/aws4_request", Scope{}, false},
		{"Basic dXNlcjpwYXNz", Scope{}, false},
		{"AWS4-HMAC-SHA256 Credential=AKID/20261002//logs/aws4_request", Scope{}, false},
	}
	for _, tc := range tests {
		got, ok := ParseScope(tc.auth)
		require.Equal(t, tc.ok, ok, tc.auth)
		require.Equal(t, tc.want, got, tc.auth)
	}
}

func TestValidRegion(t *testing.T) {
	for _, r := range []string{"us-east-1", "ap-southeast-5", "us-gov-west-1", "eu-central-2", "cn-north-1"} {
		require.True(t, ValidRegion(r), r)
	}
	for _, r := range []string{"", "us-east", "US-EAST-1", "us-east-1.evil.example", "us-east-1/x", "evil.com#us-east-1"} {
		require.False(t, ValidRegion(r), r)
	}
}

func TestSigningScopeOverridesServiceAndRegion(t *testing.T) {
	isolate(t)
	s, err := NewSigner(staticOptions())
	require.NoError(t, err)
	r, _ := http.NewRequest(http.MethodPost, "https://logs.eu-west-1.amazonaws.com/", nil)
	ctx := WithSigningScope(t.Context(), Scope{Service: "logs", Region: "eu-west-1"})
	require.NoError(t, s.SignRequest(ctx, r))
	require.Contains(t, r.Header.Get("Authorization"), "/eu-west-1/logs/aws4_request")

	// an empty field keeps the signer's own
	r, _ = http.NewRequest(http.MethodPost, "https://ec2.us-east-1.amazonaws.com/", nil)
	require.NoError(t, s.SignRequest(WithSigningScope(t.Context(), Scope{Service: "ec2"}), r))
	require.Contains(t, r.Header.Get("Authorization"), "/us-east-1/ec2/aws4_request")
}
