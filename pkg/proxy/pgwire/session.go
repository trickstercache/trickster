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
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	codeCancelRequest = 80877102
	codeSSLRequest    = 80877103
	codeGSSEncRequest = 80877104

	protocolMajor     = 3
	protocolMinorBase = 0
	// maxStartupPacketLen is PostgreSQL's MAX_STARTUP_PACKET_LENGTH.
	maxStartupPacketLen = 10000
	minStartupPacketLen = 8
	// maxStartupFrameLen bounds each origin message relayed before ReadyForQuery.
	maxStartupFrameLen = 1 << 20
	// maxPreStartupRequests allows one SSLRequest and one GSSENCRequest.
	maxPreStartupRequests = 2

	sslRefused = 'N'

	paramUser        = "user"
	paramDatabase    = "database"
	paramReplication = "replication"
	// protocolOptionPrefix marks protocol extensions, which the server must
	// report back when it does not implement them.
	protocolOptionPrefix = "_pq_."

	severityFatal           = "FATAL"
	sqlstateProtocol        = "08P01"
	sqlstateUnsupported     = "0A000"
	sqlstateInvalidAuthSpec = "28000"
	sqlstateInvalidPassword = "28P01"
	sqlstateTooManyConns    = "53300"
	sqlstateConnectFailure  = "08006"

	authOK           = 0
	authSASLFinal    = 12
	authTypeLen      = 4
	backendKeyPIDLen = 4
)

var errStartup = errors.New("invalid startup packet")

type session struct {
	// front owns the listener; server is the backend the session runs on, which
	// differs from front only once a user router has picked a target.
	front             *Server
	server            *Server
	client            net.Conn
	upstream          net.Conn
	secure            bool
	servedCertificate *tls.Certificate

	user       string
	database   string
	params     map[string]string
	rawStartup []byte
	minor      uint32

	tracker        *sessionTracker
	upstreamReader *bufio.Reader
	handoff        upstreamHandoff
	relayResumed   bool
	pid            uint32
	realPID        uint32
	realSecret     []byte

	closeMtx sync.Mutex
	closed   bool

	outstanding  atomic.Int64
	requestStart atomic.Int64
	txStatus     atomic.Uint32
	rows         int64
	failed       bool
	emptyQuery   bool
	completed    bool
}

func (s *session) serve() {
	front := s.front
	backend := front.config.BackendName
	front.countEvent(eventAccepted)
	metrics.PGWireActiveConnections.WithLabelValues(backend).Inc()
	defer func() {
		s.close()
		if s.pid != 0 {
			front.keys.release(s.pid)
		}
		metrics.PGWireActiveConnections.WithLabelValues(backend).Dec()
		front.countEvent(eventClosed)
		front.untrack(s)
	}()
	if timeout := front.config.HandshakeTimeout; timeout > 0 {
		_ = s.client.SetDeadline(time.Now().Add(timeout))
	}
	proceed, err := s.startup()
	if err != nil {
		front.countError(classStartup)
		return
	}
	if !proceed {
		return
	}
	routed := front.routes != nil
	if routed {
		// the route depends on who the client is, so it authenticates before a target is known
		if s.authenticateClient() != nil || !s.route() {
			return
		}
	}
	target := s.server
	if !target.reserveUpstream() {
		target.countError(classAdmission)
		s.fatal(sqlstateTooManyConns, "sorry, too many clients already")
		return
	}
	defer target.releaseUpstream()
	switch {
	case routed:
		err = s.loginTerminated()
	case target.config.Terminated():
		err = s.connectTerminated()
	default:
		err = s.connectPassthrough()
	}
	if err != nil {
		return
	}
	front.countEvent(eventAuthenticated)
	_ = s.client.SetDeadline(time.Time{})
	_ = s.upstream.SetDeadline(time.Time{})
	s.relay()
}

func (s *session) close() {
	s.closeMtx.Lock()
	defer s.closeMtx.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	_ = s.client.Close()
	if s.upstream != nil {
		_ = s.upstream.Close()
	}
}

func (s *session) setUpstream(conn net.Conn) bool {
	s.closeMtx.Lock()
	defer s.closeMtx.Unlock()
	if s.closed {
		_ = conn.Close()
		return false
	}
	s.upstream = conn
	return true
}

func (s *session) startup() (bool, error) {
	// handles everything before authentication. It returns false when the
	// connection was fully served already, e.g. a cancel request.
	for range maxPreStartupRequests + 1 {
		code, packet, err := readStartupPacket(s.client)
		if err != nil {
			return false, err
		}
		switch code {
		case codeSSLRequest:
			if err = s.answerSSLRequest(); err != nil {
				return false, err
			}
		case codeGSSEncRequest:
			if _, err = s.client.Write([]byte{sslRefused}); err != nil {
				return false, err
			}
		case codeCancelRequest:
			s.forwardCancel(packet[minStartupPacketLen:])
			return false, nil
		default:
			return s.acceptStartupMessage(code, packet)
		}
	}
	return false, errStartup
}

