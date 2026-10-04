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

package memconn

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/session"
)

func TestStreams(t *testing.T) {
	a, b := Pair()
	ctx := context.Background()
	st, err := a.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := b.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	got, err := io.ReadAll(peer)
	if err != nil || string(got) != "ping" {
		t.Fatalf("read %q %v", got, err)
	}
	if _, err = st.Write([]byte("x")); !errors.Is(err, ErrClosedWrite) {
		t.Errorf("write after FIN: %v", err)
	}
	if _, err = peer.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	peer.CancelWrite(7)
	buf := make([]byte, 8)
	if n, _ := st.Read(buf); string(buf[:n]) != "pong" {
		t.Errorf("data written before a reset is still read: %q", buf[:n])
	}
	if _, err = st.Read(buf); !isStreamErr(err, 7, true) {
		t.Errorf("read after a reset: %v", err)
	}

	us, err := a.OpenUniStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	urs, err := b.AcceptUniStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	us.SetPriority(3, true)
	us.SetReliableBoundary()
	rs := urs.(*ReceiveStream)
	if u, incr, set := rs.Priority(); u != 3 || !incr || !set || !rs.ReliableBoundary() {
		t.Error("the receiver reports the sender's priority and boundary")
	}
	urs.CancelRead(9)
	if _, err = us.Write([]byte("x")); !isStreamErr(err, 9, true) {
		t.Errorf("write after STOP_SENDING: %v", err)
	}
	if !isStreamErr(context.Cause(us.Context()), 9, true) {
		t.Errorf("a stopped stream's context: %v", context.Cause(us.Context()))
	}
	if _, err = urs.Read(buf); !isStreamErr(err, 9, false) {
		t.Errorf("read after CancelRead: %v", err)
	}
}

func isStreamErr(err error, code uint64, remote bool) bool {
	se, ok := errors.AsType[*session.StreamError](err)
	return ok && se.Code == code && se.Remote == remote
}

func TestDeadlinesAndCapacity(t *testing.T) {
	a, b := Pair()
	a.Capacity = 4
	st, _ := a.OpenUniStreamSync(context.Background())
	rs, _ := b.AcceptUniStream(context.Background())
	if _, err := st.Write([]byte("full")); err != nil {
		t.Fatal(err)
	}
	_ = st.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := st.Write([]byte("more")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("a full stream blocks until its deadline: %v", err)
	}
	go func() {
		time.Sleep(10 * time.Millisecond)
		buf := make([]byte, 4)
		_, _ = rs.Read(buf)
	}()
	_ = st.SetWriteDeadline(time.Time{})
	if _, err := st.Write([]byte("more")); err != nil {
		t.Errorf("a drained stream accepts writes: %v", err)
	}
	_ = rs.SetReadDeadline(time.Now().Add(time.Hour))
	buf := make([]byte, 8)
	if n, err := rs.Read(buf); err != nil || string(buf[:n]) != "more" {
		t.Errorf("read %q %v", buf[:n], err)
	}
	_ = rs.SetReadDeadline(time.Now().Add(-time.Second))
	if _, err := rs.Read(buf); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("read past its deadline: %v", err)
	}
	_ = rs.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := rs.Read(buf); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("read until its deadline: %v", err)
	}
}

func TestDatagrams(t *testing.T) {
	a, b := Pair()
	if err := a.SendDatagram(make([]byte, a.MaxDatagram+1)); !errors.Is(err, ErrDatagramTooLarge) {
		t.Errorf("oversize datagram: %v", err)
	}
	for range datagramBacklog + 1 {
		if err := a.SendDatagram([]byte("d")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.ReceiveDatagram(context.Background())
	if err != nil || string(got) != "d" {
		t.Errorf("datagram %q %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = a.ReceiveDatagram(ctx); err == nil {
		t.Error("a canceled receive returns")
	}
	_ = a.CloseWithError(1, "bye")
	if err = a.SendDatagram([]byte("d")); err == nil {
		t.Error("a closed connection sends nothing")
	}
}

func TestClose(t *testing.T) {
	a, b := Pair()
	ctx := context.Background()
	st, _ := a.OpenStreamSync(ctx)
	peer, _ := b.AcceptStream(ctx)
	if err := b.CloseWithError(5, "done"); err != nil {
		t.Fatal(err)
	}
	if err := a.CloseWithError(6, "again"); err != nil {
		t.Error("a second close is a no-op")
	}
	closeErr := func(err error) *session.CloseError {
		ce, _ := errors.AsType[*session.CloseError](err)
		return ce
	}
	buf := make([]byte, 1)
	if _, err := st.Read(buf); closeErr(err) == nil || !closeErr(err).Remote || closeErr(err).Code != 5 {
		t.Errorf("the far side reads a remote close: %v", err)
	}
	if _, err := peer.Write(buf); closeErr(err) == nil || closeErr(err).Remote {
		t.Errorf("the closing side writes into its own close: %v", err)
	}
	if _, err := st.Write(buf); closeErr(err) == nil || !closeErr(err).Remote {
		t.Errorf("the far side writes into a remote close: %v", err)
	}
	if ce := closeErr(a.Cause()); ce == nil || !ce.Remote || ce.Reason != "done" {
		t.Errorf("cause %v", a.Cause())
	}
	if a.Context().Err() == nil || b.Context().Err() == nil {
		t.Error("both ends are done")
	}
	if _, err := a.OpenStreamSync(ctx); err == nil {
		t.Error("no streams open on a closed connection")
	}
	if _, err := a.OpenUniStreamSync(ctx); err == nil {
		t.Error("no streams open on a closed connection")
	}
	if _, err := a.AcceptStream(ctx); err == nil {
		t.Error("no streams are accepted on a closed connection")
	}
	if _, err := a.AcceptUniStream(ctx); err == nil {
		t.Error("no streams are accepted on a closed connection")
	}
	c, d := Pair()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for range acceptBacklog {
		_, _ = c.OpenUniStreamSync(ctx)
	}
	if _, err := c.OpenUniStreamSync(canceled); err == nil {
		t.Error("an open beyond the backlog waits for its context")
	}
	for range acceptBacklog {
		_, _ = c.OpenStreamSync(ctx)
	}
	if _, err := c.OpenStreamSync(canceled); err == nil {
		t.Error("an open beyond the backlog waits for its context")
	}
	_ = d.CloseWithError(0, "")
	// a closed connection's streams report the close, from the side each half is on
	if _, err := c.OpenUniStreamSync(ctx); err == nil {
		t.Error("no streams open after the peer closed")
	}
	e, _ := Pair()
	if _, err := e.AcceptStream(canceled); err == nil {
		t.Error("accepting with a canceled context")
	}
	if _, err := e.AcceptUniStream(canceled); err == nil {
		t.Error("accepting with a canceled context")
	}
}
