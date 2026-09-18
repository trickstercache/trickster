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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

func rawConn(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(fakeTimeout))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func startupPacket(code uint32, pairs ...string) []byte {
	body := binary.BigEndian.AppendUint32(nil, code)
	for _, s := range pairs {
		body = append(append(body, s...), 0)
	}
	if len(pairs) > 0 {
		body = append(body, 0)
	}
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body)+frameLenSize)), body...)
}

func writeStartup(t *testing.T, conn net.Conn, code uint32, pairs ...string) {
	t.Helper()
	if _, err := conn.Write(startupPacket(code, pairs...)); err != nil {
		t.Fatal(err)
	}
}

func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := io.ReadAll(conn); err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatal("expected the server to close the connection")
		}
	}
}

func expectFatal(t *testing.T, conn net.Conn) string {
	t.Helper()
	typ, body, err := readFrame(conn, maxStartupFrameLen)
	if err != nil || typ != msgErrorResponse {
		t.Fatalf("expected an ErrorResponse, got %q, %v", typ, err)
	}
	var response pgproto3.ErrorResponse
	if err = response.Decode(body); err != nil {
		t.Fatal(err)
	}
	if response.Severity != severityFatal {
		t.Fatalf("expected severity %s, got %s", severityFatal, response.Severity)
	}
	return response.Code
}

func TestStartupRejections(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	_, address := startServer(t, testConfig(upstream))
	for name, test := range map[string]struct {
		packet []byte
		code   string
	}{
		"protocol 2":        {startupPacket(2<<16, paramUser, testClientUser), sqlstateUnsupported},
		"no user":           {startupPacket(protocolMajor<<16, paramDatabase, testDatabase), sqlstateInvalidAuthSpec},
		"odd pair count":    {startupPacket(protocolMajor<<16, paramUser)[:len(startupPacket(protocolMajor<<16, paramUser))-1], ""},
		"unterminated name": {append(binary.BigEndian.AppendUint32(nil, 12), 0, 3, 0, 0, 'u', 's', 'e', 'r'), sqlstateProtocol},
	} {
		t.Run(name, func(t *testing.T) {
			conn := rawConn(t, address)
			packet := test.packet
			if test.code == "" {
				// re-frame the truncated packet so its length field is honest
				packet = append(binary.BigEndian.AppendUint32(nil, uint32(len(packet))), packet[frameLenSize:]...)
				test.code = sqlstateProtocol
			}
			if _, err := conn.Write(packet); err != nil {
				t.Fatal(err)
			}
			if code := expectFatal(t, conn); code != test.code {
				t.Fatalf("expected SQLSTATE %s, got %s", test.code, code)
			}
			expectClosed(t, conn)
		})
	}
	for name, packet := range map[string][]byte{
		"length too small": {0, 0, 0, 4},
		"length too large": binary.BigEndian.AppendUint32(nil, maxStartupPacketLen+1),
		"short body":       {0, 0, 0, 12, 0, 3},
	} {
		t.Run(name, func(t *testing.T) {
			conn := rawConn(t, address)
			if _, err := conn.Write(packet); err != nil {
				t.Fatal(err)
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.CloseWrite()
			}
			expectClosed(t, conn)
		})
	}
}

func TestPreStartupRequests(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	_, address := startServer(t, testConfig(upstream))
	conn := rawConn(t, address)
	answer := make([]byte, 1)
	for _, code := range []uint32{codeGSSEncRequest, codeSSLRequest} {
		writeStartup(t, conn, code)
		if _, err := io.ReadFull(conn, answer); err != nil || answer[0] != sslRefused {
			t.Fatalf("request %d: expected a refusal, got %q, %v", code, answer, err)
		}
	}
	// a third pre-startup request is one more than any client needs
	writeStartup(t, conn, codeGSSEncRequest)
	_, _ = io.ReadFull(conn, answer)
	writeStartup(t, conn, codeGSSEncRequest)
	expectClosed(t, conn)
}

func TestParseStartupParams(t *testing.T) {
	params, err := parseStartupParams(startupPacket(0, paramUser, testClientUser, paramDatabase, "")[minStartupPacketLen:])
	if err != nil || params[paramUser] != testClientUser || len(params) != 2 {
		t.Fatalf("unexpected result %v, %v", params, err)
	}
	for name, payload := range map[string][]byte{
		"empty":          {},
		"no terminator":  []byte("user\x00x\x00"),
		"missing value":  []byte("user\x00x"),
		"trailing bytes": []byte("user\x00x\x00\x00junk"),
	} {
		if _, err := parseStartupParams(payload); !errors.Is(err, errStartup) {
			t.Fatalf("%s: expected errStartup, got %v", name, err)
		}
	}
}