func (s *session) answerSSLRequest() error {
	base := s.front.inboundTLS.Load()
	if base == nil || s.secure {
		_, err := s.client.Write([]byte{sslRefused})
		return err
	}
	if _, err := s.client.Write([]byte{sslAccepted}); err != nil {
		return err
	}
	// Bytes a client sends in the clear after SSLRequest are never read: the
	// TLS server reads the socket directly, so nothing can be injected.
	secured := tls.Server(s.client, bindingTLSConfig(base, &s.servedCertificate))
	if err := secured.Handshake(); err != nil {
		return err
	}
	s.closeMtx.Lock()
	s.client, s.secure = secured, true
	s.closeMtx.Unlock()
	return nil
}

func (s *session) acceptStartupMessage(version uint32, packet []byte) (bool, error) {
	if version>>16 != protocolMajor {
		s.fatal(sqlstateUnsupported, fmt.Sprintf("unsupported frontend protocol %d.%d",
			version>>16, version&0xffff))
		return false, errStartup
	}
	params, err := parseStartupParams(packet[minStartupPacketLen:])
	if err != nil {
		s.fatal(sqlstateProtocol, "invalid startup packet layout")
		return false, err
	}
	if s.server.config.RequireSecureTransport && !s.secure {
		s.fatal(sqlstateInvalidAuthSpec, "this server requires an encrypted connection")
		return false, errStartup
	}
	s.user = params[paramUser]
	if s.user == "" {
		s.fatal(sqlstateInvalidAuthSpec, "no PostgreSQL user name specified in startup packet")
		return false, errStartup
	}
	s.database, s.params, s.rawStartup, s.minor = params[paramDatabase], params, packet, version&0xffff
	if s.server.config.Analyzer != nil {
		s.tracker = newSessionTracker(s.user, s.database, params)
	}
	return true, nil
}

func readStartupPacket(r io.Reader) (uint32, []byte, error) {
	var lengthBytes [frameLenSize]byte
	if _, err := io.ReadFull(r, lengthBytes[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(lengthBytes[:])
	if length < minStartupPacketLen || length > maxStartupPacketLen {
		return 0, nil, errStartup
	}
	packet := make([]byte, length)
	copy(packet, lengthBytes[:])
	if _, err := io.ReadFull(r, packet[frameLenSize:]); err != nil {
		return 0, nil, err
	}
	return binary.BigEndian.Uint32(packet[frameLenSize:]), packet, nil
}

func parseStartupParams(payload []byte) (map[string]string, error) {
	params := make(map[string]string)
	for {
		name, rest, ok := bytes.Cut(payload, []byte{0})
		if !ok {
			return nil, errStartup
		}
		if len(name) == 0 {
			if len(rest) != 0 {
				return nil, errStartup
			}
			return params, nil
		}
		value, rest, ok := bytes.Cut(rest, []byte{0})
		if !ok {
			return nil, errStartup
		}
		params[string(name)] = string(value)
		payload = rest
	}
}

func (s *session) forwardCancel(key []byte) {
	// maps a Trickster-issued cancellation key to the origin's and
	// relays the request. Like PostgreSQL, it never answers the requester.
	if len(key) < backendKeyPIDLen+legacySecretLen {
		return
	}
	target := s.front.keys.lookup(binary.BigEndian.Uint32(key), key[backendKeyPIDLen:])
	if target == nil {
		s.server.countError(classCancel)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.server.cancelBudget())
	defer cancel()
	if err := target.origin.cancelUpstream(ctx, target.realPID, target.realSecret); err != nil {
		s.server.countError(classCancel)
		return
	}
	s.server.countEvent(eventCanceled)
}

func (s *Server) cancelBudget() time.Duration {
	return s.config.ConnectTimeout + cancelDrainTimeout
}

func (s *session) connectPassthrough() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.server.config.HandshakeTimeout)
	defer cancel()
	upstream, err := s.server.config.dialUpstream(ctx)
	if err != nil {
		return s.upstreamFailed(err)
	}
	if !s.setUpstream(upstream) {
		return net.ErrClosed
	}
	_ = upstream.SetDeadline(time.Now().Add(s.server.config.HandshakeTimeout))
	if _, err = upstream.Write(s.rawStartup); err != nil {
		return s.upstreamFailed(err)
	}
	for {
		typ, body, err := readFrame(upstream, maxStartupFrameLen)
		if err != nil {
			return s.upstreamFailed(err)
		}
		switch typ {
		case msgBackendKeyData:
			if body, err = s.issueKey(body); err != nil {
				return s.upstreamFailed(err)
			}
		case msgParameterStatus:
			s.observeParameterStatus(body)
		}
		if _, err = s.client.Write(appendFrame(nil, typ, body)); err != nil {
			return err
		}
		switch typ {
		case msgErrorResponse:
			s.server.countError(classAuthentication)
			return errAuthFailed
		case msgReadyForQuery:
			s.observeReady(body)
			return nil
		case msgAuthentication:
			if err = s.relayAuthResponse(body); err != nil {
				return err
			}
		}
	}
}

