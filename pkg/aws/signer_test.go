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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
// signing, so a failed resolution is reused only for failureBackoff.
func TestFailedResolutionIsNotCached(t *testing.T) {
	isolate(t)
	s, err := NewSigner(&Options{AccessKey: "a", SecretKey: "b"})
	require.NoError(t, err)
	now := time.Now()
	s.now = func() time.Time { return now }

	r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	require.ErrorIs(t, s.SignRequest(t.Context(), r), ErrNoRegion)
	require.Nil(t, s.cachedConfig(), "a failed resolve must not be cached")

	// within the backoff the failure is reused rather than resolved again
	t.Setenv("AWS_REGION", "us-west-2")
	require.ErrorIs(t, s.SignRequest(t.Context(), r), ErrNoRegion)

	// once it elapses, the same signer resolves and succeeds
	now = now.Add(failureBackoff)
	require.NoError(t, s.SignRequest(t.Context(), r))
	require.Contains(t, r.Header.Get("Authorization"), "/us-west-2/")
}

func TestConcurrentFirstUseResolvesOnce(t *testing.T) {
	isolate(t)
	s, err := NewSigner(staticOptions())
	require.NoError(t, err)
	var loads atomic.Int32
	release := make(chan struct{})
	load := s.resolve
	s.resolve = func(ctx context.Context) (*aws.Config, error) {
		loads.Add(1)
		<-release
		return load(ctx)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
			if err := s.SignRequest(t.Context(), r); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, loads.Load())
}

// A waiter that gives up must not cancel the resolution the other callers share.
func TestCanceledWaiterDoesNotFailTheSharedResolution(t *testing.T) {
	isolate(t)
	s, err := NewSigner(staticOptions())
	require.NoError(t, err)
	release := make(chan struct{})
	load := s.resolve
	s.resolve = func(ctx context.Context) (*aws.Config, error) {
		<-release
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return load(ctx)
	}
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() {
		r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		first <- s.SignRequest(ctx, r)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	close(release)
	<-first
	r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	require.NoError(t, s.SignRequest(t.Context(), r))
}

// credentialProcessProfile writes a shared config whose profile p runs a credential_process
// returning expiring credentials, and returns a func reporting how many times it ran.
func credentialProcessProfile(t *testing.T, expires time.Time) func() int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("credential_process script requires a POSIX shell")
	}
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	script := filepath.Join(dir, "creds.sh")
	body := fmt.Sprintf("#!/bin/sh\necho x >> %q\nprintf '%%s' '%s'\n", counter,
		`{"Version":1,"AccessKeyId":"ASIAPROCESS","SecretAccessKey":"secret",`+
			`"SessionToken":"token","Expiration":"`+expires.UTC().Format(time.RFC3339)+`"}`)
	require.NoError(t, os.WriteFile(script, []byte(body), 0o700))
	cfg := filepath.Join(dir, "config")
	require.NoError(t, os.WriteFile(cfg,
		[]byte("[profile p]\nregion = us-east-1\ncredential_process = "+script+"\n"), 0o600))
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	return func() int {
		b, _ := os.ReadFile(counter)
		return strings.Count(string(b), "x")
	}
}

// Credentials resolved through the chain must refresh within expiryWindow of expiry, not after.
func TestChainCredentialsRefreshBeforeExpiry(t *testing.T) {
	isolate(t)
	runs := credentialProcessProfile(t, time.Now().Add(expiryWindow/4))
	s, err := NewSigner(&Options{Profile: "p", Service: "monitoring"})
	require.NoError(t, err)
	for range 2 {
		r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		require.NoError(t, s.SignRequest(t.Context(), r))
	}
	require.Equal(t, 2, runs(), "credentials inside the expiry window must be refreshed")
}

func TestChainCredentialsOutsideWindowAreReused(t *testing.T) {
	isolate(t)
	runs := credentialProcessProfile(t, time.Now().Add(time.Hour))
	s, err := NewSigner(&Options{Profile: "p", Service: "monitoring"})
	require.NoError(t, err)
	for range 3 {
		r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		require.NoError(t, s.SignRequest(t.Context(), r))
	}
	require.Equal(t, 1, runs())
}

