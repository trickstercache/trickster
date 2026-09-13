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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// staticOptions returns options that resolve without touching the network,
// so the tests never depend on an ambient AWS environment.
func staticOptions() *Options {
	return &Options{
		Region:    "us-east-1",
		AccessKey: "AKIDEXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		Service:   "ec2",
	}
}

// isolate clears the ambient AWS environment so a developer's own
// credentials or region cannot make a test pass or fail.
func isolate(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE",
		"AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE",
	} {
		t.Setenv(k, "")
	}
	// keep a wedged or absent metadata service from stalling the resolve
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

func TestNewSignerValidates(t *testing.T) {
	_, err := NewSigner(nil)
	require.Error(t, err)

	_, err = NewSigner(&Options{AccessKey: "a"})
	require.ErrorIs(t, err, ErrIncompleteStaticCredentials)

	s, err := NewSigner(staticOptions())
	require.NoError(t, err)
	require.Equal(t, "ec2", s.Service())
}

// NewSigner must not reach the network: startup cannot depend on the
// instance metadata service being up at that instant.
func TestNewSignerDoesNoNetworkIO(t *testing.T) {
	isolate(t)
	// no region anywhere; if construction resolved, this would fail here
	s, err := NewSigner(&Options{AccessKey: "a", SecretKey: "b"})
	require.NoError(t, err)
	require.NotNil(t, s)

	// the failure surfaces on first use instead
	r, _ := http.NewRequest(http.MethodGet, "https://ec2.amazonaws.com/", nil)
	require.ErrorIs(t, s.SignRequest(t.Context(), r), ErrNoRegion)
}

func TestSignRequestAddsSigV4Headers(t *testing.T) {
	isolate(t)
	s, err := NewSigner(staticOptions())
	require.NoError(t, err)

	r, _ := http.NewRequest(http.MethodGet, "https://ec2.us-east-1.amazonaws.com/?Action=DescribeInstances", nil)
	require.NoError(t, s.SignRequest(t.Context(), r))

	auth := r.Header.Get("Authorization")
	require.Contains(t, auth, "AWS4-HMAC-SHA256")
	require.Contains(t, auth, "Credential=AKIDEXAMPLE/")
	require.Contains(t, auth, "/us-east-1/ec2/aws4_request")
	require.Contains(t, auth, "Signature=")
	require.NotEmpty(t, r.Header.Get("X-Amz-Date"))
}

// The signing service is what the previous implementation hardcoded; the
// whole point of the re-home is that it is now configurable.
func TestSigningServiceIsConfigurable(t *testing.T) {
	isolate(t)
	for _, service := range []string{"aps", "ec2", "ecs"} {
		o := staticOptions()
		o.Service = service
		s, err := NewSigner(o)
		require.NoError(t, err)
		r, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
		require.NoError(t, s.SignRequest(t.Context(), r))
		require.Contains(t, r.Header.Get("Authorization"),
			"/us-east-1/"+service+"/aws4_request")
	}

	// and an unset service still signs for aps, as every existing config
	// expects
	o := staticOptions()
	o.Service = ""
	s, err := NewSigner(o)
	require.NoError(t, err)
	r, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	require.NoError(t, s.SignRequest(t.Context(), r))
	require.Contains(t, r.Header.Get("Authorization"), "/aps/aws4_request")
}

// SigV4 signs a hash of the body, so a request with one must remain
// readable and retryable after signing.
func TestSignRequestWithBodyLeavesItReadable(t *testing.T) {
	isolate(t)
	s, err := NewSigner(staticOptions())
	require.NoError(t, err)

	r, _ := http.NewRequest(http.MethodPost, "https://ec2.us-east-1.amazonaws.com/",
		strings.NewReader("Action=DescribeInstances&Version=2016-11-15"))
	require.NoError(t, s.SignRequest(t.Context(), r))

	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.Equal(t, "Action=DescribeInstances&Version=2016-11-15", string(body))
	require.EqualValues(t, len(body), r.ContentLength)

	require.NotNil(t, r.GetBody, "a signed request must stay retryable")
	rc, err := r.GetBody()
	require.NoError(t, err)
	again, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, string(body), string(again))
}

