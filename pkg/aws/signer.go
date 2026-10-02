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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sigv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// emptyPayloadHash is the SHA-256 of the empty string, which SigV4 requires
// for a request with no body.
const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// resolveTimeout bounds the first credential and region resolution, so a
// wedged instance metadata service cannot hang a request indefinitely.
const resolveTimeout = 15 * time.Second

const (
	// failureBackoff is how long a failed resolution is reused, so an IMDS or STS outage blocks
	// one resolution per interval rather than every request
	failureBackoff = 5 * time.Second
	// expiryWindow refreshes expiring credentials this long before they lapse, jittered by expiryJitter
	expiryWindow = 5 * time.Minute
	expiryJitter = 0.5
	// authErrorPeekBytes bounds how much of a 403 body is read to find its error code
	authErrorPeekBytes = 4 << 10
	// drainBytes bounds how much of a discarded response is read so its connection can be reused
	drainBytes = 64 << 10
	// rereadInterval bounds how often a rejection re-reads credentials from the shared files
	rereadInterval = 5 * time.Second
	// sharedFileSource prefixes the SDK's Source for credentials read from the shared files
	sharedFileSource = "SharedConfigCredentials"

	headerErrorType = "X-Amzn-Errortype"
)

// authErrorCodes are the 403 codes that invalidate cached credentials and retry the request once.
// Longer codes precede their prefixes so the reported code is the most specific.
var authErrorCodes = []string{
	"ExpiredTokenException",
	"ExpiredToken",
	"InvalidClientTokenId",
	"UnrecognizedClientException",
	"InvalidSignatureException",
}

// Signer resolves AWS credentials on first use and signs requests with SigV4.
// A failed resolution is reused only for failureBackoff, so an IMDS blip cannot disable signing.
type Signer struct {
	opts    *Options
	service string
	signer  *sigv4.Signer
	// now and resolve are overridable in tests
	now     func() time.Time
	resolve func(context.Context) (*aws.Config, error)

	mtx        sync.Mutex
	cfg        *aws.Config
	resolvedAt time.Time
	loading    chan struct{}
	loadErr    error
	retryAt    time.Time

	invalidateMtx sync.Mutex
}

// NewSigner returns a Signer for the given options. It performs no network
// I/O; credentials and region are resolved on first use.
func NewSigner(o *Options) (*Signer, error) {
	if o == nil {
		return nil, errors.New("aws: nil options")
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	s := &Signer{
		opts:    o.Clone(),
		service: o.GetService(),
		signer:  sigv4.NewSigner(),
		now:     time.Now,
	}
	s.resolve = s.load
	return s, nil
}

// Service returns the signing service name this Signer uses.
func (s *Signer) Service() string { return s.service }

// config resolves and caches the AWS configuration. Concurrent callers share one resolution,
// and a failure is returned to every caller until failureBackoff elapses.
func (s *Signer) config(ctx context.Context) (*aws.Config, error) {
	for {
		s.mtx.Lock()
		if s.cfg != nil {
			cfg := s.cfg
			s.mtx.Unlock()
			return cfg, nil
		}
		if s.loadErr != nil && s.now().Before(s.retryAt) {
			err := s.loadErr
			s.mtx.Unlock()
			return nil, err
		}
		if wait := s.loading; wait != nil {
			s.mtx.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		s.loading = done
		s.mtx.Unlock()

		// detached so one caller's cancellation cannot fail the resolution every waiter shares
		cfg, err := s.resolve(context.WithoutCancel(ctx))

		s.mtx.Lock()
		s.loading = nil
		if err != nil {
			s.loadErr, s.retryAt = err, s.now().Add(failureBackoff)
		} else {
			s.cfg, s.resolvedAt, s.loadErr = cfg, s.now(), nil
		}
		s.mtx.Unlock()
		close(done)
		return cfg, err
	}
}

// cachedConfig returns the resolved configuration without resolving it.
func (s *Signer) cachedConfig() *aws.Config {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.cfg
}

// cacheOptions makes a credentials cache refresh within expiryWindow of expiry rather than after it.
func cacheOptions(o *aws.CredentialsCacheOptions) {
	o.ExpiryWindow = expiryWindow
	o.ExpiryWindowJitterFrac = expiryJitter
}

// load builds the AWS configuration from the options and the standard
// credential chain.
func (s *Signer) load(ctx context.Context) (*aws.Config, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	loadOpts := make([]func(*awsconfig.LoadOptions) error, 0, 4)
	loadOpts = append(loadOpts, awsconfig.WithCredentialsCacheOptions(cacheOptions))
	if s.opts.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(s.opts.Region))
	}
	if s.opts.Profile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(s.opts.Profile))
	}
	if s.opts.AccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				s.opts.AccessKey, string(s.opts.SecretKey), "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("aws: loading configuration: %w", err)
	}
	if s.opts.RoleARN != "" {
		// assume the role with whatever the chain resolved first, and cache
		// the resulting short-lived credentials so every request does not
		// call STS
		cfg.Credentials = aws.NewCredentialsCache(
			stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), s.opts.RoleARN), cacheOptions)
	}
	if cfg.Region == "" {
		return nil, ErrNoRegion
	}
	return &cfg, nil
}

// SignRequest signs r in place with SigV4.
//
// Its signature matches the poller's RequestDecorator, so a discovery
// provider can pass this method directly without this package importing
// anything from the discovery tree.
//
// A request with a body is buffered so its payload can be hashed, which
// SigV4 requires; r.Body and r.GetBody are replaced with readers over that
// buffer so the request remains usable and retryable.
func (s *Signer) SignRequest(ctx context.Context, r *http.Request) error {
	_, err := s.sign(ctx, r)
	return err
}

