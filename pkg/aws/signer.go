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

// Signer resolves AWS credentials and signs requests with SigV4.
//
// Resolution is lazy and retried. Doing it eagerly at construction would
// make Trickster's startup depend on the instance metadata service being
// reachable at that instant, which is a bad trade for a proxy whose job is
// to keep serving; and caching a failure permanently would turn a momentary
// IMDS blip into a dead signer. Only a successful resolution is cached.
type Signer struct {
	opts    *Options
	service string
	signer  *sigv4.Signer
	// now is overridable in tests
	now func() time.Time

	mtx      sync.Mutex
	cfg      *aws.Config
	resolved bool
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
	return &Signer{
		opts:    o.Clone(),
		service: o.GetService(),
		signer:  sigv4.NewSigner(),
		now:     time.Now,
	}, nil
}

// Service returns the signing service name this Signer uses.
func (s *Signer) Service() string { return s.service }

// config resolves and caches the AWS configuration.
func (s *Signer) config(ctx context.Context) (*aws.Config, error) {
	s.mtx.Lock()
	if s.resolved {
		cfg := s.cfg
		s.mtx.Unlock()
		return cfg, nil
	}
	s.mtx.Unlock()

	cfg, err := s.load(ctx)
	if err != nil {
		// deliberately not cached: a transient metadata-service failure
		// must not permanently disable signing
		return nil, err
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()
	if !s.resolved {
		s.cfg, s.resolved = cfg, true
	}
	return s.cfg, nil
}

// load builds the AWS configuration from the options and the standard
// credential chain.
func (s *Signer) load(ctx context.Context) (*aws.Config, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	loadOpts := make([]func(*awsconfig.LoadOptions) error, 0, 3)
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
			stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), s.opts.RoleARN))
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
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("aws: retrieving credentials: %w", err)
	}
	hash, err := hashPayload(r)
	if err != nil {
		return err
	}
	if err := s.signer.SignHTTP(ctx, creds, r, hash,
		s.service, cfg.Region, s.now().UTC()); err != nil {
		return fmt.Errorf("aws: signing request: %w", err)
	}
	return nil
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

// roundTripper signs each request before handing it to the next
// RoundTripper.
type roundTripper struct {
	signer *Signer
	next   http.RoundTripper
}

// NewRoundTripper wraps next so that every request through it is SigV4
// signed. It is how the proxy applies a backend's sigv4 block to its
// outbound requests.
func NewRoundTripper(o *Options, next http.RoundTripper) (http.RoundTripper, error) {
	s, err := NewSigner(o)
	if err != nil {
		return nil, err
	}
	if next == nil {
		next = http.DefaultTransport
	}
	return &roundTripper{signer: s, next: next}, nil
}

// RoundTrip implements http.RoundTripper.
func (rt *roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	// the RoundTripper contract forbids modifying the request, and signing
	// adds headers and rewinds the body, so sign a clone
	signed := r.Clone(r.Context())
	if r.Body != nil && r.Body != http.NoBody {
		// Clone shares the original body reader; give the clone its own so
		// hashing it does not consume the caller's
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("aws: reading request body to sign: %w", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		signed.Body = io.NopCloser(bytes.NewReader(body))
	}
	if err := rt.signer.SignRequest(r.Context(), signed); err != nil {
		return nil, err
	}
	return rt.next.RoundTrip(signed)
}
