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

package moq

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/proxy/quicdial"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	sharedConnAttempts = 2
	pooledBidiStreams  = 1 << 10
	pooledUniStreams   = 1 << 14
	pooledData         = 64 << 20
)

var pooledFlowControl = &webtransport.Config{
	MaxIncomingStreams: pooledBidiStreams, MaxIncomingUniStreams: pooledUniStreams, MaxIncomingData: pooledData,
}

type wtConn struct {
	key      string
	qconn    *quic.Conn
	cc       *webtransport.ClientConn
	sessions int
	ready    chan struct{}
	err      error
}

// lateRejection is the reason a session ends with when its origin answers its CONNECT with 200 and then resets it
// with H3_REQUEST_REJECTED, as webtransport-go servers refuse a second session on a connection without flow control
var lateRejection = (&http3.Error{ErrorCode: http3.ErrCodeRequestRejected, Remote: true}).Error()

var (
	wtConnsMu sync.Mutex
	wtConns   = map[string]*wtConn{}
	unpooled  = map[string]struct{}{}
)

type wtDial struct {
	key, addr, target string
	transport         *webtransport.Transport
	tls               *tls.Config
	quic              *quic.Config
	direct            func(context.Context) (*webtransport.Session, error)
}

func (w *wtDial) session(ctx context.Context) (*webtransport.Session, error) {
	// backends with one host, port, TLS settings and drafts share a QUIC connection, each with its own session,
	// until the last ends; an origin that refuses a second session is dialed one connection per session
	for range sharedConnAttempts {
		wtConnsMu.Lock()
		_, alone := unpooled[w.key]
		wtConnsMu.Unlock()
		if alone {
			return w.direct(ctx)
		}
		c, err := w.conn(ctx)
		if err != nil {
			return nil, err
		}
		_, s, err := c.cc.Dial(ctx, w.target, nil)
		if err != nil {
			gone := c.qconn.Context().Err() != nil
			c.release()
			if rejected(err) {
				unpool(w.key)
				continue
			}
			if gone {
				continue
			}
			return nil, err
		}
		context.AfterFunc(s.Context(), c.release)
		return s, nil
	}
	return nil, errDialerClosed
}

func unpool(key string) {
	wtConnsMu.Lock()
	unpooled[key] = struct{}{}
	wtConnsMu.Unlock()
}

func lateRejected(err error) bool {
	ce, ok := errors.AsType[*session.CloseError](err)
	return ok && !ce.Remote && http3.ErrCode(ce.Code) == http3.ErrCodeDatagramError && ce.Reason == lateRejection
}

func rejected(err error) bool {
	if he, ok := errors.AsType[*http3.Error](err); ok && he.ErrorCode == http3.ErrCodeRequestRejected {
		return true
	}
	se, ok := errors.AsType[*quic.StreamError](err)
	return ok && http3.ErrCode(se.ErrorCode) == http3.ErrCodeRequestRejected
}

func (w *wtDial) conn(ctx context.Context) (*wtConn, error) {
	for {
		wtConnsMu.Lock()
		c := wtConns[w.key]
		if c != nil {
			// a connection is looked at only once its dial is done, which closing ready publishes
			select {
			case <-c.ready:
				if c.err != nil || c.qconn.Context().Err() != nil {
					delete(wtConns, w.key)
					c = nil
				}
			default:
			}
		}
		if c == nil {
			c = &wtConn{key: w.key, ready: make(chan struct{})}
			wtConns[w.key] = c
			c.sessions++
			wtConnsMu.Unlock()
			c.dial(ctx, w)
			if c.err != nil {
				c.release()
				return nil, c.err
			}
			return c, nil
		}
		c.sessions++
		ready := c.ready
		wtConnsMu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			c.release()
			return nil, ctx.Err()
		}
		if c.err == nil {
			return c, nil
		}
		// the dial another backend started failed; this one tries for itself
		c.release()
	}
}

func (c *wtConn) dial(ctx context.Context, w *wtDial) {
	defer close(c.ready)
	tc := w.tls.Clone()
	tc.NextProtos = []string{http3.NextProtoH3}
	qconn, err := quicdial.DialAddr(ctx, w.addr, tc, w.quic)
	if err != nil {
		c.err = err
		return
	}
	// several sessions on one HTTP/3 connection need WebTransport flow control at both ends
	// (draft-ietf-webtrans-http3); webtransport-go replenishes the credit as streams and data are consumed
	pooled := &webtransport.Transport{
		TLSClientConfig: w.transport.TLSClientConfig, QUICConfig: w.quic,
		ApplicationProtocols: w.transport.ApplicationProtocols, Config: pooledFlowControl,
	}
	cc, err := pooled.NewClientConn(qconn)
	if err != nil {
		_ = qconn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "")
		c.err = err
		return
	}
	c.qconn, c.cc = qconn, cc
}

func (c *wtConn) release() {
	wtConnsMu.Lock()
	c.sessions--
	last := c.sessions <= 0
	if last && wtConns[c.key] == c {
		delete(wtConns, c.key)
	}
	wtConnsMu.Unlock()
	if last && c.qconn != nil {
		_ = c.qconn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "")
	}
}
