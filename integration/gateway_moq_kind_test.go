/*
 * Copyright 2026 The Trickster Authors
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

package integration

import (
	"context"
	"crypto/tls"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	gatewayMoQAddr   = "127.0.0.1:30089" // the moq listener's NodePort, from integration/kind/gateway-moq.yaml
	gatewayMoQOrigin = "127.0.0.1:30090" // the origin relay's NodePort, which the test publishes to
	gatewayMoQHost   = "moq.example.com"
	gatewayMoQDraft  = 21
	gatewayMoQGroups = 3
)

func moqGatewayTLS(alpns ...string) *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, ServerName: gatewayMoQHost, // #nosec G402 -- self-signed
		NextProtos: alpns, MinVersion: tls.VersionTLS13}
}

func quicServedSerial(addr string) *big.Int {
	// the serial of the certificate the listener presents for the scenario's host, nil when the handshake fails
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, moqGatewayTLS(codec.ALPNFor(gatewayMoQDraft)), nil)
	if err != nil {
		return nil
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	certs := conn.ConnectionState().TLS.PeerCertificates
	if len(certs) == 0 {
		return nil
	}
	return certs[0].SerialNumber
}

func dialGatewayMoQ(t *testing.T) *session.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), moqWait)
	defer cancel()
	conn, err := quic.DialAddr(ctx, gatewayMoQAddr, moqGatewayTLS(codec.ALPNs(codec.ClientOffer)...),
		&quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	require.NoError(t, err)
	c, ok := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
	require.True(t, ok)
	s, err := session.New(session.FromQUIC(conn), session.Options{
		Role: session.RoleClient, Codec: c,
		Setup: message.Setup{Path: "/", HasPath: true, Authority: gatewayMoQHost, HasAuthority: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	require.NoError(t, s.Start(ctx))
	return s
}

func TestGatewayMoQKind(t *testing.T) {
	// a UDPRoute on a trickstercache.org/moq listener relays a moq-relay origin's track through the Gateway, which
	// terminates QUIC with the certificate pushed from the listener's Secret
	skipUnlessKind(t)
	manifest, serial := tlsSecretManifest(t, "moq-tls", gatewayMoQHost)
	applyKind(t, manifest)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		got := quicServedSerial(gatewayMoQAddr)
		if assert.NotNil(collect, got, "no certificate served yet") {
			assert.Zero(collect, got.Cmp(serial), "serving %s, want %s", got, serial)
		}
	}, 3*time.Minute, 500*time.Millisecond, "the moq listener never served the Secret's certificate")

	// the origin outlives a run, so each run publishes under a namespace of its own
	ns := wire.Namespace{[]byte("kind"), []byte(strconv.FormatInt(time.Now().UnixNano(), 36))}
	pub := publishTo(t, gatewayMoQOrigin, gatewayMoQDraft, ns)
	lt := subscribeThrough(t, dialGatewayMoQ(t), pub, message.FullTrackName{Namespace: ns, Name: []byte(moqTrack)})
	for range gatewayMoQGroups {
		lt.publish()
	}
}