// Different bodies must produce different signatures, or the payload hash
// is not actually being computed.
func TestBodyIsCoveredByTheSignature(t *testing.T) {
	isolate(t)
	s, err := NewSigner(staticOptions())
	require.NoError(t, err)
	s.now = func() time.Time { return time.Unix(1700000000, 0).UTC() }

	sign := func(body string) string {
		r, _ := http.NewRequest(http.MethodPost, "https://ec2.us-east-1.amazonaws.com/",
			strings.NewReader(body))
		require.NoError(t, s.SignRequest(t.Context(), r))
		return r.Header.Get("Authorization")
	}
	require.NotEqual(t, sign("one"), sign("two"))
	require.Equal(t, sign("same"), sign("same"))
}

func TestHashPayloadEmptyBody(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	h, err := hashPayload(r)
	require.NoError(t, err)
	require.Equal(t, emptyPayloadHash, h)

	r, _ = http.NewRequest(http.MethodGet, "https://example.com/", http.NoBody)
	h, err = hashPayload(r)
	require.NoError(t, err)
	require.Equal(t, emptyPayloadHash, h)
}

// A momentary metadata-service failure must not permanently disable
// signing, so only a successful resolution is cached.
func TestFailedResolutionIsNotCached(t *testing.T) {
	isolate(t)
	s, err := NewSigner(&Options{AccessKey: "a", SecretKey: "b"})
	require.NoError(t, err)

	r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	require.ErrorIs(t, s.SignRequest(t.Context(), r), ErrNoRegion)
	s.mtx.Lock()
	resolved := s.resolved
	s.mtx.Unlock()
	require.False(t, resolved, "a failed resolve must not be cached")

	// once the region is available, the same signer succeeds
	t.Setenv("AWS_REGION", "us-west-2")
	require.NoError(t, s.SignRequest(t.Context(), r))
	require.Contains(t, r.Header.Get("Authorization"), "/us-west-2/")
}

func TestConcurrentSigning(t *testing.T) {
	isolate(t)
	s, err := NewSigner(staticOptions())
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 20 {
				r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
				if err := s.SignRequest(context.Background(), r); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// The RoundTripper is how the proxy applies a backend's sigv4 block.
func TestRoundTripperSignsOutboundRequests(t *testing.T) {
	isolate(t)
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer srv.Close()

	rt, err := NewRoundTripper(staticOptions(), nil)
	require.NoError(t, err)
	client := &http.Client{Transport: rt}

	resp, err := client.Post(srv.URL, "text/plain", strings.NewReader("payload"))
	require.NoError(t, err)
	resp.Body.Close()
	require.Contains(t, gotAuth, "AWS4-HMAC-SHA256")
	require.Contains(t, gotAuth, "/ec2/aws4_request")
	require.Equal(t, "payload", gotBody, "the body must survive signing")
}

// The RoundTripper contract forbids modifying the request it is given.
func TestRoundTripperDoesNotMutateTheCallersRequest(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	rt, err := NewRoundTripper(staticOptions(), nil)
	require.NoError(t, err)

	r, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("payload"))
	resp, err := rt.RoundTrip(r)
	require.NoError(t, err)
	resp.Body.Close()

	require.Empty(t, r.Header.Get("Authorization"),
		"the caller's request must not be modified")
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.Equal(t, "payload", string(body),
		"the caller's body must remain readable")
}

func TestNewRoundTripperValidates(t *testing.T) {
	_, err := NewRoundTripper(&Options{SecretKey: "s"}, nil)
	require.ErrorIs(t, err, ErrIncompleteStaticCredentials)
}

// A signing failure must surface as an error rather than sending an
// unsigned request that the origin would reject confusingly.
func TestRoundTripperFailsClosed(t *testing.T) {
	isolate(t)
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()

	rt, err := NewRoundTripper(&Options{AccessKey: "a", SecretKey: "b"}, nil)
	require.NoError(t, err)
	r, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err = rt.RoundTrip(r)
	require.ErrorIs(t, err, ErrNoRegion)
	require.False(t, reached, "an unsigned request must not be sent")
}