func TestTerminatedRefusesReplicationAndDefaultsDatabase(t *testing.T) {
	upstream := newFakeUpstream(t, cleartextUpstream)
	config := terminatedConfig(upstream, testClientPass)
	_, address := startServer(t, config)
	if _, err := dial(t, address, testClientUser, testClientPass, paramReplication+"=database"); sqlstate(err) != sqlstateUnsupported {
		t.Fatalf("expected replication to be refused, got %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
	defer cancel()
	hijacked, err := config.loginUpstream(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = hijacked.Conn.Close()
	if database := upstream.lastStartup()[paramDatabase]; database != testDatabase {
		t.Fatalf("expected the origin_url database %q, got %q", testDatabase, database)
	}
	config.Upstream.Address = "not-an-address"
	if _, err = config.loginUpstream(ctx, "", nil); err == nil {
		t.Fatal("expected an invalid origin address to be rejected")
	}
}

func TestProtocolOptionsAreReportedUnrecognized(t *testing.T) {
	upstream := newFakeUpstream(t, cleartextUpstream)
	_, address := startServer(t, terminatedConfig(upstream, testClientPass))
	conn := rawConn(t, address)
	const option = protocolOptionPrefix + "future_feature"
	writeStartup(t, conn, protocolMajor<<16, paramUser, testClientUser, option, "on")
	typ, body, err := readFrame(conn, maxStartupFrameLen)
	if err != nil || typ != 'v' {
		t.Fatalf("expected NegotiateProtocolVersion, got %q, %v", typ, err)
	}
	var negotiate pgproto3.NegotiateProtocolVersion
	if err = negotiate.Decode(body); err != nil {
		t.Fatal(err)
	}
	if negotiate.NewestMinorProtocol != pgproto3.ProtocolVersion30 ||
		len(negotiate.UnrecognizedOptions) != 1 || negotiate.UnrecognizedOptions[0] != option {
		t.Fatalf("unexpected negotiation %+v", negotiate)
	}
	if typ, _, err = readFrame(conn, maxStartupFrameLen); err != nil || typ != msgAuthentication {
		t.Fatalf("expected an authentication request, got %q, %v", typ, err)
	}
}

func TestTerminatedAuthProtocolViolations(t *testing.T) {
	upstream := newFakeUpstream(t, cleartextUpstream)
	_, address := startServer(t, terminatedConfig(upstream, testClientPass))
	for name, reply := range map[string][]byte{
		"wrong message type": appendFrame(nil, msgQuery, []byte("select 1\x00")),
		"undecodable SASL":   appendFrame(nil, msgPassword, []byte("no-terminator")),
		"unknown mechanism":  appendFrame(nil, msgPassword, append([]byte("PLAIN\x00"), 0, 0, 0, 0)),
		"malformed first":    appendFrame(nil, msgPassword, append([]byte(mechSCRAMSHA256+"\x00"), 0, 0, 0, 3, 'x', ',', ',')),
	} {
		t.Run(name, func(t *testing.T) {
			conn := rawConn(t, address)
			writeStartup(t, conn, protocolMajor<<16, paramUser, testClientUser)
			if typ, _, err := readFrame(conn, maxStartupFrameLen); err != nil || typ != msgAuthentication {
				t.Fatalf("expected an authentication request, got %q, %v", typ, err)
			}
			if _, err := conn.Write(reply); err != nil {
				t.Fatal(err)
			}
			if code := expectFatal(t, conn); code != sqlstateInvalidPassword {
				t.Fatalf("expected SQLSTATE %s, got %s", sqlstateInvalidPassword, code)
			}
		})
	}
	if upstream.lastStartup() != nil {
		t.Fatal("the origin must not be contacted before authentication succeeds")
	}
}

func TestReadStartupPacket(t *testing.T) {
	code, packet, err := readStartupPacket(bytes.NewReader(startupPacket(codeSSLRequest)))
	if err != nil || code != codeSSLRequest || len(packet) != minStartupPacketLen {
		t.Fatalf("unexpected result %d, %v, %v", code, packet, err)
	}
	if _, _, err = readStartupPacket(bytes.NewReader(nil)); err == nil {
		t.Fatal("expected an empty stream to fail")
	}
}
