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

// Package dnsserver provides an in-process DNS server for tests. It serves a
// mutable record set on a loopback port over both UDP and TCP, and can be
// told to fail or truncate answers to exercise those paths.
package dnsserver

import (
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/dns/client"
)

const (
	listenAttempts  = 5
	udpReadBufSize  = 4096
	tcpConnDeadline = 10 * time.Second
)

// Server is an in-process DNS server bound to a loopback port
type Server struct {
	mtx      sync.Mutex
	records  map[client.Type][]client.Record
	rcode    client.RCode
	truncate bool

	addr      string
	pc        net.PacketConn
	ln        net.Listener
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// New starts a server on a free loopback port and stops it at test cleanup
func New(t *testing.T) *Server {
	t.Helper()
	ln, pc := listenPair(t)
	s := &Server{
		records: make(map[client.Type][]client.Record),
		addr:    ln.Addr().String(),
		pc:      pc,
		ln:      ln,
	}
	s.wg.Add(2)
	go s.serveUDP()
	go s.serveTCP()
	t.Cleanup(s.Stop)
	return s
}

// Addr returns the server's host:port
func (s *Server) Addr() string { return s.addr }

// Set replaces the records served for the given query type
func (s *Server) Set(qtype client.Type, records ...client.Record) {
	s.mtx.Lock()
	s.records[qtype] = records
	s.mtx.Unlock()
}

// SetRCode makes every subsequent answer carry rc and no records
func (s *Server) SetRCode(rc client.RCode) {
	s.mtx.Lock()
	s.rcode = rc
	s.mtx.Unlock()
}

// SetTruncate makes UDP answers come back with the truncated bit set and no
// records, which drives clients to retry the query over TCP
func (s *Server) SetTruncate(truncate bool) {
	s.mtx.Lock()
	s.truncate = truncate
	s.mtx.Unlock()
}

// Stop closes the server's listeners and waits for its handlers to finish
func (s *Server) Stop() {
	s.closeOnce.Do(func() {
		_ = s.pc.Close()
		_ = s.ln.Close()
	})
	s.wg.Wait()
}

func (s *Server) serveUDP() {
	defer s.wg.Done()
	buf := make([]byte, udpReadBufSize)
	for {
		n, addr, err := s.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		resp, err := s.respond(buf[:n], true)
		if err != nil {
			continue
		}
		_, _ = s.pc.WriteTo(resp, addr)
	}
}

func (s *Server) serveTCP() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.handleTCP(conn)
	}
}

func (s *Server) handleTCP(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(tcpConnDeadline))
	var sz [2]byte
	if _, err := io.ReadFull(conn, sz[:]); err != nil {
		return
	}
	buf := make([]byte, binary.BigEndian.Uint16(sz[:]))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}
	resp, err := s.respond(buf, false)
	if err != nil {
		return
	}
	framed := make([]byte, 2, len(resp)+2)
	// #nosec G115 -- test answers are orders of magnitude below the 64KB frame
	binary.BigEndian.PutUint16(framed, uint16(len(resp)))
	_, _ = conn.Write(append(framed, resp...))
}

// respond builds the wire-format answer to a query; udp answers honor the
// configured truncation, which does not apply to TCP
func (s *Server) respond(query []byte, udp bool) ([]byte, error) {
	req := &client.Msg{}
	if err := req.Unpack(query); err != nil {
		return nil, err
	}
	resp := &client.Msg{
		ID:                 req.ID,
		Response:           true,
		Authoritative:      true,
		RecursionDesired:   req.RecursionDesired,
		RecursionAvailable: true,
		Questions:          req.Questions,
	}
	s.mtx.Lock()
	resp.RCode = s.rcode
	resp.Truncated = s.truncate && udp
	if resp.RCode == client.RCodeSuccess && !resp.Truncated && len(req.Questions) > 0 {
		resp.Answers = s.records[req.Questions[0].Type]
	}
	s.mtx.Unlock()
	return resp.Pack()
}

// listenPair binds TCP and UDP to the same loopback port, retrying when the
// port picked for TCP is already taken on UDP
func listenPair(t *testing.T) (net.Listener, net.PacketConn) {
	t.Helper()
	for range listenAttempts {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		pc, err := net.ListenPacket("udp", ln.Addr().String())
		if err == nil {
			return ln, pc
		}
		_ = ln.Close()
	}
	t.Fatal("could not bind a loopback port for both tcp and udp")
	return nil, nil
}

// A returns an IPv4 address record, panicking on an unparsable address
func A(name string, ttl uint32, addr string) *client.A {
	return &client.A{
		Hdr:  header(name, client.TypeA, ttl),
		Addr: netip.MustParseAddr(addr),
	}
}

// AAAA returns an IPv6 address record, panicking on an unparsable address
func AAAA(name string, ttl uint32, addr string) *client.AAAA {
	return &client.AAAA{
		Hdr:  header(name, client.TypeAAAA, ttl),
		Addr: netip.MustParseAddr(addr),
	}
}

// SRV returns a service location record
func SRV(name string, ttl uint32, priority, weight, port uint16,
	target string,
) *client.SRV {
	return &client.SRV{
		Hdr:      header(name, client.TypeSRV, ttl),
		Priority: priority,
		Weight:   weight,
		Port:     port,
		Target:   client.Fqdn(target),
	}
}

func header(name string, qtype client.Type, ttl uint32) client.RecordHeader {
	return client.RecordHeader{
		Name:  client.Fqdn(name),
		Type:  qtype,
		Class: client.ClassINET,
		TTL:   ttl,
	}
}