func (s *session) relayAuthResponse(request []byte) error {
	// forwards the client's answer to an authentication request
	// that expects one. AuthenticationOk and SASLFinal are the two that do not.
	if len(request) < authTypeLen {
		return s.upstreamFailed(errFrameLength)
	}
	if kind := binary.BigEndian.Uint32(request); kind == authOK || kind == authSASLFinal {
		return nil
	}
	typ, body, err := readFrame(s.client, maxAuthMessageLen)
	if err != nil {
		return err
	}
	_, err = s.upstream.Write(appendFrame(nil, typ, body))
	return err
}

func (s *session) issueKey(body []byte) ([]byte, error) {
	if len(body) < backendKeyPIDLen+legacySecretLen {
		return nil, errFrameLength
	}
	s.realPID = binary.BigEndian.Uint32(body)
	s.realSecret = slices.Clone(body[backendKeyPIDLen:])
	pid, secret, err := s.front.keys.register(&s.server.config, s.realPID, s.realSecret)
	if err != nil {
		return nil, err
	}
	s.pid = pid
	return append(binary.BigEndian.AppendUint32(nil, pid), secret...), nil
}

func (s *session) connectTerminated() error {
	if err := s.authenticateClient(); err != nil {
		return err
	}
	return s.loginTerminated()
}

func (s *session) authenticateClient() error {
	if _, replication := s.params[paramReplication]; replication {
		s.fatal(sqlstateUnsupported, "replication connections are not supported with an authenticator")
		return errStartup
	}
	_, protocolOptions := s.upstreamParams()
	// The origin session always speaks 3.0, so a newer request is negotiated
	// down. PostgreSQL puts the whole version number in this field, not the minor.
	if s.minor > protocolMinorBase || len(protocolOptions) > 0 {
		if err := s.send(&pgproto3.NegotiateProtocolVersion{
			NewestMinorProtocol: pgproto3.ProtocolVersion30, UnrecognizedOptions: protocolOptions,
		}); err != nil {
			return err
		}
	}
	if err := s.authenticate(); err != nil {
		s.front.countError(classAuthentication)
		s.front.logFailure("postgres client authentication failed", s.user, err)
		s.fatal(sqlstateInvalidPassword, fmt.Sprintf("password authentication failed for user %q", s.user))
		return err
	}
	return s.send(&pgproto3.AuthenticationOk{})
}

func (s *session) loginTerminated() error {
	upstreamParams, _ := s.upstreamParams()
	ctx, cancel := context.WithTimeout(context.Background(), s.server.config.HandshakeTimeout)
	defer cancel()
	hijacked, defaults, err := s.server.config.loginUpstream(ctx, s.database, upstreamParams, s.tracker != nil)
	if err != nil {
		return s.upstreamFailed(err)
	}
	if s.tracker != nil {
		s.tracker.sessionDefaults(defaults)
	}
	if !s.setUpstream(hijacked.Conn) {
		return net.ErrClosed
	}
	body, err := s.issueKey(append(binary.BigEndian.AppendUint32(nil, hijacked.PID), hijacked.SecretKey...))
	if err != nil {
		return s.upstreamFailed(err)
	}
	var greeting []byte
	names := make([]string, 0, len(hijacked.ParameterStatuses))
	for name := range hijacked.ParameterStatuses {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if s.tracker != nil {
			s.tracker.parameterStatus(name, hijacked.ParameterStatuses[name])
		}
		greeting = appendFrame(greeting, msgParameterStatus,
			append(append([]byte(name), 0), append([]byte(hijacked.ParameterStatuses[name]), 0)...))
	}
	greeting = appendFrame(greeting, msgBackendKeyData, body)
	greeting = appendFrame(greeting, msgReadyForQuery, []byte{hijacked.TxStatus})
	s.txStatus.Store(uint32(hijacked.TxStatus))
	_, err = s.client.Write(greeting)
	return err
}

func (s *session) upstreamParams() (map[string]string, []string) {
	params := make(map[string]string, len(s.params))
	var protocolOptions []string
	for name, value := range s.params {
		switch {
		case strings.HasPrefix(name, protocolOptionPrefix):
			protocolOptions = append(protocolOptions, name)
		case name != paramUser && name != paramDatabase:
			params[name] = value
		}
	}
	slices.Sort(protocolOptions)
	return params, protocolOptions
}

func (s *session) observeParameterStatus(body []byte) {
	if s.tracker == nil {
		return
	}
	name, rest, ok := bytes.Cut(body, []byte{0})
	if !ok {
		return
	}
	value, _, _ := bytes.Cut(rest, []byte{0})
	s.tracker.parameterStatus(string(name), string(value))
}

func (s *session) upstreamFailed(err error) error {
	s.server.countError(classUpstream)
	s.server.logFailure("postgres origin connection failed", s.user, err)
	s.fatal(sqlstateConnectFailure, "could not connect to the origin server")
	return err
}

func (s *session) send(message pgproto3.BackendMessage) error {
	encoded, err := message.Encode(nil)
	if err != nil {
		return err
	}
	_, err = s.client.Write(encoded)
	return err
}

func (s *session) fatal(code, message string) {
	_ = s.send(&pgproto3.ErrorResponse{
		Severity: severityFatal, SeverityUnlocalized: severityFatal, Code: code, Message: message,
	})
}