// The role_arn path builds its own cache, so the window is checked on cacheOptions directly.
func TestCacheOptionsRefreshBeforeExpiry(t *testing.T) {
	p := &rotatingProvider{expires: time.Now().Add(expiryWindow / 4)}
	c := aws.NewCredentialsCache(p, cacheOptions)
	for range 2 {
		_, err := c.Retrieve(t.Context())
		require.NoError(t, err)
	}
	require.EqualValues(t, 2, p.calls.Load())
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

	client := &http.Client{Transport: newRoundTripper(t, staticOptions(), Observer{})}

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

	rt := newRoundTripper(t, staticOptions(), Observer{})

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

// A signing failure must surface as an error rather than sending an
// unsigned request that the origin would reject confusingly.
func TestRoundTripperFailsClosed(t *testing.T) {
	isolate(t)
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()

	var failures int
	rt := newRoundTripper(t, &Options{AccessKey: "a", SecretKey: "b"},
		Observer{SignFailed: func(error) { failures++ }})
	r, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := rt.RoundTrip(r)
	require.ErrorIs(t, err, ErrNoRegion)
	require.False(t, reached, "an unsigned request must not be sent")
	require.Equal(t, 1, failures, "the failure must reach the observer")
}

func newRoundTripper(t *testing.T, o *Options, obs Observer) http.RoundTripper {
	t.Helper()
	s, err := NewSigner(o)
	require.NoError(t, err)
	return WrapTransport(s, nil, obs)
}

// rotatingProvider issues expiring credentials with a new access key on every call.
type rotatingProvider struct {
	calls   atomic.Int32
	expires time.Time
}

func (p *rotatingProvider) Retrieve(context.Context) (aws.Credentials, error) {
	n := p.calls.Add(1)
	expires := p.expires
	if expires.IsZero() {
		expires = time.Now().Add(time.Hour)
	}
	return aws.Credentials{
		AccessKeyID:     fmt.Sprintf("ASIAKEY%d", n),
		SecretAccessKey: "secret",
		SessionToken:    fmt.Sprintf("token%d", n),
		CanExpire:       true,
		Expires:         expires,
	}, nil
}

// rotatingSigner returns a signer whose resolved configuration uses p for credentials.
func rotatingSigner(t *testing.T, p aws.CredentialsProvider) *Signer {
	t.Helper()
	s, err := NewSigner(&Options{Region: "us-east-1", Service: "monitoring"})
	require.NoError(t, err)
	s.cfg = &aws.Config{Region: "us-east-1", Credentials: aws.NewCredentialsCache(p, cacheOptions)}
	return s
}

// rejectingOrigin answers 403 with the given code to requests signed with a rejected key.
type rejectingOrigin struct {
	mtx      sync.Mutex
	hits     int
	bodies   []string
	reject   func(key string) bool
	inHeader bool
	code     string
}

func (o *rejectingOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	auth := r.Header.Get("Authorization")
	key, _, _ := strings.Cut(auth[strings.Index(auth, "Credential=")+len("Credential="):], "/")
	o.mtx.Lock()
	o.hits++
	o.bodies = append(o.bodies, string(b))
	o.mtx.Unlock()
	if !o.reject(key) {
		w.Write([]byte("ok"))
		return
	}
	if o.inHeader {
		w.Header().Set(headerErrorType, o.code+":http://internal.amazon.com/coral/")
	}
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `{"__type":"%s","message":"rejected"}`, o.code)
}

func TestRoundTripperRetriesRejectedExpiringCredentials(t *testing.T) {
	for _, inHeader := range []bool{true, false} {
		t.Run(fmt.Sprintf("inHeader=%v", inHeader), func(t *testing.T) {
			p := &rotatingProvider{}
			origin := &rejectingOrigin{
				inHeader: inHeader, code: "ExpiredTokenException",
				reject: func(key string) bool { return key == "ASIAKEY1" },
			}
			srv := httptest.NewServer(origin)
			defer srv.Close()

			var codes []string
			rt := WrapTransport(rotatingSigner(t, p), nil,
				Observer{CredentialsRetried: func(c string) { codes = append(codes, c) }})
			r, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("payload"))
			resp, err := rt.RoundTrip(r)
			require.NoError(t, err)
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "ok", string(b))
			require.Equal(t, 2, origin.hits)
			require.Equal(t, []string{"payload", "payload"}, origin.bodies,
				"the retry must resend the body")
			require.EqualValues(t, 2, p.calls.Load(), "the rejected credentials must be refreshed")
			require.Equal(t, []string{"ExpiredTokenException"}, codes)
		})
	}
}

func TestRoundTripperRetriesOnlyOnce(t *testing.T) {
	origin := &rejectingOrigin{
		inHeader: true, code: "UnrecognizedClientException",
		reject: func(string) bool { return true },
	}
	srv := httptest.NewServer(origin)
	defer srv.Close()

	rt := WrapTransport(rotatingSigner(t, &rotatingProvider{}), nil, Observer{})
	r, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := rt.RoundTrip(r)
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(b), "UnrecognizedClientException")
	require.Equal(t, 2, origin.hits)
}

