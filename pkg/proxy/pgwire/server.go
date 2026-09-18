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
package pgwire

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricMethodQuery             = "QUERY"
	metricPathQuery               = "query"
	metricHTTPStatusOK            = "200"
	metricHTTPStatusInternalError = "500"

	eventAccepted      = "accepted"
	eventAuthenticated = "authenticated"
	eventClosed        = "closed"
	eventCanceled      = "cancel_forwarded"

	classStartup        = "startup"
	classAuthentication = "authentication"
	classAdmission      = "upstream_admission"
	classUpstream       = "upstream_connect"
	classProtocol       = "protocol"
	classCancel         = "cancel"

	logKeyBackend = "backendName"
	logKeyDetail  = "detail"
	logKeyUser    = "user"

	mockSecretLen       = 32
	acceptBackoff       = 5 * time.Millisecond
	acceptBackoffCeil   = time.Second
	pumpBufferSizeBytes = 32 * 1024
)

var pumpBuffers = sync.Pool{New: func() any {
	b := make([]byte, pumpBufferSizeBytes)
	return &b
}}

type requestMetrics struct {
	requests prometheus.Counter
	elements prometheus.Counter
	duration prometheus.Observer
}

func newRequestMetrics(backend, provider string, lookup status.LookupStatus, httpStatus string) requestMetrics {
	label := lookup.String()
	return requestMetrics{
		requests: metrics.ProxyRequestStatus.WithLabelValues(backend, provider,
			metricMethodQuery, label, httpStatus, metricPathQuery),
		elements: metrics.ProxyRequestElements.WithLabelValues(backend, provider, label, metricPathQuery),
		duration: metrics.ProxyRequestDuration.WithLabelValues(backend, provider,
			metricMethodQuery, label, httpStatus, metricPathQuery),
	}
}

// Server terminates the PostgreSQL startup exchange and relays each session to
// the one origin its backend names. Nothing is cached or rewritten.
type Server struct {
	config     Config
	inboundTLS atomic.Pointer[tls.Config]
	keys       *cancelRegistry
	auth       map[string]*authEntry
	mockSecret []byte
	proxied    requestMetrics
	failed     requestMetrics
	upstreams  atomic.Int64

	mtx      sync.Mutex
	listener net.Listener
	sessions map[*session]struct{}
	closing  bool
	wg       sync.WaitGroup
}

// NewServer returns a server ready to serve an existing net.Listener.
func NewServer(config Config) (*Server, error) {
	if config.Upstream.Address == "" {
		return nil, errors.New("postgres origin address is required")
	}
	s := &Server{
		config: config, keys: newCancelRegistry(), sessions: make(map[*session]struct{}),
		proxied: newRequestMetrics(config.BackendName, config.Provider,
			status.LookupStatusProxyOnly, metricHTTPStatusOK),
		failed: newRequestMetrics(config.BackendName, config.Provider,
			status.LookupStatusProxyError, metricHTTPStatusInternalError),
	}
	s.setInboundTLS(config.InboundTLS)
	if config.Terminated() {
		var err error
		if s.auth, err = newAuthEntries(config.Users); err != nil {
			return nil, err
		}
		s.mockSecret = make([]byte, mockSecretLen)
		if _, err = rand.Read(s.mockSecret); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Server) setInboundTLS(config *tls.Config) {
	if config != nil && len(config.Certificates) == 0 && config.GetCertificate == nil {
		config = nil
	}
	s.inboundTLS.Store(config)
}

// Serve runs the accept loop on l until Shutdown.
func (s *Server) Serve(l net.Listener) error {
	if s.config.RequireSecureTransport && s.inboundTLS.Load() == nil {
		return errors.New("postgres secure transport requires an inbound TLS configuration")
	}
	s.mtx.Lock()
	s.listener = l
	s.mtx.Unlock()
	backoff := acceptBackoff
	for {
		conn, err := l.Accept()
		if err != nil {
			if s.isClosing() {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				time.Sleep(backoff)
				backoff = min(backoff*2, acceptBackoffCeil)
				continue
			}
			return err
		}
		backoff = acceptBackoff
		sess := &session{server: s, client: conn}
		if !s.track(sess) {
			_ = conn.Close()
			return nil
		}
		go sess.serve()
	}
}

func (s *Server) isClosing() bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.closing
}

func (s *Server) track(sess *session) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.closing {
		return false
	}
	s.sessions[sess] = struct{}{}
	s.wg.Add(1)
	return true
}

func (s *Server) untrack(sess *session) {
	s.mtx.Lock()
	delete(s.sessions, sess)
	s.mtx.Unlock()
	s.wg.Done()
}

// Shutdown stops admission, closes every session, and waits for their
// handlers to leave until ctx expires.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mtx.Lock()
	s.closing = true
	listener := s.listener
	sessions := make([]*session, 0, len(s.sessions))
	for sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mtx.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	for _, sess := range sessions {
		sess.close()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// UpdateTLSConfig atomically rotates the certificate used by new in-band TLS
// handshakes. Existing TLS sessions keep their negotiated certificate state.
func (s *Server) UpdateTLSConfig(config *tls.Config) {
	if config != nil {
		s.setInboundTLS(config)
	}
}

// ProtocolRestartKey identifies the immutable transport/authentication state
// that a configuration reload cannot change without restarting the listener.
func (s *Server) ProtocolRestartKey() string { return s.config.RestartKey }

func (s *Server) reserveUpstream() bool {
	limit := s.config.MaxUpstreamConnections
	if s.upstreams.Add(1) > limit && limit > 0 {
		s.upstreams.Add(-1)
		return false
	}
	return true
}

func (s *Server) releaseUpstream() { s.upstreams.Add(-1) }

func (s *Server) countError(class string) {
	metrics.PGWireConnectionErrors.WithLabelValues(s.config.BackendName, class).Inc()
}

func (s *Server) countEvent(event string) {
	metrics.PGWireConnections.WithLabelValues(s.config.BackendName, event).Inc()
}

func (s *Server) logFailure(event, user string, err error) {
	logger.Warn(event, logging.Pairs{
		logKeyBackend: s.config.BackendName, logKeyUser: user, logKeyDetail: err,
	})
}