// sign signs r in place and returns the credentials it was signed with.
func (s *Signer) sign(ctx context.Context, r *http.Request) (aws.Credentials, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return aws.Credentials{}, err
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("aws: retrieving credentials: %w", err)
	}
	hash, err := hashPayload(r)
	if err != nil {
		return creds, err
	}
	service, region := signingScope(ctx, s.service, cfg.Region)
	if err := s.signer.SignHTTP(ctx, creds, r, hash,
		service, region, s.now().UTC()); err != nil {
		return creds, fmt.Errorf("aws: signing request: %w", err)
	}
	return creds, nil
}

// refresh renews a rejected request's credentials and reports whether a resend may succeed;
// shared-file ones are re-read at most every rereadInterval.
func (s *Signer) refresh(ctx context.Context, used aws.Credentials) bool {
	if used.CanExpire {
		s.invalidate(ctx, used)
		return true
	}
	if !strings.HasPrefix(used.Source, sharedFileSource) {
		return false
	}
	s.mtx.Lock()
	cfg, resolvedAt := s.cfg, s.resolvedAt
	if cfg != nil && s.now().Sub(resolvedAt) >= rereadInterval {
		// the next request resolves the chain again, which re-reads the files
		s.cfg = nil
		cfg = nil
	}
	s.mtx.Unlock()
	if cfg == nil {
		return true
	}
	// read recently: resend only if that read found other credentials than the request used
	cur, err := cfg.Credentials.Retrieve(ctx)
	return err == nil && (cur.AccessKeyID != used.AccessKeyID || cur.SessionToken != used.SessionToken)
}

// invalidate drops the cached credentials if they are still the ones a rejected request used,
// so concurrent rejections of the same credentials cause one refresh rather than many.
func (s *Signer) invalidate(ctx context.Context, used aws.Credentials) {
	cfg := s.cachedConfig()
	if cfg == nil {
		return
	}
	cache, ok := cfg.Credentials.(*aws.CredentialsCache)
	if !ok {
		return
	}
	s.invalidateMtx.Lock()
	defer s.invalidateMtx.Unlock()
	if cur, err := cache.Retrieve(ctx); err == nil &&
		(cur.AccessKeyID != used.AccessKeyID || cur.SessionToken != used.SessionToken) {
		return
	}
	cache.Invalidate()
}

// hashPayload returns the SigV4 payload hash for r, buffering the body when
// there is one.
func hashPayload(r *http.Request) (string, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return emptyPayloadHash, nil
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return "", fmt.Errorf("aws: reading request body to sign: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// Observer receives a signing RoundTripper's events; nil funcs are skipped.
type Observer struct {
	// SignFailed reports a request that could not be signed and so was not sent
	SignFailed func(error)
	// CredentialsRetried reports a request resent with refreshed credentials after a 403 with code
	CredentialsRetried func(code string)
}

// roundTripper signs each request before handing it to the next RoundTripper.
type roundTripper struct {
	signer *Signer
	next   http.RoundTripper
	obs    Observer
}

// WrapTransport returns next wrapped so that every request through it is signed by s.
// One Signer may back several transports, which then share its credential cache.
func WrapTransport(s *Signer, next http.RoundTripper, obs Observer) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport //nolint:forbidigo // a caller that gives no transport gets Go's own
	}
	return &roundTripper{signer: s, next: next, obs: obs}
}

// RoundTrip sends a signed clone of r, which it must not modify, and resends once with renewed
// credentials if the origin rejects ones that refresh can renew.
func (rt *roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("aws: reading request body to sign: %w", err)
		}
		body = b
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, creds, err := rt.send(r, body)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		return resp, err
	}
	code := authErrorCode(resp)
	if code == "" || r.Context().Err() != nil || !rt.signer.refresh(r.Context(), creds) {
		return resp, nil
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, drainBytes))
	resp.Body.Close()
	if rt.obs.CredentialsRetried != nil {
		rt.obs.CredentialsRetried(code)
	}
	resp, _, err = rt.send(r, body)
	return resp, err
}

// send signs a clone of r carrying its own reader over body, and sends it.
func (rt *roundTripper) send(r *http.Request, body []byte,
) (*http.Response, aws.Credentials, error) {
	signed := r.Clone(r.Context())
	if body != nil {
		signed.Body = io.NopCloser(bytes.NewReader(body))
	}
	creds, err := rt.signer.sign(r.Context(), signed)
	if err != nil {
		if rt.obs.SignFailed != nil {
			rt.obs.SignFailed(err)
		}
		return nil, creds, err
	}
	resp, err := rt.next.RoundTrip(signed)
	return resp, creds, err
}

// authErrorCode returns the auth error code of a 403, or "" if it has none in authErrorCodes.
// Any body it reads to find out is restored, so the response is unchanged for the caller.
func authErrorCode(resp *http.Response) string {
	if v := resp.Header.Get(headerErrorType); v != "" {
		if code, _, _ := strings.Cut(v, ":"); slices.Contains(authErrorCodes, code) {
			return code
		}
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		return ""
	}
	peek, _ := io.ReadAll(io.LimitReader(resp.Body, authErrorPeekBytes))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(peek), resp.Body), resp.Body}
	for _, code := range authErrorCodes {
		if bytes.Contains(peek, []byte(code)) {
			return code
		}
	}
	return ""
}