// A 403 that is not about credentials is returned as is, with the body it peeked restored.
func TestRoundTripperDoesNotRetryOtherForbidden(t *testing.T) {
	origin := &rejectingOrigin{
		inHeader: true, code: "AccessDeniedException",
		reject: func(string) bool { return true },
	}
	srv := httptest.NewServer(origin)
	defer srv.Close()

	rt := WrapTransport(rotatingSigner(t, &rotatingProvider{}), nil, Observer{})
	r, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := rt.RoundTrip(r)
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, `{"__type":"AccessDeniedException","message":"rejected"}`, string(b))
	require.Equal(t, 1, origin.hits)
}

// Static credentials refresh to the same values, so a rejection of them is not retried.
func TestRoundTripperDoesNotRetryStaticCredentials(t *testing.T) {
	isolate(t)
	origin := &rejectingOrigin{
		inHeader: true, code: "InvalidSignatureException",
		reject: func(string) bool { return true },
	}
	srv := httptest.NewServer(origin)
	defer srv.Close()

	s, err := NewSigner(staticOptions())
	require.NoError(t, err)
	now := time.Now()
	s.now = func() time.Time { return now }
	rt := WrapTransport(s, nil, Observer{})
	for i := range 2 {
		// the second rejection comes after rereadInterval, which must not matter for configured keys
		now = now.Add(time.Duration(i) * rereadInterval)
		r, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := rt.RoundTrip(r)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.Equal(t, i+1, origin.hits)
	}
}

// Concurrent rejections of the same credentials must cause one refresh, not one each.
func TestConcurrentRejectionsRefreshOnce(t *testing.T) {
	p := &rotatingProvider{}
	origin := &rejectingOrigin{
		inHeader: true, code: "ExpiredTokenException",
		reject: func(key string) bool { return key == "ASIAKEY1" },
	}
	srv := httptest.NewServer(origin)
	defer srv.Close()

	rt := WrapTransport(rotatingSigner(t, p), nil, Observer{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			r, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := rt.RoundTrip(r)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status %d", resp.StatusCode)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 2, p.calls.Load())
}

// writeSharedCredentials writes a default profile holding key to a shared credentials file the
// chain reads, as a refresher that rewrites the file in place would.
func writeSharedCredentials(t *testing.T, path, key string) {
	t.Helper()
	body := "[default]\naws_access_key_id = " + key +
		"\naws_secret_access_key = secret\naws_session_token = token-" + key + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// Session credentials in the shared file look static to the SDK, so a rejection of them re-reads
// the file, at most every rereadInterval, rather than refreshing the cache.
func TestRoundTripperRereadsSharedFileCredentials(t *testing.T) {
	isolate(t)
	creds := filepath.Join(t.TempDir(), "credentials")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", creds)
	writeSharedCredentials(t, creds, "ASIAOLD")

	origin := &rejectingOrigin{
		inHeader: true, code: "ExpiredTokenException",
		reject: func(key string) bool { return key == "ASIAOLD" },
	}
	srv := httptest.NewServer(origin)
	defer srv.Close()

	s, err := NewSigner(&Options{Region: "us-east-1", Service: "monitoring"})
	require.NoError(t, err)
	now := time.Now()
	s.now = func() time.Time { return now }
	var retries int
	rt := WrapTransport(s, nil, Observer{CredentialsRetried: func(string) { retries++ }})
	send := func() int {
		r, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := rt.RoundTrip(r)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}

	// the first request just read the file, so its rejection is not retried
	require.Equal(t, http.StatusForbidden, send())
	require.Equal(t, 1, origin.hits)
	// after the interval, a rejection re-reads the unchanged file and resends once
	now = now.Add(rereadInterval)
	require.Equal(t, http.StatusForbidden, send())
	require.Equal(t, 3, origin.hits)
	// a rejection soon after does not re-read the file again
	require.Equal(t, http.StatusForbidden, send())
	require.Equal(t, 4, origin.hits)
	require.Equal(t, 1, retries)

	// once the file is rewritten and the interval passes, a rejection picks up the new key
	writeSharedCredentials(t, creds, "ASIANEW")
	now = now.Add(rereadInterval)
	require.Equal(t, http.StatusOK, send())
	require.Equal(t, 6, origin.hits)
	require.Equal(t, http.StatusOK, send())
	require.Equal(t, 7, origin.hits, "the new key is used without a further re-read")
	require.Equal(t, 2, retries)
}
